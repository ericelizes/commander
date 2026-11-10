package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/claudeacct"
)

// Errors returned by commands.
var (
	ErrNotRunning = errors.New("chat: no running process for this role")
	ErrBusy       = errors.New("chat: a turn is in progress")
	ErrNoRequest  = errors.New("chat: no such pending request")
	ErrHandover   = errors.New("chat: handover/pop-out is disabled (enable it in the engine and pass confirm=true)")
)

// StateSink receives the derived agent state (store.Store implements it).
type StateSink interface {
	SetAgentState(ctx context.Context, role, state, detail string) error
}

// TmuxRunner runs `tmux args...` (layout.DefaultRunner implements it).
type TmuxRunner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// Options configures an Engine.
type Options struct {
	Home     string   // COMMANDER_HOME: logs in <Home>/chat, locks in <Home>/chat-locks
	Binary   string   // claude binary; default "claude"
	Resolver Resolver // role -> Spec
	States   StateSink
	Costs    CostSink // optional: per-turn cost of each role
	// OnUsageMove, when set, is told of every move a usage limit made (role, text).
	OnUsageMove func(role, text string)
	// CodexHooksReady gates a Manager's move onto Codex; default CodexHooksReady.
	CodexHooksReady func() bool
	// Usage keeps the latest plan usage per account; default OpenUsage(Home).
	Usage *UsageStore
	// Procs finds processes that reference a session id; default PSScanner.
	Procs ProcScanner
	// ProjectsDir is Claude's transcript root; default ClaudeProjectsDir().
	ProjectsDir string
	// Tmux and AllowHandover enable Handover/PopOut. Both are needed, and each
	// call must still pass Confirm.
	Tmux          TmuxRunner
	AllowHandover bool
	// InterruptGrace is how long Interrupt waits for the CLI's ack before
	// falling back to SIGINT; default 3s.
	InterruptGrace time.Duration
	// StopGrace is how long Stop waits for a clean exit before signals; default 5s.
	StopGrace time.Duration
	// HostBinary is the executable that runs chat hosts (it must call
	// MaybeRunHost first); default: this executable.
	HostBinary string
	// ResumeRetry is how often ResumeRunning retries a session still held by
	// the previous cmdrd's child; default 1s.
	ResumeRetry time.Duration
	// OrphanGrace is how long a headless claude we do not own may hold a
	// session during resume before it is ended; default 30s.
	OrphanGrace time.Duration
	// RingSize is how many recent events (deltas included) each role keeps in
	// memory for replay; default 20000.
	RingSize int
	// CrashRestart picks the roles that are started again (on their session)
	// when their process dies on its own; nil restarts none. Operators are
	// left stuck: cmdrd respawns them on their bead.
	CrashRestart func(role string) bool
	// CrashRestartDelay is the pause before that restart; default 2s.
	CrashRestartDelay time.Duration
	Now               func() time.Time
	Logf              func(format string, args ...any)
}

// Engine owns the headless Claude processes, one per role.
type Engine struct {
	opts Options

	mu         sync.Mutex
	chats      map[string]*chatRole
	owners     map[string]string // session id -> role, for live processes
	ownerCache map[string]ownerHit

	versionOnce sync.Once
	version     string

	// resumed closes once every role ResumeRunning brings back has had its
	// first start attempt; nil before ResumeRunning (nothing to wait for).
	resumed chan struct{}

	crashes map[string][]time.Time // role -> recent crash restarts, guarded by mu
}

