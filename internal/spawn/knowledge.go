package spawn

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
)

// taskIDHexRE matches the canonical bd task-bead id form, e.g. "cmdr-4fc",
// "cmdr-deadbeef". The flat-task slug is the first 8 chars of the trailing
// hex segment after the last "-"; for non-conforming ids we fall back to a
// sanitized whole-id slug so spawn never fails on naming alone.
var taskIDHexRE = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// maxFlat bounds a non-hex flatTask; longer ids are cut and given a hash suffix.
const maxFlat = 40

// flatTask derives a filesystem/agent-name-safe short slug from a bead ID.
//
//   - "cmdr-4fc"        -> "4fc"
//   - "cmdr-1a2b3c4d5e" -> "1a2b3c4d"
//   - "weird id!"       -> "weird-id" (non-hex fallback, sanitized)
//   - "example-automation-t09.4.2" -> "example-automation-t09-4-2"
//
// A hex tail is clipped to 8 characters. Any other id is kept whole (it names
// the agent, its session file, output file and tmux session, so two beads must
// never share it: clipping to 8 made every bead of "example-automation-…" into
// "example-"); past 40 characters it is cut and a hash of the id appended.
func flatTask(taskBeadID string) string {
	id := strings.TrimSpace(taskBeadID)
	if id == "" {
		return "task"
	}
	// Prefer the trailing hex segment (matches bd's `<scope>-<hex>` shape).
	if i := strings.LastIndex(id, "-"); i >= 0 && i < len(id)-1 {
		tail := id[i+1:]
		if taskIDHexRE.MatchString(tail) {
			if len(tail) > 8 {
				return tail[:8]
			}
			return tail
		}
	}
	// Whole-id is hex (no dash, e.g. "deadbeef").
	if taskIDHexRE.MatchString(id) {
		if len(id) > 8 {
			return id[:8]
		}
		return id
	}
	// Fallback: the whole id, anything not [a-zA-Z0-9_-] as '-' ('.' too: tmux
	// rewrites it in session names).
	var b strings.Builder
	for _, r := range id {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	if len(out) > maxFlat {
		sum := sha256.Sum256([]byte(id))
		return strings.TrimRight(out[:maxFlat-9], "-") + "-" + hex.EncodeToString(sum[:4])
	}
	return out
}

// knowledgeSessionName composes the tmux session name. Format mirrors
// share/contracts.md: cmdr-<project>-<flat-task>.
func knowledgeSessionName(project, flat string) string {
	return "cmdr-" + project + "-" + flat
}

// validateKnowledgeRequest checks the SpawnRequest fields for knowledge-type.
// Knowledge spawn does not require a Branch (operator commits on whatever
// HEAD is) but otherwise mirrors validateRequest.
func validateKnowledgeRequest(req SpawnRequest) error {
	if req.ProjectType != "knowledge" {
		return fmt.Errorf("spawn: ProjectType %q: expected \"knowledge\"", req.ProjectType)
	}
	if req.ProjectName == "" {
		return errors.New("spawn: ProjectName required")
	}
	if req.TaskBeadID == "" {
		return errors.New("spawn: TaskBeadID required")
	}
	if req.Model == "" {
		return errors.New("spawn: Model required")
	}
	if !filepath.IsAbs(req.ProjectPath) {
		return fmt.Errorf("spawn: ProjectPath %q must be absolute", req.ProjectPath)
	}
	info, err := os.Stat(req.ProjectPath)
	if err != nil {
		return fmt.Errorf("spawn: ProjectPath %q: %w", req.ProjectPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("spawn: ProjectPath %q is not a directory", req.ProjectPath)
	}
	if err := validateMountReads(req.MountReads); err != nil {
		return err
	}
	return nil
}

// spawnKnowledge runs the knowledge-type operator spawn flow. It mirrors
// spawnCode (flock, am register, bd state, agent_runs row, capture-then-resume
// session ID) but has no worktree and no Docker container — opencode runs in
// a detached tmux pane on the host, with stdout pipe-paned to a fifo so the
// U10 watchdog consumes nd-JSON the same way it does for code-type containers.
func (s *Spawner) spawnKnowledge(ctx context.Context, req SpawnRequest) (*SpawnResult, error) {
	if err := validateKnowledgeRequest(req); err != nil {
		return nil, err
	}

	flat := flatTask(req.TaskBeadID)
	agentName := "operator-" + flat
	commanderDir := filepath.Join(req.ProjectPath, ".commander")
	operatorsDir := filepath.Join(commanderDir, "operators")
	kind := agentrt.For(agentrt.TierOperator)
	sessionFile := filepath.Join(operatorsDir, agentrt.SessionFileName(flat, kind))
	fifoPath := filepath.Join(operatorsDir, flat+".fifo")
	sessionName := knowledgeSessionName(req.ProjectName, flat)

	var cleanups cleanups
	bail := func(err error) (*SpawnResult, error) {
		_ = cleanups.runReverse(ctx)
		return nil, err
	}

	// Step a: flock.
	lock, err := acquireSpawnLock(req.ProjectName, flat)
	if err != nil {
		return nil, fmt.Errorf("spawn: acquire lock: %w", err)
	}
	cleanups.push(func() error { return lock.release() })

	// Ensure operators dir exists for the fifo + session-id file.
	if err := os.MkdirAll(operatorsDir, 0o755); err != nil {
		return bail(fmt.Errorf("spawn: create operators dir: %w", err))
	}
	if err := promptassets.EnsureAgentFor(kind, req.promptRole(promptassets.RoleOperatorKnowledge), agentName); err != nil {
		return bail(fmt.Errorf("spawn: ensure operator agent prompt: %w", err))
	}

	// Step b: mkfifo (knowledge-type's nd-JSON event source). If a stale fifo
	// from a prior crashed spawn is in the way, remove it first so mkfifo
	// succeeds idempotently.
	if _, err := os.Stat(fifoPath); err == nil {
		_ = os.Remove(fifoPath)
	}
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		return bail(fmt.Errorf("spawn: mkfifo %s: %w", fifoPath, err))
	}
	cleanups.push(func() error {
		return os.Remove(fifoPath)
	})

	// Step c: tmux new-session (detached) at the project root.
	if err := s.tmux.NewSession(ctx, sessionName, req.ProjectPath); err != nil {
		return bail(fmt.Errorf("spawn: tmux new-session %s: %w", sessionName, err))
	}
	cleanups.push(func() error {
		killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.tmux.KillPane(killCtx, sessionName)
	})

	// Step d: pipe-pane stdout into the fifo so U10 watchdog can tail it.
	if err := s.tmux.PipePane(ctx, sessionName, fifoPath); err != nil {
		return bail(fmt.Errorf("spawn: tmux pipe-pane: %w", err))
	}

	// Step e: send the bootstrap opencode invocation. No --session: this is
	// the capture leg of capture-then-resume per Decision 5 in contracts.md.
	//
	// U19: when req.MountReads is non-empty, prepend
	// `COMMANDER_READS=<colon-joined-paths>` so the opencode process (and any
	// subshells it spawns) sees the additional repo paths. Knowledge operators
	// have no sandbox, so they read these host paths directly via the env var.
	bootstrap := knowledgeBootstrapPrompt(req)
	envPrefix := ""
	if len(req.MountReads) > 0 {
		envPrefix = "COMMANDER_READS=" + shellQuote(strings.Join(req.MountReads, ":")) + " "
	}
	cmdLine := fmt.Sprintf(
		"%sopencode run --format json --agent %s --model %s %s",
		envPrefix, agentName, shellQuote(req.Model), shellQuote(bootstrap),
	)
	if kind == agentrt.Claude {
		args := agentrt.ClaudeHeadlessArgs(agentName, agentrt.ClaudePromptPath(agentName), req.Model, agentrt.NewClaudeSessionID(), bootstrap, false)
		for i, a := range args {
			args[i] = shellQuote(a)
		}
		cmdLine = "env -u CLAUDE_CONFIG_DIR " + envPrefix + strings.Join(args, " ")
	}
	if err := s.tmux.SendKeys(ctx, sessionName, cmdLine); err != nil {
		return bail(fmt.Errorf("spawn: tmux send-keys: %w", err))
	}

	// Step f: capture sessionID by reading the fifo until the first nd-JSON
	// event with a non-empty sessionID arrives.
	captureFn := s.captureKnowledgeSession
	if captureFn == nil {
		captureFn = captureSessionFromFifo
	}
	sessionID, err := captureFn(ctx, fifoPath, s.captureTimeout)
	if err != nil {
		return bail(fmt.Errorf("spawn: capture session: %w", err))
	}

	// Step g: persist session-id.
	if err := writeSessionFile(sessionFile, sessionID); err != nil {
		return bail(fmt.Errorf("spawn: persist session id: %w", err))
	}
	cleanups.push(func() error {
		return os.Remove(sessionFile)
	})

	// Step h: am register (idempotent).
	if err := s.am.Register(ctx, req.ProjectName, "opencode-ai", modelShort(req.Model), agentName); err != nil {
		return bail(fmt.Errorf("spawn: am register %s: %w", agentName, err))
	}

	// Step i: bd set-state cmdr=brainstorming.
	if err := s.beads.SetState(ctx, req.ProjectPath, req.TaskBeadID, "cmdr", "brainstorming", "operator spawned"); err != nil {
		return bail(fmt.Errorf("spawn: bd set-state %s: %w", req.TaskBeadID, err))
	}

	// Step j: insert agent_runs row. ContainerID stays NULL for knowledge-type;
	// TmuxPaneID gets the session name (which serves as the pane target for
	// downstream tmux commands).
	spawnTime := s.now()
	runID, err := s.store.InsertAgentRun(ctx, store.AgentRun{
		ProjectID:         req.ProjectID,
		TaskBeadID:        req.TaskBeadID,
		AgentName:         agentName,
		OpencodeSessionID: sessionID,
		TmuxPaneID:        sql.NullString{String: sessionName, Valid: true},
		SpawnTime:         spawnTime.Unix(),
		Status:            "running",
	})
	if err != nil {
		return bail(fmt.Errorf("spawn: insert agent_runs: %w", err))
	}

	return &SpawnResult{
		RunID:     runID,
		SessionID: sessionID,
		AgentName: agentName,
		// WorktreePath empty: knowledge operator works on the project root.
		// ContainerID empty: no Docker container.
		SpawnTime: spawnTime,
	}, nil
}

// knowledgeBootstrapPrompt is the seed message the knowledge operator sees on
// first invocation. Behavior lives in prompts/operator-knowledge.md.
func knowledgeBootstrapPrompt(req SpawnRequest) string {
	if req.ExecutionClaimToken != "" {
		return executionBootstrapPrompt(req, "Your assigned knowledge workspace is the project root. Follow its artifact and git rules; do not assume a shared-branch commit is authorized.")
	}
	return fmt.Sprintf(
		"You are the knowledge-type Operator for task %s in project %q. "+
			"Working directory is the project root; commit directly to the current branch. "+
			"Read the task body via `bd show %s` and reply with a one-line acknowledgement.",
		req.TaskBeadID, req.ProjectName, req.TaskBeadID,
	)
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes,
// so the resulting string survives intact through `tmux send-keys` into a
// shell. Used for opencode prompt args that may contain spaces or quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// captureSessionFromFifo opens fifoPath for reading and returns the first
// non-empty sessionID it finds in the nd-JSON event stream. Returns an error
// if timeout elapses before a sessionID arrives.
//
// Production path; tests inject a deterministic Spawner.captureKnowledgeSession.
func captureSessionFromFifo(ctx context.Context, fifoPath string, timeout time.Duration) (string, error) {
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Open the fifo for reading. This blocks until a writer (tmux pipe-pane's
	// `cat >> <fifo>`) connects, so a goroutine + select bounds the wait.
	type opened struct {
		f   *os.File
		err error
	}
	openCh := make(chan opened, 1)
	go func() {
		f, err := os.OpenFile(fifoPath, os.O_RDONLY, 0)
		openCh <- opened{f: f, err: err}
	}()

	var f *os.File
	select {
	case o := <-openCh:
		if o.err != nil {
			return "", fmt.Errorf("open fifo %s: %w", fifoPath, o.err)
		}
		f = o.f
	case <-tCtx.Done():
		return "", fmt.Errorf("session-capture timeout after %s (fifo never opened)", timeout)
	}
	defer f.Close()

	type result struct {
		id  string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		id, err := scanFifoForSessionID(bufio.NewReader(f))
		resCh <- result{id: id, err: err}
	}()

	select {
	case r := <-resCh:
		if r.err != nil {
			return "", r.err
		}
		if r.id == "" {
			return "", errors.New("fifo closed before sessionID arrived")
		}
		return r.id, nil
	case <-tCtx.Done():
		return "", fmt.Errorf("session-capture timeout after %s", timeout)
	}
}

// scanFifoForSessionID reads nd-JSON lines from r and returns the first
// non-empty sessionID it finds. Mirrors scanForSessionID (code-type) but
// reads via a bufio.Reader rather than a Scanner so very large lines do not
// trigger token-too-long errors on the long-running fifo.
func scanFifoForSessionID(r *bufio.Reader) (string, error) {
	for {
		line, err := r.ReadBytes('\n')
		trimmed := bytes_trimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			if id := agentrt.EventSessionID(trimmed); id != "" {
				return id, nil
			}
		}
		if err != nil {
			// io.EOF on a fifo means the writer closed without yielding a
			// sessionID — surface empty so caller errors with the standard
			// "fifo closed before sessionID arrived" message.
			return "", nil
		}
	}
}