// WaitResumed blocks until the chats running before a restart have been picked
// back up once, so a request cannot start a second process on one of them.
func (e *Engine) WaitResumed(ctx context.Context) error {
	e.mu.Lock()
	ch := e.resumed
	e.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// New returns an Engine.
func New(opts Options) *Engine {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.Procs == nil {
		opts.Procs = PSScanner
	}
	if opts.ProjectsDir == "" {
		opts.ProjectsDir = ClaudeProjectsDir()
	}
	if opts.InterruptGrace <= 0 {
		opts.InterruptGrace = 3 * time.Second
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = 5 * time.Second
	}
	if opts.ResumeRetry <= 0 {
		opts.ResumeRetry = time.Second
	}
	if opts.OrphanGrace <= 0 {
		opts.OrphanGrace = 30 * time.Second
	}
	if opts.RingSize <= 0 {
		opts.RingSize = 20000
	}
	if opts.CrashRestartDelay <= 0 {
		opts.CrashRestartDelay = 2 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Usage == nil {
		opts.Usage = OpenUsage(opts.Home)
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Engine{opts: opts, chats: map[string]*chatRole{}, owners: map[string]string{}, crashes: map[string][]time.Time{}}
}

func (e *Engine) logDir() string  { return filepath.Join(e.opts.Home, "chat") }
func (e *Engine) lockDir() string { return filepath.Join(e.opts.Home, "chat-locks") }

// chatRole is one role's history, subscribers and (maybe) live process.
type chatRole struct {
	e    *Engine
	role string

	mu        sync.Mutex
	log       *eventLog
	seq       uint64
	ring      []Event
	subs      map[*Subscription]struct{}
	tracker   *stateTracker
	lastState State
	meta      meta
	sessions  map[string]bool
	native    map[string]bool // sessions the engine ran (see logSummary.native)
	hydrated  map[string]int  // transcript events published per session
	syncMu    sync.Mutex      // serializes syncTranscript
	syncSize  int64           // transcript size at the last sync
	proc      *process
	starting  bool
	// limit is the usage limit the last turn stopped at (Status.UsageLimit);
	// turnLimit is one seen in the current turn, decided at its result.
	limit, turnLimit *UsageLimit
}

// process is one running claude.
type process struct {
	spec      Spec
	pid       int
	hc        *hostConn
	norm      *Normalizer
	startedAt time.Time
	resumed   bool
	done      chan struct{}

	pmu     sync.Mutex
	pending map[string]*ControlRequest
	acks    map[string]chan struct{}
	reqN    int

	stopping bool        // guarded by chatRole.mu
	detached bool        // guarded by chatRole.mu: Close let go of the host; claude keeps running
	info     SessionInfo // last init, guarded by chatRole.mu
	handoff  atomic.Bool // the Claude handoff note went out (takeClaudeHandoff)

	lineMu  sync.Mutex // one stdout line being published (Close waits for it)
	ackMu   sync.Mutex
	flushMu sync.Mutex // one flush at a time, so Close's flush waits for one in flight
	ackIdx  int64      // journal lines below this are published
	ackSent int64
	ackKick chan struct{}
}

func (e *Engine) chat(role string) (*chatRole, error) {
	if !ValidRole(role) {
		return nil, fmt.Errorf("chat: invalid role %q", role)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.chats[role]; ok {
		return c, nil
	}
	sum, err := summarizeLog(filepath.Join(e.logDir(), role+".jsonl"))
	if err != nil {
		return nil, err
	}
	l, err := openEventLog(e.logDir(), role)
	if err != nil {
		return nil, err
	}
	c := &chatRole{e: e, role: role, log: l, seq: sum.lastSeq, subs: map[*Subscription]struct{}{},
		tracker: newStateTracker(), meta: readMeta(e.logDir(), role), sessions: sum.sessions, native: sum.native, hydrated: sum.hydrated,
		lastState: State{State: "idle"}}
	e.chats[role] = c
	return c, nil
}

// publish assigns seqs, persists, buffers and fans out events, then records a
// changed agent state.
func (c *chatRole) publish(events ...Event) {
	c.mu.Lock()
	var changed *State
	for _, ev := range events {
		c.emitLocked(ev)
		if st := c.tracker.state(); st != c.lastState {
			c.lastState = st
			s := st
			changed = &s
			c.emitLocked(Event{Kind: KindState, Source: SourceEngine, Time: c.e.opts.Now(), State: &s})
		}
	}
	c.mu.Unlock()
	if changed != nil {
		c.writeState(*changed)
	}
}

func (c *chatRole) writeState(s State) {
	if c.e.opts.States == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.e.opts.States.SetAgentState(ctx, c.role, s.State, s.Detail); err != nil {
		c.e.opts.Logf("chat: %s: set agent state: %v", c.role, err)
	}
}

func (c *chatRole) emitLocked(ev Event) {
	c.seq++
	ev.Seq, ev.Role = c.seq, c.role
	if ev.Time.IsZero() {
		ev.Time = c.e.opts.Now()
	}
	if ev.SessionID == "" && c.proc != nil {
		ev.SessionID = c.proc.spec.SessionID
	}
	if ev.SessionID != "" {
		c.sessions[ev.SessionID] = true
		if ev.Source != SourceTranscript {
			c.native[ev.SessionID] = true
		}
	}
	if persisted(ev.Kind) {
		if err := c.log.append(ev); err != nil {
			c.e.opts.Logf("chat: %s: append log: %v", c.role, err)
		}
	}
	c.ring = append(c.ring, ev)
	if over := len(c.ring) - c.e.opts.RingSize; over > 0 {
		c.ring = append([]Event(nil), c.ring[over:]...)
	}
	c.tracker.observe(ev)
	c.recordCost(ev)
	for s := range c.subs {
		select {
		case s.ch <- ev:
		default: // too slow: drop it; the client reconnects with ?since
			close(s.ch)
			delete(c.subs, s)
		}
	}
}

// recordCost hands a live turn's cost to the sink. Lines replayed silently
// never get here, so adopting a host does not count a turn twice; the sink
// ignores the rest (a turn re-published after an unacked crash).
func (c *chatRole) recordCost(ev Event) {
	sink := c.e.opts.Costs
	rec, ok := costRecord(ev)
	if sink == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.RecordChatCost(ctx, rec); err != nil {
		c.e.opts.Logf("chat: %s: record cost: %v", c.role, err)
	}
}

// StartOptions tunes Start.
type StartOptions struct {
	Model string // overrides the resolved model
}

// Start launches role's claude process (resuming its session when Claude has a
// transcript for it). It is a no-op when the role is already running. It
// refuses with *OwnedError when anything else owns the session id.
func (e *Engine) Start(ctx context.Context, role string, o StartOptions) (Status, error) {
	st, _, err := e.start(ctx, role, o)
	return st, err
}

// start is Start; spawned reports that it launched a new claude process (not a
// no-op, not an adopted host).
func (e *Engine) start(ctx context.Context, role string, o StartOptions) (st Status, spawned bool, err error) {
	c, err := e.chat(role)
	if err != nil {
		return Status{}, false, err
	}
	c.mu.Lock()
	if c.proc != nil || c.starting {
		c.mu.Unlock()
		st, err = e.Status(role)
		return st, false, err
	}
	c.starting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.starting = false
		c.mu.Unlock()
	}()

	if e.opts.Resolver == nil {
		return Status{}, false, errors.New("chat: no resolver configured")
	}
	spec, err := e.opts.Resolver.Resolve(ctx, role)
	if err != nil {
		return Status{}, false, err
	}
	if o.Model != "" {
		spec.Model = o.Model
	}
	if spec.SessionID == "" {
		return Status{}, false, fmt.Errorf("chat: %s has no session id", role)
	}
	if err := e.claimSession(spec.SessionID, role); err != nil {
		return Status{}, false, err
	}
	// A host that survived a cmdrd restart holds the session: adopt it.
	if hc, hello := e.findHost(role, spec.SessionID); hc != nil {
		e.adopt(c, spec, hc, hello)
		st, err = e.Status(role)
		return st, false, err
	}
	lock, err := e.checkAndLock(ctx, spec.SessionID, role)
	if err != nil {
		e.releaseSession(spec.SessionID)
		return Status{}, false, err
	}
	// A Manager a share drew onto Codex goes back to Claude at a new process
	// only: an adopted host above keeps running as it is, and a host findHost
	// could not reach holds the lock, so it never gets this far. For a
	// Manager the steer below makes that call (only when Claude has usage), so
	// an outage never flips it to Claude and back, losing its Codex thread.
	resolve := !isManagedChatRole(role) && dropDrawnCodex(e.opts.Home, role)
	// Every Manager start lands on a runtime and account with usage left.
	moved := ""
	if isManagedChatRole(role) {
		var changed bool
		if moved, changed, err = e.steerManager(role, e.NeverStarted(role)); err != nil {
			lock.release()
			e.releaseSession(spec.SessionID)
			return Status{}, false, err
		}
		resolve = resolve || changed
	}
	if resolve {
		again, err := e.opts.Resolver.Resolve(ctx, role)
		if err != nil {
			lock.release()
			e.releaseSession(spec.SessionID)
			return Status{}, false, err
		}
		if o.Model != "" {
			again.Model = o.Model
		}
		again.SessionID = spec.SessionID
		spec = again
	}
	if err := e.spawn(ctx, c, spec, lock); err != nil {
		lock.release()
		e.releaseSession(spec.SessionID)
		return Status{}, false, err
	}
	if moved != "" {
		e.startMoved(c, moved)
	}
	st, err = e.Status(role)
	return st, true, err
}

func (e *Engine) claimSession(id, role string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.owners[id]; ok {
		return &OwnedError{SessionID: id, By: "engine", Role: r}
	}
	e.owners[id] = role
	return nil
}

func (e *Engine) releaseSession(id string) {
	e.mu.Lock()
	delete(e.owners, id)
	e.mu.Unlock()
}

// ourPIDs are the claude children of this engine.
func (e *Engine) ourPIDs() map[int]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[int]bool{}
	for _, c := range e.chats {
		c.mu.Lock()
		if c.proc != nil && c.proc.pid > 0 {
			out[c.proc.pid] = true
		}
		c.mu.Unlock()
	}
	return out
}

// checkAndLock takes the session flock and then makes sure no process outside
// this engine (a tmux TUI, an orphan) has the session open.
func (e *Engine) checkAndLock(ctx context.Context, id, role string) (*sessionLock, error) {
	lock, err := acquireSessionLock(e.lockDir(), id, role)
	if err != nil {
		return nil, err
	}
	procs, err := e.opts.Procs(ctx, id)
	if err != nil {
		lock.release()
		return nil, fmt.Errorf("chat: cannot verify session owner: %w", err)
	}
	if owned := foreignOwner(id, procs, e.ourPIDs()); owned != nil {
		lock.release()
		return nil, owned
	}
	return lock, nil
}

func (e *Engine) cliVersion(ctx context.Context) string {
	e.versionOnce.Do(func() {
		vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(vctx, e.opts.Binary, "--version")
		cmd.Env = childEnv()
		out, err := cmd.Output()
		if err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				e.version = f[0]
			}
		}
	})
	return e.version
}

// childEnv is cmdrd's environment for claude: ANTHROPIC_API_KEY removed so a
// chat never bills the API instead of the user's plan, and no auto-updater.
// extra (CLAUDE_CONFIG_DIR for a named account) replaces inherited values.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "DISABLE_AUTOUPDATER=") || overridden(kv, extra) {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, extra...), "DISABLE_AUTOUPDATER=1")
}

func overridden(kv string, extra []string) bool {
	for _, x := range extra {
		if k, _, _ := strings.Cut(x, "="); strings.HasPrefix(kv, k+"=") {
			return true
		}
	}
	return false
}

// syncTranscript publishes the transcript events of sessionID that the log does
// not have yet, so a session a terminal runs shows its history (and its newer
// turns) in the app. A session the engine ran itself is covered by live events:
// its transcript is only read for the cost baseline.
func (c *chatRole) syncTranscript(path, sessionID string) (TranscriptInfo, error) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	events, info, err := ReadTranscript(path, c.role)
	if err != nil {
		return info, err
	}
	c.mu.Lock()
	skip := c.native[sessionID]
	done := c.hydrated[sessionID]
	c.mu.Unlock()
	if skip || done >= len(events) {
		return info, nil
	}
	for i := range events {
		events[i].SessionID = sessionID
	}
	c.publish(events[done:]...)
	c.mu.Lock()
	c.hydrated[sessionID] = len(events)
	c.mu.Unlock()
	return info, nil
}

func (e *Engine) spawn(ctx context.Context, c *chatRole, spec Spec, lock *sessionLock) error {
	transcript := FindTranscript(e.projectsDir(spec), spec.SessionID)
	if spec.Runtime != RuntimeCodex {
		var err error
		if transcript, err = e.carryTranscript(spec); err != nil {
			return err
		}
	}
	resume := transcript != ""

	// Hydrate history the engine has not published yet (e.g. a Manager handed
	// over from the TUI).
	var info TranscriptInfo
	if resume {
		var err error
		info, err = c.syncTranscript(transcript, spec.SessionID)
		if err != nil {
			e.opts.Logf("chat: %s: read transcript: %v", c.role, err)
		}
	}

	c.mu.Lock()
	m := c.meta
	c.mu.Unlock()
	base, known := 0.0, !resume // a fresh session starts at $0
	if m.SessionID == spec.SessionID && m.CostKnown {
		base, known = m.TotalCostUSD, true
	}
	if info.CostKnown && (!known || info.TotalCostUSD > base) {
		base, known = info.TotalCostUSD, true
	}

	version := ""
	if spec.Runtime != RuntimeCodex {
		version = e.cliVersion(ctx)
	}
	hc, hello, err := e.startHost(spec, resume, lock)
	if err != nil {
		return err
	}
	p := e.newProcess(spec, hc, hello, base, known)
	c.mu.Lock()
	c.proc = p
	c.limit, c.turnLimit = nil, nil // a new process (often on another account) starts clean
	c.meta.SessionID, c.meta.Running, c.meta.CLIVersion = spec.SessionID, true, version
	c.meta.TotalCostUSD, c.meta.CostKnown = base, known
	_ = writeMeta(e.logDir(), c.role, c.meta)
	c.mu.Unlock()

	c.publish(Event{Kind: KindSession, Source: SourceEngine, SessionID: spec.SessionID, Session: &SessionInfo{
		Model: spec.Model, Cwd: spec.Cwd, CLIVersion: version, PID: hello.PID, Resumed: resume}})
	if version != "" && version != PinnedCLIVersion {
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "cli_version",
			Text: fmt.Sprintf("Claude Code %s differs from the verified %s; the stream protocol may have drifted.", version, PinnedCLIVersion)})
	}
	go e.hostLoop(c, p, hello)
	return nil
}

func (e *Engine) newProcess(spec Spec, hc *hostConn, hello *hostHello, base float64, known bool) *process {
	return &process{spec: spec, pid: hello.PID, hc: hc, norm: NewNormalizer(base, known, e.opts.Now), startedAt: hello.Info.StartedAt,
		resumed: hello.Info.Resumed, done: make(chan struct{}), pending: map[string]*ControlRequest{}, acks: map[string]chan struct{}{},
		ackKick: make(chan struct{}, 1)}
}

// startHost launches a detached chat host for spec (it takes over the session
// lock) and attaches to it.
func (e *Engine) startHost(spec Spec, resume bool, lock *sessionLock) (*hostConn, *hostHello, error) {
	dir := hostDir(e.opts.Home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("chat: mkdir %s: %w", dir, err)
	}
	sock := hostSock(e.opts.Home, spec.Role)
	binary, args := e.opts.Binary, Args(spec, resume)
	if spec.Runtime == RuntimeCodex {
		// The bridge speaks Claude's stream-json and drives `codex app-server`.
		bridge := e.opts.HostBinary
		if bridge == "" {
			exe, err := os.Executable()
			if err != nil {
				return nil, nil, err
			}
			bridge = exe
		}
		binary, args = bridge, CodexBridgeArgs(spec, e.opts.Home)
	}
	cfg := hostConfig{Sock: sock, Binary: binary, Args: args, Cwd: spec.Cwd,
		Info: hostInfo{Role: spec.Role, SessionID: spec.SessionID, Model: spec.Model, Cwd: spec.Cwd, Resumed: resume, StartedAt: e.opts.Now()}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	cfgPath := filepath.Join(dir, spec.Role+".cfg.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		return nil, nil, err
	}
	bin := e.opts.HostBinary
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			return nil, nil, err
		}
	}
	logf, err := os.OpenFile(filepath.Join(dir, spec.Role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	defer logf.Close()
	cmd := exec.Command(bin, HostArg, cfgPath)
	// COMMANDER_ROLE tells cmdr commands the agent runs who is asking
	// (internal-operator-provider lets a Manager switch only its own Operators).
	cmd.Env = childEnv(append(claudeacct.Env(e.opts.Home, spec.Account), "COMMANDER_ROLE="+spec.Role)...)
	cmd.ExtraFiles = []*os.File{lock.f} // the host (and the claude it starts) hold the session lock
	cmd.Stdout, cmd.Stderr = logf, logf
	// Own session and process group: neither a launchd job kill nor a terminal
	// hangup aimed at cmdrd's group reaches the host or its claude.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("chat: start chat host: %w", err)
	}
	lock.handoff()
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		hc, hello, err := dialHost(sock)
		if err == nil {
			return hc, hello, nil
		}
		select {
		case <-exited:
			return nil, nil, fmt.Errorf("chat: chat host exited before it listened (see %s)", logf.Name())
		default:
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("chat: chat host did not come up: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// findHost attaches to role's running host when it runs sessionID. A host on
// another session (or none) yields nil; a stale one is ended.
func (e *Engine) findHost(role, sessionID string) (*hostConn, *hostHello) {
	sock := hostSock(e.opts.Home, role)
	hc, hello, err := dialHost(sock)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			_ = os.Remove(sock) // a dead host's leftover socket
		}
		return nil, nil
	}
	if hello.Info.SessionID != sessionID {
		e.opts.Logf("chat: ending chat host of %s on another session", role)
		_ = hc.send(hostMsg{Op: "quit"})
		hc.close()
		for i := 0; i < 250; i++ {
			if _, err := os.Stat(sock); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return nil, nil
	}
	return hc, hello
}

// adopt takes over a running host: the claude it holds never notices. History
// the previous Engine already published is replayed silently to rebuild the
// normalizer and pending requests; the rest is published.
func (e *Engine) adopt(c *chatRole, spec Spec, hc *hostConn, hello *hostHello) {
	spec.SessionID = hello.Info.SessionID
	if hello.Info.Model != "" {
		spec.Model = hello.Info.Model
	}
	if hello.Info.Cwd != "" {
		spec.Cwd = hello.Info.Cwd
	}
	c.mu.Lock()
	m := c.meta
	c.mu.Unlock()
	base, known := 0.0, false
	if m.SessionID == spec.SessionID && m.CostKnown {
		base, known = m.TotalCostUSD, true
	}
	p := e.newProcess(spec, hc, hello, base, known)
	c.mu.Lock()
	c.proc = p
	c.tracker.observe(Event{Kind: KindSession})
	c.meta.SessionID, c.meta.Running = spec.SessionID, true
	_ = writeMeta(e.logDir(), c.role, c.meta)
	c.mu.Unlock()
	e.opts.Logf("chat: %s: adopted running claude pid %d", c.role, hello.PID)
	go e.hostLoop(c, p, hello)
}

type controlEnvelope struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Response  struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
	} `json:"response"`
}

// hostLoop reads the host's stream until claude exits or the Engine lets go.
func (e *Engine) hostLoop(c *chatRole, p *process, hello *hostHello) {
	defer close(p.ackKick)
	go e.ackLoop(p)
	answered := map[string]bool{}
	for _, id := range hello.Done {
		answered[id] = true
	}
	silentUntil := hello.Acked
	if hello.First >= silentUntil {
		c.syncState()
	}
	for {
		m, err := p.hc.read()
		if err != nil {
			c.mu.Lock()
			detached := p.detached
			c.mu.Unlock()
			if !detached {
				e.onExit(c, p, &hostExit{Code: -1, Stderr: "chat host disconnected"})
			}
			return
		}
		switch m.T {
		case "out":
			p.lineMu.Lock()
			c.mu.Lock()
			detached := p.detached
			c.mu.Unlock()
			if detached { // Close let go: the next engine takes this line
				p.lineMu.Unlock()
				return
			}
			e.handleLine(c, p, []byte(m.Line), m.Idx < silentUntil, answered)
			p.setAck(m.Idx + 1)
			p.lineMu.Unlock()
			if m.Idx+1 == silentUntil {
				c.syncState()
			}
		case "exit":
			e.onExit(c, p, m.Exit)
			return
		case "kicked": // a newer cmdrd attached: leave the chat to it
			c.mu.Lock()
			p.detached = true
			if c.proc == p {
				c.proc = nil
			}
			c.mu.Unlock()
			e.releaseSession(p.spec.SessionID)
			return
		}
	}
}

// syncState publishes the agent state when replaying history changed it.
func (c *chatRole) syncState() {
	c.mu.Lock()
	st := c.tracker.state()
	if st == c.lastState {
		c.mu.Unlock()
		return
	}
	c.lastState = st
	s := st
	c.emitLocked(Event{Kind: KindState, Source: SourceEngine, Time: c.e.opts.Now(), State: &s})
	c.mu.Unlock()
	c.writeState(st)
}

func (p *process) setAck(idx int64) {
	p.ackMu.Lock()
	p.ackIdx = idx
	p.ackMu.Unlock()
	select {
	case p.ackKick <- struct{}{}:
	default:
	}
}

// flushAck tells the host how far this Engine has published.
func (p *process) flushAck() {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	p.ackMu.Lock()
	idx, sent := p.ackIdx, p.ackSent
	p.ackSent = idx
	p.ackMu.Unlock()
	if idx > sent {
		_ = p.hc.send(hostMsg{Op: "ack", Idx: idx})
	}
}

// ackLoop sends acks at most every 25ms; the host prunes finished turns by them.
func (e *Engine) ackLoop(p *process) {
	for range p.ackKick {
		time.Sleep(25 * time.Millisecond)
		p.flushAck()
	}
}

// handleLine processes one stdout line of claude. silent lines were already
// published by a previous Engine: they only rebuild state.
func (e *Engine) handleLine(c *chatRole, p *process, line []byte, silent bool, answered map[string]bool) {
	var env controlEnvelope
	_ = json.Unmarshal(line, &env)
	switch env.Type {
	case "control_response":
		p.pmu.Lock()
		if ch, ok := p.acks[env.Response.RequestID]; ok {
			close(ch)
			delete(p.acks, env.Response.RequestID)
		}
		p.pmu.Unlock()
		if env.Response.Subtype == "error" && !silent {
			c.publish(Event{Kind: KindError, Source: SourceLive, Error: "control request failed: " + env.Response.Error})
		}
		return
	case "control_cancel_request":
		p.pmu.Lock()
		_, ok := p.pending[env.RequestID]
		delete(p.pending, env.RequestID)
		p.pmu.Unlock()
		if ok && !silent {
			c.publish(Event{Kind: KindResolved, Source: SourceLive, Request: &Request{ID: env.RequestID, Decision: "cancelled"}})
		}
		return
	}
	events, cr := p.norm.Line(line)
	if cr != nil && !answered[cr.RequestID] {
		if cr.Subtype == "can_use_tool" {
			p.pmu.Lock()
			p.pending[cr.RequestID] = cr
			p.pmu.Unlock()
		} else if !silent {
			_ = p.write(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "error", "request_id": cr.RequestID, "error": "unsupported by commander: " + cr.Subtype}})
		}
	}
	for i := range events {
		if events[i].Kind == KindSession && events[i].Session != nil {
			c.mu.Lock()
			events[i].Session.PID = p.pid
			events[i].Session.Resumed = p.resumed
			p.info = *events[i].Session
			c.mu.Unlock()
		}
	}
	if silent {
		c.mu.Lock()
		for _, ev := range events {
			c.tracker.observe(ev)
			if ev.Request != nil && answered[ev.Request.ID] {
				c.tracker.resolve(ev.Request.ID)
			}
		}
		c.mu.Unlock()
		return
	}
	c.publish(events...)
	for _, ev := range events {
		if ev.Kind == KindCompaction {
			boardturn.MarkFull(e.opts.Home, c.role)
		}
		if ev.Kind == KindRateLimit {
			e.opts.Usage.Record(p.runtime(), p.spec.Account, c.role, ev.RateLimit, e.opts.Now())
		}
		c.observeLimit(ev)
		if ev.Kind == KindResult {
			e.afterResult(c, p)
			e.settleLimit(c, p, ev)
		}
	}
}

// afterResult cancels requests the turn left unanswered and saves the cost.
func (e *Engine) afterResult(c *chatRole, p *process) {
	p.pmu.Lock()
	var ids []string
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pending = map[string]*ControlRequest{}
	p.pmu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		c.publish(Event{Kind: KindResolved, Source: SourceEngine, Request: &Request{ID: id, Decision: "cancelled"}})
	}
	c.mu.Lock()
	c.meta.TotalCostUSD, c.meta.CostKnown = p.norm.LastTotal(), true
	_ = writeMeta(e.logDir(), c.role, c.meta)
	c.mu.Unlock()
}

func (e *Engine) onExit(c *chatRole, p *process, hx *hostExit) {
	if hx == nil {
		hx = &hostExit{Code: -1}
	}
	ex := &Exit{Code: hx.Code, Signal: hx.Signal}
	c.mu.Lock()
	ex.Expected = p.stopping
	midTurn := c.tracker.turn
	c.mu.Unlock()
	if !ex.Expected && (ex.Code != 0 || ex.Signal != "") {
		if tail := strings.TrimSpace(hx.Stderr); tail != "" {
			c.publish(Event{Kind: KindError, Source: SourceEngine, Error: tail})
		}
	}
	// Free the role before announcing the exit, so a client reacting to
	// process_exit (e.g. restarting) sees it stopped. The host drops the session
	// lock as soon as claude is gone.
	e.releaseSession(p.spec.SessionID)
	c.mu.Lock()
	if c.proc == p {
		c.proc = nil
	}
	if !ex.Expected {
		c.meta.Running = false
	}
	_ = writeMeta(e.logDir(), c.role, c.meta)
	c.mu.Unlock()
	c.publish(Event{Kind: KindExit, Source: SourceEngine, SessionID: p.spec.SessionID, Exit: ex})
	close(p.done)
	_ = p.hc.send(hostMsg{Op: "bye"})
	p.hc.close()
	if !ex.Expected && e.opts.CrashRestart != nil && e.opts.CrashRestart(c.role) {
		go e.restartCrashed(c, midTurn)
	}
}

// Crash restarts are capped so a role that dies on every start stops looping.
const (
	maxCrashRestarts = 3
	crashWindow      = 10 * time.Minute
)

// CrashResumeMessage is sent to a role restarted after dying mid-turn.
const CrashResumeMessage = "[resume] your session crashed and was restarted; continue where you left off"

// restartCrashed starts c's role again on its session after it died on its
// own, telling it to carry on when the crash cut a turn short.
func (e *Engine) restartCrashed(c *chatRole, midTurn bool) {
	now := e.opts.Now()
	e.mu.Lock()
	var recent []time.Time
	for _, t := range e.crashes[c.role] {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxCrashRestarts {
		e.crashes[c.role] = recent
		e.mu.Unlock()
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "crash_restart",
			Text: fmt.Sprintf("Not restarting: the session crashed %d times in %s. Start it again when the cause is fixed.", len(recent)+1, crashWindow)})
		return
	}
	e.crashes[c.role] = append(recent, now)
	e.mu.Unlock()
	time.Sleep(e.opts.CrashRestartDelay)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, spawned, err := e.start(ctx, c.role, StartOptions{})
	if err != nil {
		e.opts.Logf("chat: restart %s after crash: %v", c.role, err)
		c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "crash_restart", Text: "Could not restart the session: " + err.Error()})
		return
	}
	if !spawned {
		return // something else started it first
	}
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "crash_restart", Text: "Restarted the session after the crash"})
	if midTurn {
		if err := e.send(c.role, CrashResumeMessage, "mail"); err != nil {
			e.opts.Logf("chat: crash resume message to %s: %v", c.role, err)
		}
	}
}

// Usage is the engine's latest plan usage per account.
func (e *Engine) Usage() *UsageStore { return e.opts.Usage }

// runtime is the agent CLI p runs.
func (p *process) runtime() string {
	if p.spec.Runtime == RuntimeCodex {
		return RuntimeCodex
	}
	return RuntimeClaude
}

func (p *process) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.hc.send(hostMsg{Op: "in", Line: string(raw)})
}

func (p *process) closeStdin() { _ = p.hc.send(hostMsg{Op: "close_stdin"}) }

func (p *process) signal(sig syscall.Signal) error {
	return p.hc.send(hostMsg{Op: "signal", Sig: int(sig)})
}

func (e *Engine) live(role string) (*chatRole, *process, error) {
	c, err := e.chat(role)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil || c.proc.stopping {
		return c, nil, ErrNotRunning
	}
	return c, c.proc, nil
}

// Send writes a user message to role's process.
func (e *Engine) Send(ctx context.Context, role, text string) error {
	return e.send(role, text, "user")
}

// DeliverMail writes a mail notice as a user message, only while role is idle
// (no turn, nothing pending): mail arriving mid-turn is delivered by the Stop
// mail hook instead. ErrBusy means "try again later".
func (e *Engine) DeliverMail(ctx context.Context, role, text string) error {
	c, _, err := e.live(role)
	if err != nil {
		return err
	}
	c.mu.Lock()
	busy := c.tracker.turn || len(c.tracker.order) > 0
	c.mu.Unlock()
	if busy {
		return ErrBusy
	}
	return e.send(role, text, "mail")
}

// Running reports whether role has a live process.
func (e *Engine) Running(role string) bool {
	_, _, err := e.live(role)
	return err == nil
}

// WaitRunning reports whether role has a live process, giving a Start in
// flight (a restart's adoption, a respawn) up to wait to finish first, so a
// message sent in that window is not mistaken for "not running".
func (e *Engine) WaitRunning(ctx context.Context, role string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if e.Running(role) {
			return true
		}
		c, err := e.chat(role)
		if err != nil {
			return false
		}
		c.mu.Lock()
		starting := c.starting
		c.mu.Unlock()
		if !starting || time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (e *Engine) send(role, text, origin string, files ...Attachment) error {
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return errors.New("chat: empty message")
	}
	c, p, err := e.live(role)
	if err != nil {
		return err
	}
	if origin == "user" {
		_ = e.MarkTalking(role) // hold its mail while the user talks to it
	}
	ev := Event{Kind: KindUser, Source: SourceEngine, Text: text, Origin: origin}
	for _, f := range files {
		ev.Attachments = append(ev.Attachments, EventAttachment{Name: f.Name, Mime: f.Mime, Path: f.Path})
	}
	c.publish(ev)
	content := userContent(text, files)
	if note := e.takeClaudeHandoff(p); note != "" {
		content = withLeadingText(note, content)
	}
	return p.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content},
		"parent_tool_use_id": nil, "session_id": p.spec.SessionID})
}

// takeClaudeHandoff is the note a Claude process back from Codex gets on its
// first message (once; the bridge does the same for Codex).
func (e *Engine) takeClaudeHandoff(p *process) string {
	if p.spec.Runtime == RuntimeCodex || p.spec.Handoff == "" || !p.handoff.CompareAndSwap(false, true) {
		return ""
	}
	_ = os.Remove(HandoffFile(e.opts.Home, p.spec.Role))
	return HandoffNote(p.spec.Handoff)
}

// HandoffNote asks an agent to read the conversation it takes over.
func HandoffNote(log string) string {
	return "Before answering, read your predecessor's conversation at " + log +
		" (JSONL, newest at the end): pick up what was in flight and any promises made, and say in one line what you picked up."
}

func withLeadingText(note string, content any) any {
	lead := map[string]any{"type": "text", "text": note}
	if blocks, ok := content.([]map[string]any); ok {
		return append([]map[string]any{lead}, blocks...)
	}
	return []map[string]any{lead, {"type": "text", "text": content.(string)}}
}

// Interrupt stops the current turn with a control request; when the CLI does
// not acknowledge it within InterruptGrace the process gets SIGINT (which ends
// the turn and the process; Start resumes it).
func (e *Engine) Interrupt(ctx context.Context, role string) error {
	c, p, err := e.live(role)
	if err != nil {
		return err
	}
	ack := e.sendInterrupt(p)
	c.publish(Event{Kind: KindStatus, Source: SourceEngine, Status: "interrupting"})
	select {
	case <-ack:
		return nil
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(e.opts.InterruptGrace):
	}
	c.publish(Event{Kind: KindNotice, Source: SourceEngine, Subtype: "interrupt_fallback", Text: "no interrupt ack; sending SIGINT"})
	return p.signal(syscall.SIGINT)
}

func (e *Engine) sendInterrupt(p *process) chan struct{} {
	p.pmu.Lock()
	p.reqN++
	id := fmt.Sprintf("cmdr-interrupt-%d", p.reqN)
	ack := make(chan struct{})
	p.acks[id] = ack
	p.pmu.Unlock()
	if err := p.write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}}); err != nil {
		p.pmu.Lock()
		delete(p.acks, id)
		p.pmu.Unlock()
		close(ack)
	}
	return ack
}

// Decision answers a permission request (or a plan).
type Decision struct {
	// Behavior is allow, allow_session or deny.
	Behavior string `json:"decision"`
	// UpdatedInput replaces the tool input (default: the requested input).
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
	// UpdatedPermissions are permission updates to apply (Claude's
	// PermissionUpdate objects). For allow_session the default is the request's
	// suggestions with their destination set to "session".
	UpdatedPermissions json.RawMessage `json:"updated_permissions,omitempty"`
	Message            string          `json:"message,omitempty"`   // deny reason shown to Claude
	Interrupt          bool            `json:"interrupt,omitempty"` // deny and stop the turn
}

func (e *Engine) takePending(role, id string) (*chatRole, *process, *ControlRequest, error) {
	c, p, err := e.live(role)
	if err != nil {
		return nil, nil, nil, err
	}
	p.pmu.Lock()
	defer p.pmu.Unlock()
	cr, ok := p.pending[id]
	if !ok {
		return nil, nil, nil, ErrNoRequest
	}
	delete(p.pending, id)
	return c, p, cr, nil
}

// Approve answers a permission_request or plan.
func (e *Engine) Approve(ctx context.Context, role, id string, d Decision) error {
	switch d.Behavior {
	case "allow", "allow_session", "deny":
	default:
		return fmt.Errorf("chat: decision must be allow, allow_session or deny")
	}
	c, p, cr, err := e.takePending(role, id)
	if err != nil {
		return err
	}
	var resp map[string]any
	if d.Behavior == "deny" {
		msg := d.Message
		if msg == "" {
			msg = "The user denied this tool call."
		}
		resp = map[string]any{"behavior": "deny", "message": msg}
		if d.Interrupt {
			resp["interrupt"] = true
		}
	} else {
		input := d.UpdatedInput
		if len(input) == 0 {
			input = cr.Input
		}
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
		perms := d.UpdatedPermissions
		if len(perms) == 0 && d.Behavior == "allow_session" {
			perms = sessionSuggestions(cr.Suggestions)
		}
		if len(perms) > 0 {
			resp["updatedPermissions"] = perms
		}
	}
	if err := p.write(controlSuccess(id, resp)); err != nil {
		return err
	}
	c.publish(Event{Kind: KindResolved, Source: SourceEngine, Request: &Request{ID: id, ToolName: cr.ToolName, Decision: d.Behavior, Message: d.Message}})
	return nil
}

// Answer answers an AskUserQuestion request: answers maps each question's text
// to the chosen label(s) (comma-separated for multi-select) or free text.
func (e *Engine) Answer(ctx context.Context, role, id string, answers map[string]string) error {
	c, p, cr, err := e.takePending(role, id)
	if err != nil {
		return err
	}
	if cr.ToolName != "AskUserQuestion" {
		p.pmu.Lock()
		p.pending[id] = cr // not a question: put it back
		p.pmu.Unlock()
		return fmt.Errorf("chat: request %s is %s, not a question (use approve)", id, cr.ToolName)
	}
	input := map[string]any{}
	_ = json.Unmarshal(cr.Input, &input)
	input["answers"] = answers
	if err := p.write(controlSuccess(id, map[string]any{"behavior": "allow", "updatedInput": input})); err != nil {
		return err
	}
	c.publish(Event{Kind: KindResolved, Source: SourceEngine, Request: &Request{ID: id, ToolName: cr.ToolName, Decision: "answered"}})
	return nil
}

func controlSuccess(id string, resp map[string]any) map[string]any {
	return map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}}
}

// sessionSuggestions rewrites permission suggestions to apply to this session
// only ("Allow for this session" instead of writing settings files).
func sessionSuggestions(raw json.RawMessage) json.RawMessage {
	var sugg []map[string]any
	if json.Unmarshal(raw, &sugg) != nil || len(sugg) == 0 {
		return nil
	}
	for _, s := range sugg {
		s["destination"] = "session"
	}
	out, _ := json.Marshal(sugg)
	return out
}

// Stop ends role's process and keeps it stopped (cmdrd will not resume it).
func (e *Engine) Stop(ctx context.Context, role string) error {
	c, p, err := e.live(role)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.meta.Running = false
	_ = writeMeta(e.logDir(), c.role, c.meta)
	c.mu.Unlock()
	return e.stopProcess(ctx, c, p)
}

func (e *Engine) stopProcess(ctx context.Context, c *chatRole, p *process) error {
	c.mu.Lock()
	p.stopping = true
	busy := c.tracker.turn
	c.mu.Unlock()
	e.denyPending(p, "the chat was stopped")
	if busy {
		e.sendInterrupt(p)
	}
	p.closeStdin() // claude -p exits once stdin ends and the turn is over
	for _, sig := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
		if sig != 0 {
			_ = p.signal(sig)
		}
		select {
		case <-p.done:
			return e.waitGone(ctx, p.pid)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.opts.StopGrace):
		}
	}
	return fmt.Errorf("chat: %s did not exit", c.role)
}

// waitGone waits for the claude process itself to exit. done can fire first
// (an adopted host reports the end before its claude child is gone), and a
// start in that gap finds the session held by the old pid and refuses it.
func (e *Engine) waitGone(ctx context.Context, pid int) error {
	if pid <= 1 {
		return nil
	}
	killed := false
	deadline := time.Now().Add(e.opts.StopGrace)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			if killed {
				return fmt.Errorf("chat: claude pid %d did not exit", pid)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
			killed, deadline = true, time.Now().Add(e.opts.StopGrace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

func (e *Engine) denyPending(p *process, why string) {
	p.pmu.Lock()
	ids := make([]string, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pmu.Unlock()
	for _, id := range ids {
		_ = p.write(controlSuccess(id, map[string]any{"behavior": "deny", "message": why}))
	}
}

// Close lets go of every chat host without touching its claude: the claude
// processes keep running (and finishing their turns) under their detached
// hosts, roles stay marked running, and the next cmdrd's ResumeRunning adopts
// them. The final acks are flushed so the next Engine neither repeats nor
// misses an event.
func (e *Engine) Close() {
	e.mu.Lock()
	chats := make([]*chatRole, 0, len(e.chats))
	for _, c := range e.chats {
		chats = append(chats, c)
	}
	e.mu.Unlock()
	for _, c := range chats {
		c.mu.Lock()
		p := c.proc
		if p != nil {
			p.detached = true
		}
		c.mu.Unlock()
		if p != nil {
			p.lineMu.Lock() // let the line being published finish and be acked
			p.flushAck()
			p.lineMu.Unlock()
			p.hc.close()
		}
	}
}

// ResumeRunning brings back every role that was running when the previous
// cmdrd stopped: it adopts the role's surviving chat host, or starts a new
// process. A session still held by a pre-host orphan finishing its turn is
// retried for up to two minutes. It returns at once: the work runs in the
// background and WaitResumed reports when each role's first attempt is done.
func (e *Engine) ResumeRunning(ctx context.Context) {
	done := make(chan struct{})
	e.mu.Lock()
	e.resumed = done
	e.mu.Unlock()
	go func() {
		defer close(done)
		e.endOrphanHosts()
		var first sync.WaitGroup
		for _, role := range e.knownRoles() {
			if readMeta(e.logDir(), role).Running {
				first.Add(1)
				go e.resumeRole(ctx, role, first.Done)
			}
		}
		first.Wait()
	}()
}

// resumeRole brings role back, calling firstDone once its first attempt ends.
func (e *Engine) resumeRole(ctx context.Context, role string, firstDone func()) {
	var once sync.Once
	defer once.Do(firstDone)
	started := e.opts.Now()
	deadline := started.Add(2 * time.Minute)
	orphanKilled := false
	cut := e.turnCutShort(role)
	for {
		_, spawned, err := e.start(ctx, role, StartOptions{})
		once.Do(firstDone)
		if err == nil && spawned && cut {
			// The restart killed claude mid-turn: nothing would start
			// another turn, so tell the role to carry on. Once per
			// interruption: the loop returns right after.
			if err := e.send(role, ResumeMessage, "mail"); err != nil {
				e.opts.Logf("chat: resume message to %s: %v", role, err)
			}
		}
		var owned *OwnedError
		if err != nil && errors.As(err, &owned) && owned.By == "headless" && owned.PID > 1 {
			// A headless claude on this session that we do not own is an
			// orphan of a previous cmdrd (its stdin pipe is gone): after a
			// grace period for a turn to finish, end it so the chat can
			// resume instead of staying detached from the app.
			if !orphanKilled && e.opts.Now().Sub(started) >= e.opts.OrphanGrace {
				orphanKilled = true
				e.opts.Logf("chat: ending orphan claude pid %d on %s's session", owned.PID, role)
				_ = syscall.Kill(owned.PID, syscall.SIGTERM)
			}
		}
		if err == nil || !errors.As(err, &owned) || e.opts.Now().After(deadline) {
			if err != nil {
				e.opts.Logf("chat: resume %s: %v", role, err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.opts.ResumeRetry):
		}
	}
}

// Heartbeat rewrites the agent state of every live role each interval, so a
// long approval wait or tool run never looks stale to livedata.
func (e *Engine) Heartbeat(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e.mu.Lock()
		chats := make([]*chatRole, 0, len(e.chats))
		for _, c := range e.chats {
			chats = append(chats, c)
		}
		e.mu.Unlock()
		for _, c := range chats {
			c.mu.Lock()
			live, st := c.proc != nil, c.lastState
			c.mu.Unlock()
			if live {
				c.writeState(st)
			}
		}
	}
}

// ResumeMessage is sent to a role whose turn a restart cut short.
const ResumeMessage = "[resume] your turn was interrupted by a restart; continue where you left off"

// turnCutShort reports whether role's event log ends mid-turn: a user turn or
// work with no turn_result after it and no recorded exit.
func (e *Engine) turnCutShort(role string) bool {
	events, err := readLog(filepath.Join(e.logDir(), role+".jsonl"), 0, 0)
	if err != nil {
		return false
	}
	t := newStateTracker()
	for _, ev := range events {
		t.observe(ev)
	}
	return t.turn
}

// NeverStarted reports that role has no chat history: nobody has run it
// natively, so there is no Stop or crash to respect when starting it.
func (e *Engine) NeverStarted(role string) bool {
	info, err := os.Stat(filepath.Join(e.logDir(), role+".jsonl"))
	return err != nil || info.Size() == 0
}

func (e *Engine) knownRoles() []string {
	seen := map[string]bool{}
	entries, _ := os.ReadDir(e.logDir())
	for _, ent := range entries {
		if role, ok := strings.CutSuffix(ent.Name(), ".jsonl"); ok && ValidRole(role) {
			seen[role] = true
		}
	}
	e.mu.Lock()
	for role := range e.chats {
		seen[role] = true
	}
	e.mu.Unlock()
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Status is a role's chat state.
type Status struct {
	Role         string     `json:"role"`
	Running      bool       `json:"running"`
	SessionID    string     `json:"session_id,omitempty"`
	PID          int        `json:"pid,omitempty"`
	Resumed      bool       `json:"resumed,omitempty"`
	State        string     `json:"state"`
	Detail       string     `json:"detail,omitempty"`
	Cwd          string     `json:"cwd,omitempty"`
	Model        string     `json:"model,omitempty"`
	CLIVersion   string     `json:"cli_version,omitempty"`
	Capabilities []string   `json:"capabilities,omitempty"`
	LastSeq      uint64     `json:"last_seq"`
	TotalCostUSD float64    `json:"total_cost_usd"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	Pending      []Request  `json:"pending"`
	// OwnedBy is "terminal" (or "headless") when the role is not running here but
	// another process (e.g. its tmux TUI) holds its session: starting it would fork
	// the history, so UIs must offer "Move to app", never "Start".
	OwnedBy  string `json:"owned_by,omitempty"`
	OwnerPID int    `json:"owner_pid,omitempty"`
	// OwnerUnknown: no owner could be looked for (the role did not resolve, the process
	// scan failed), so an empty OwnedBy does not mean nobody holds the session. A
	// Manager with no registration or session file is not unknown: nothing to hold.
	OwnerUnknown bool `json:"owner_unknown,omitempty"`
	// Runtime is the agent CLI: the running process's, else the role's choice.
	Runtime string `json:"runtime"`
	// Account is the Claude account: the running process's, else the role's choice.
	Account string `json:"account"`
	// AutoSwitch: the role moves to another logged-in account by itself at a usage limit.
	AutoSwitch bool `json:"auto_switch,omitempty"`
	// UsageLimit is set while the role's last turn stopped at a usage limit.
	UsageLimit *UsageLimit `json:"usage_limit,omitempty"`
}

// Status returns role's chat state.
func (e *Engine) Status(role string) (Status, error) {
	c, err := e.chat(role)
	if err != nil {
		return Status{}, err
	}
	s := e.statusLocked(c, role)
	if !s.Running {
		// Outside c.mu: this resolves the role and scans processes.
		owner := e.foreignOwner(role)
		s.OwnedBy, s.OwnerPID, s.OwnerUnknown = owner.by, owner.pid, owner.unknown
		if s.OwnedBy != "" {
			e.syncForeign(c, owner.session)
			s.LastSeq = e.lastSeq(c)
		}
	}
	return s, nil
}

func (e *Engine) statusLocked(c *chatRole, role string) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Role: role, SessionID: c.meta.SessionID, State: c.lastState.State, Detail: c.lastState.Detail,
		LastSeq: c.seq, TotalCostUSD: c.meta.TotalCostUSD, CLIVersion: c.meta.CLIVersion, Pending: []Request{}, Runtime: ReadRuntime(e.opts.Home, role),
		Account: ReadAccount(e.opts.Home, role), AutoSwitch: AutoSwitch(e.opts.Home, role), UsageLimit: c.limit}
	if p := c.proc; p != nil {
		s.Runtime, s.Account = RuntimeClaude, accountName(p.spec.Account)
		if p.spec.Runtime == RuntimeCodex {
			s.Runtime, s.CLIVersion = RuntimeCodex, p.info.CLIVersion
		}
		started := p.startedAt
		s.Running, s.SessionID, s.PID, s.Resumed = true, p.spec.SessionID, p.pid, p.resumed
		s.Cwd, s.Model, s.StartedAt, s.TotalCostUSD = p.spec.Cwd, p.spec.Model, &started, p.norm.LastTotal()
		s.Capabilities = p.info.Capabilities
		if p.info.Model != "" {
			s.Model = p.info.Model
		}
		p.pmu.Lock()
		for id, cr := range p.pending {
			s.Pending = append(s.Pending, Request{ID: id, ToolName: cr.ToolName, Input: cr.Input, Suggestions: cr.Suggestions})
		}
		p.pmu.Unlock()
		sort.Slice(s.Pending, func(i, j int) bool { return s.Pending[i].ID < s.Pending[j].ID })
	}
	return s
}

// foreignOwner reports whether a process we did not start holds role's session
// ("terminal" for an interactive claude, else "headless"), cached for a few
// seconds because the app polls.
func (e *Engine) foreignOwner(role string) ownerHit {
	if e.opts.Resolver == nil || e.opts.Procs == nil {
		return ownerHit{unknown: true}
	}
	e.mu.Lock()
	if hit, ok := e.ownerCache[role]; ok && time.Since(hit.at) < 5*time.Second {
		e.mu.Unlock()
		return hit
	}
	e.mu.Unlock()
	var hit ownerHit
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	spec, err := e.opts.Resolver.Resolve(ctx, role)
	switch {
	case errors.Is(err, errNoSession):
	case err != nil:
		hit.unknown = true
	case spec.SessionID != "":
		procs, err := e.opts.Procs(ctx, spec.SessionID)
		if err != nil {
			hit.unknown = true
			break
		}
		ours := e.ourPIDs()
		for _, p := range procs {
			if ours[p.PID] {
				continue
			}
			hit.by, hit.pid, hit.session = "terminal", p.PID, spec.SessionID
			if strings.Contains(p.Command, " -p ") || strings.Contains(p.Command, "--print") {
				hit.by = "headless"
			}
			break
		}
	}
	e.mu.Lock()
	if e.ownerCache == nil {
		e.ownerCache = map[string]ownerHit{}
	}
	hit.at = time.Now()
	e.ownerCache[role] = hit
	e.mu.Unlock()
	return hit
}

// syncForeign publishes the transcript of a session another process runs, when
// the file changed since the last look (the app polls Status every few seconds).
func (e *Engine) syncForeign(c *chatRole, sessionID string) {
	path := FindTranscript(e.opts.ProjectsDir, sessionID)
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	c.mu.Lock()
	same := fi.Size() == c.syncSize
	c.syncSize = fi.Size()
	c.mu.Unlock()
	if same {
		return
	}
	if _, err := c.syncTranscript(path, sessionID); err != nil {
		e.opts.Logf("chat: %s: read transcript: %v", c.role, err)
	}
}

func (e *Engine) lastSeq(c *chatRole) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

type ownerHit struct {
	by      string
	pid     int
	session string
	unknown bool
	at      time.Time
}

// List returns the status of every role with a log or a process.
func (e *Engine) List() []Status {
	// In parallel: each idle role's foreignOwner can take its full 3s timeout,
	// and one at a time that made the app's 3s poll take over 30s.
	roles := e.knownRoles()
	got := make([]*Status, len(roles))
	var wg sync.WaitGroup
	for i, role := range roles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s, err := e.Status(role); err == nil {
				got[i] = &s
			}
		}()
	}
	wg.Wait()
	var out []Status
	for _, s := range got {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// Subscription is a live event feed for one role. C is closed when the
// subscriber falls too far behind (reconnect with since = last seq seen) or
// on Close.
type Subscription struct {
	C  <-chan Event
	ch chan Event
	c  *chatRole
}

// Close ends the subscription.
func (s *Subscription) Close() {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if _, ok := s.c.subs[s]; ok {
		delete(s.c.subs, s)
		close(s.ch)
	}
}

// Subscribe returns role's events with seq > since (from the log, then the
// in-memory ring, which also has the live-only deltas) and a feed of the
// events that follow, with no gap or overlap between the two.
func (e *Engine) Subscribe(role string, since uint64) ([]Event, *Subscription, error) {
	c, err := e.chat(role)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ringFirst := uint64(0)
	if len(c.ring) > 0 {
		ringFirst = c.ring[0].Seq
	}
	var replay []Event
	if ringFirst == 0 || since+1 < ringFirst {
		disk, err := readLog(c.log.path, since, ringFirst)
		if err != nil {
			return nil, nil, err
		}
		replay = disk
	}
	for _, ev := range c.ring {
		if ev.Seq > since {
			replay = append(replay, ev)
		}
	}
	ch := make(chan Event, 4096)
	s := &Subscription{C: ch, ch: ch, c: c}
	c.subs[s] = struct{}{}
	return replay, s, nil
}

// tailBuffer keeps the last 8 KiB written to it (claude's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - 8<<10; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// endOrphanHosts ends chat hosts nobody wants: roles not marked running (a Stop
// or crash that never reached the host) and invalid names. Leftover sockets of
// dead hosts are removed.
func (e *Engine) endOrphanHosts() {
	roles := map[string]bool{}
	for _, role := range e.knownRoles() {
		roles[role] = true
	}
	entries, _ := os.ReadDir(hostDir(e.opts.Home))
	for _, ent := range entries {
		if role, ok := strings.CutSuffix(ent.Name(), ".sock"); ok {
			roles[role] = true
		}
	}
	for role := range roles {
		if ValidRole(role) && readMeta(e.logDir(), role).Running {
			continue
		}
		sock := hostSock(e.opts.Home, role)
		hc, _, err := dialHost(sock)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				_ = os.Remove(sock)
			}
			continue
		}
		e.opts.Logf("chat: ending orphan chat host of %s", role)
		_ = hc.send(hostMsg{Op: "quit"})
		hc.close()
	}
}

// Live returns the state of every role with a running process here, by role.
// Cheap (no process scan): cmdrd reads it on every snapshot.
func (e *Engine) Live() map[string]State {
	e.mu.Lock()
	chats := make([]*chatRole, 0, len(e.chats))
	for _, c := range e.chats {
		chats = append(chats, c)
	}
	e.mu.Unlock()
	out := map[string]State{}
	for _, c := range chats {
		c.mu.Lock()
		if c.proc != nil {
			out[c.role] = c.lastState
		}
		c.mu.Unlock()
	}
	return out
}
