package chat

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/testguard"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat/chattest"
	"github.com/example-user/commander/internal/store"
)

func TestMain(m *testing.M) {
	// Also applies in re-executed bridge/host helpers, before dispatch: no test
	// inventories the host's system/managed MCPs or inherits browser opt-in.
	// Empty paths are missing files, including in helpers that exit directly.
	codexSystemConfigPath = ""
	codexManagedConfigPath = ""
	os.Unsetenv("COMMANDER_CODEX_BROWSER_SERVER")
	MaybeRunHost()
	MaybeRunCodexBridge()
	maybeRunFakeCodex() // before the fake claude: it shares the fake's env
	chattest.MaybeRunFake()
	os.Setenv("COMMANDER_RUNTIME", "claude")
	cleanup := testguard.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type fakeSink struct {
	mu   sync.Mutex
	rows []string // "role state detail"
}

func (f *fakeSink) SetAgentState(_ context.Context, role, state, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, strings.TrimSpace(role+" "+state+" "+detail))
	return nil
}

func (f *fakeSink) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rows...)
}

type fakeProcs struct {
	mu    sync.Mutex
	procs map[string][]Proc
}

func (f *fakeProcs) set(id string, p []Proc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[id] = p
}

func (f *fakeProcs) scan(_ context.Context, id string) ([]Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[id], nil
}

type harness struct {
	t        *testing.T
	home     string
	projects string
	fakeDir  string
	work     string
	sink     *fakeSink
	procs    *fakeProcs
	ids      map[string]string
	opts     Options
	e        *Engine
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Every fake chat has a connected account in its own HOME, never the user's.
	user := t.TempDir()
	t.Setenv("HOME", user)
	os.MkdirAll(filepath.Join(user, ".claude"), 0700)
	os.WriteFile(filepath.Join(user, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0600)
	h := &harness{t: t, home: t.TempDir(), projects: t.TempDir(), fakeDir: t.TempDir(), work: t.TempDir(),
		sink: &fakeSink{}, procs: &fakeProcs{procs: map[string][]Proc{}}, ids: map[string]string{}}
	t.Setenv(chattest.EnvVar, "1")
	t.Setenv(chattest.DirVar, h.fakeDir)
	t.Setenv(chattest.ProjectsVar, h.projects)
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")
	h.opts = Options{
		Home: h.home, Binary: os.Args[0], States: h.sink, Procs: h.procs.scan, ProjectsDir: h.projects,
		InterruptGrace: 2 * time.Second, StopGrace: 2 * time.Second,
		Resolver: ResolverFunc(func(_ context.Context, role string) (Spec, error) {
			id, ok := h.ids[role]
			if !ok {
				return Spec{}, errors.New("unknown role")
			}
			return Spec{Role: role, Agent: role, SessionID: id, Cwd: h.work, Model: "haiku",
				Settings: `{"hooks":{}}`, AllowedTools: "Bash(cmdr:*)"}, nil
		}),
	}
	h.e = New(h.opts)
	t.Cleanup(func() {
		for _, s := range h.e.List() {
			if s.Running {
				_ = h.e.Stop(context.Background(), s.Role)
			}
		}
	})
	return h
}

func (h *harness) role(role string) string {
	id := agentrt.NewClaudeSessionID()
	h.ids[role] = id
	return id
}

func (h *harness) start(role string) Status {
	h.t.Helper()
	st, err := h.e.Start(context.Background(), role, StartOptions{})
	if err != nil {
		h.t.Fatalf("start %s: %v", role, err)
	}
	return st
}

// subscribe returns a feed that skips history.
func (h *harness) subscribe(role string) *Subscription {
	h.t.Helper()
	st, _ := h.e.Status(role)
	_, sub, err := h.e.Subscribe(role, st.LastSeq)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(sub.Close)
	return sub
}

// waitFor reads sub until pred matches, returning everything read.
func waitFor(t *testing.T, sub *Subscription, pred func(Event) bool) []Event {
	t.Helper()
	var seen []Event
	timeout := time.After(30 * time.Second)
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscription closed; saw %s", summarize(seen))
			}
			seen = append(seen, e)
			if pred(e) {
				return seen
			}
		case <-timeout:
			t.Fatalf("timed out; saw %s", summarize(seen))
		}
	}
}

func isKind(k Kind) func(Event) bool { return func(e Event) bool { return e.Kind == k } }

func summarize(events []Event) string {
	var parts []string
	for _, e := range events {
		parts = append(parts, string(e.Kind)+":"+e.Text)
	}
	return strings.Join(parts, " | ")
}

func (h *harness) fakeRecord(name string) map[string]any {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.fakeDir, name+".json"))
	if err != nil {
		h.t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func TestStartSendStreamsEventsAndState(t *testing.T) {
	h := newHarness(t)
	id := h.role("operator-a")
	st := h.start("operator-a")
	if !st.Running || st.SessionID != id || st.PID == 0 {
		t.Fatalf("status = %+v", st)
	}
	sub := h.subscribe("operator-a")
	if err := h.e.Send(context.Background(), "operator-a", "hello there"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindResult))
	if seen[0].Kind != KindUser || seen[0].Origin != "user" || seen[0].Text != "hello there" {
		t.Fatalf("first event = %+v", seen[0])
	}
	var deltas, final string
	var lastSeq uint64
	for _, e := range seen {
		if e.Seq <= lastSeq {
			t.Fatalf("seq not increasing: %d after %d", e.Seq, lastSeq)
		}
		lastSeq = e.Seq
		if e.Role != "operator-a" || e.SessionID != id {
			t.Fatalf("event header = %+v", e)
		}
		switch e.Kind {
		case KindTextDelta:
			deltas += e.Text
		case KindText:
			final = e.Text
		}
	}
	if final != "echo: hello there" || deltas != final {
		t.Fatalf("deltas %q final %q", deltas, final)
	}
	res := seen[len(seen)-1].Result
	if res.CostDeltaUSD != 0.01 || res.TotalCostUSD != 0.01 {
		t.Fatalf("result = %+v", res)
	}
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindState && e.State.State == store.AgentIdle })

	rec := h.fakeRecord("argv-" + id)
	argv := strings.Join(toStrings(rec["argv"]), " ")
	for _, want := range []string{"-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages",
		"--permission-prompt-tool stdio", "-n operator-a", "--model haiku", "--disallowedTools mcp__mcp-agent-mail,mcp__mcp_agent_mail",
		"--allowedTools Bash(cmdr:*)", `--settings {"hooks":{}}`, "--session-id " + id} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	if rec["api_key_set"] != false || rec["lock_fd"] != true {
		t.Fatalf("child env/lock: %+v", rec)
	}
	if cwd, _ := filepath.EvalSymlinks(rec["cwd"].(string)); !strings.HasSuffix(cwd, filepath.Base(h.work)) {
		t.Fatalf("cwd = %v", rec["cwd"])
	}
	rows := strings.Join(h.sink.all(), "\n")
	if !strings.Contains(rows, "operator-a working") || !strings.HasSuffix(rows, "operator-a idle") {
		t.Fatalf("agent_state rows = %s", rows)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestApprovalRoundTripAllowSession(t *testing.T) {
	h := newHarness(t)
	h.role("manager-b")
	h.start("manager-b")
	sub := h.subscribe("manager-b")
	_ = h.e.Send(context.Background(), "manager-b", "run a tool")
	seen := waitFor(t, sub, isKind(KindPermission))
	req := seen[len(seen)-1].Request
	if req.ToolName != "Bash" || !strings.Contains(string(req.Input), "echo hi") || !strings.Contains(string(req.Suggestions), "localSettings") {
		t.Fatalf("request = %+v", req)
	}
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindState && e.State.State == store.AgentWaiting })
	st, _ := h.e.Status("manager-b")
	if len(st.Pending) != 1 || st.Pending[0].ID != req.ID || st.State != store.AgentWaiting || st.Detail != "needs you: approve Bash" {
		t.Fatalf("status = %+v", st)
	}
	if err := h.e.Approve(context.Background(), "manager-b", "nope", Decision{Behavior: "allow"}); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("unknown id: %v", err)
	}
	if err := h.e.Approve(context.Background(), "manager-b", req.ID, Decision{Behavior: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	seen = waitFor(t, sub, isKind(KindResult))
	var resolved, result bool
	for _, e := range seen {
		if e.Kind == KindResolved && e.Request.ID == req.ID && e.Request.Decision == "allow_session" {
			resolved = true
		}
		if e.Kind == KindToolResult && e.Tool.Content == "hi" && e.Tool.Name == "Bash" && len(e.Tool.Structured) > 0 {
			result = true
		}
	}
	if !resolved || !result {
		t.Fatalf("resolved=%v result=%v: %s", resolved, result, summarize(seen))
	}
	resp := h.fakeRecord("response-Bash")["response"].(map[string]any)
	perms, _ := json.Marshal(resp["updatedPermissions"])
	if resp["behavior"] != "allow" || !strings.Contains(string(perms), `"destination":"session"`) || strings.Contains(string(perms), "localSettings") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestDenyAnswerAndPlan(t *testing.T) {
	h := newHarness(t)
	h.role("operator-c")
	h.start("operator-c")
	sub := h.subscribe("operator-c")
	ctx := context.Background()

	_ = h.e.Send(ctx, "operator-c", "tool please")
	req := waitFor(t, sub, isKind(KindPermission))
	if err := h.e.Approve(ctx, "operator-c", req[len(req)-1].Request.ID, Decision{Behavior: "deny", Message: "not now"}); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindResult))
	if !strings.Contains(summarize(seen), "Bash was denied") {
		t.Fatalf("deny: %s", summarize(seen))
	}

	_ = h.e.Send(ctx, "operator-c", "ask me")
	q := waitFor(t, sub, isKind(KindQuestion))
	qid := q[len(q)-1].Request.ID
	if err := h.e.Approve(ctx, "operator-c", qid, Decision{Behavior: "maybe"}); err == nil {
		t.Fatal("bad decision accepted")
	}
	if err := h.e.Answer(ctx, "operator-c", qid, map[string]string{"Which color?": "blue"}); err != nil {
		t.Fatal(err)
	}
	seen = waitFor(t, sub, isKind(KindResult))
	if !strings.Contains(summarize(seen), `done: answers: {"Which color?":"blue"}`) {
		t.Fatalf("answer: %s", summarize(seen))
	}

	_ = h.e.Send(ctx, "operator-c", "plan it")
	p := waitFor(t, sub, isKind(KindPlan))
	pid := p[len(p)-1].Request.ID
	if err := h.e.Answer(ctx, "operator-c", pid, nil); err == nil {
		t.Fatal("answered a plan as a question")
	}
	if err := h.e.Approve(ctx, "operator-c", pid, Decision{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindResult))
}

func TestInterruptKeepsProcess(t *testing.T) {
	h := newHarness(t)
	h.role("operator-d")
	h.start("operator-d")
	sub := h.subscribe("operator-d")
	ctx := context.Background()
	_ = h.e.Send(ctx, "operator-d", "long story")
	n := 0
	waitFor(t, sub, func(e Event) bool {
		if e.Kind == KindTextDelta {
			n++
		}
		return n >= 5
	})
	if err := h.e.DeliverMail(ctx, "operator-d", "[mail] x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("mail mid-turn: %v", err)
	}
	if err := h.e.Interrupt(ctx, "operator-d"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindResult))
	r := seen[len(seen)-1].Result
	if r.Subtype != "error_during_execution" || r.TerminalReason != "aborted_streaming" {
		t.Fatalf("result = %+v", r)
	}
	if !h.e.Running("operator-d") {
		t.Fatal("interrupt killed the process")
	}
	if err := h.e.DeliverMail(ctx, "operator-d", "[mail] from commander: hi"); err != nil {
		t.Fatal(err)
	}
	seen = waitFor(t, sub, isKind(KindResult))
	if u := kinds(seen, KindUser); len(u) != 1 || u[0].Origin != "mail" {
		t.Fatalf("mail turn = %s", summarize(seen))
	}
}

func TestInterruptFallsBackToSIGINT(t *testing.T) {
	h := newHarness(t)
	h.opts.InterruptGrace = 300 * time.Millisecond
	h.e = New(h.opts)
	h.role("operator-e")
	h.start("operator-e")
	sub := h.subscribe("operator-e")
	_ = h.e.Send(context.Background(), "operator-e", "deaf long")
	waitFor(t, sub, isKind(KindTextDelta))
	if err := h.e.Interrupt(context.Background(), "operator-e"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindExit))
	s := summarize(seen)
	if !strings.Contains(s, "notice:no interrupt ack") || !strings.Contains(s, "turn_result") {
		t.Fatalf("fallback: %s", s)
	}
	if h.e.Running("operator-e") {
		t.Fatal("still running after SIGINT")
	}
}

func TestCrashMidTurnIsStuck(t *testing.T) {
	h := newHarness(t)
	h.role("operator-f")
	h.start("operator-f")
	sub := h.subscribe("operator-f")
	_ = h.e.Send(context.Background(), "operator-f", "crash now")
	seen := waitFor(t, sub, func(e Event) bool { return e.Kind == KindState && e.State.State == store.AgentStuck })
	var exit *Exit
	for _, e := range seen {
		if e.Kind == KindExit {
			exit = e.Exit
		}
	}
	if exit == nil || exit.Code != 3 || exit.Expected {
		t.Fatalf("exit = %+v", exit)
	}
	waitUntil(t, "stuck state row", func() bool { // the sink is written just after the event is published
		rows := h.sink.all()
		return strings.HasPrefix(rows[len(rows)-1], "operator-f stuck process exited mid-turn (code 3)")
	})
	if readMeta(filepath.Join(h.home, "chat"), "operator-f").Running {
		t.Fatal("a crashed chat must not be auto-resumed")
	}
}

func TestUnsupportedControlRequestIsRefused(t *testing.T) {
	h := newHarness(t)
	h.role("operator-g")
	h.start("operator-g")
	sub := h.subscribe("operator-g")
	_ = h.e.Send(context.Background(), "operator-g", "unsupported")
	waitFor(t, sub, isKind(KindResult))
	if r := h.fakeRecord("unsupported"); r["subtype"] != "error" || r["request_id"] != "hook-1" {
		t.Fatalf("response = %+v", r)
	}
}

func TestTaskAndCompactionEvents(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h")
	h.start("operator-h")
	sub := h.subscribe("operator-h")
	_ = h.e.Send(context.Background(), "operator-h", "task")
	seen := waitFor(t, sub, isKind(KindResult))
	var started, nested, done bool
	for _, e := range seen {
		switch {
		case e.Kind == KindTask && e.Subtype == "task_started":
			started = true
		case e.Kind == KindText && e.ParentToolUseID == "toolu_task":
			nested = true
		case e.Kind == KindTask && e.Task.Status == "completed":
			done = true
		}
	}
	if !started || !nested || !done {
		t.Fatalf("task events: %s", summarize(seen))
	}
	_ = h.e.Send(context.Background(), "operator-h", "compact")
	seen = waitFor(t, sub, isKind(KindResult))
	if len(kinds(seen, KindCompaction)) != 1 {
		t.Fatalf("compaction: %s", summarize(seen))
	}
}

func TestLockRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A terminal claude (the tmux TUI) has the session open.
	id := h.role("manager-t")
	h.procs.set(id, []Proc{{PID: 4242, Command: "claude -n manager-t --dangerously-skip-permissions --resume " + id}})
	_, err := h.e.Start(ctx, "manager-t", StartOptions{})
	var owned *OwnedError
	if !errors.As(err, &owned) || owned.By != "terminal" || owned.PID != 4242 || !strings.Contains(err.Error(), "owned by a terminal") {
		t.Fatalf("terminal owner: %v", err)
	}
	if h.e.Running("manager-t") {
		t.Fatal("started despite the owner")
	}
	// An orphaned headless claude.
	h.procs.set(id, []Proc{{PID: 4243, Command: "claude -p --input-format stream-json --resume " + id}})
	if _, err := h.e.Start(ctx, "manager-t", StartOptions{}); !errors.As(err, &owned) || owned.By != "headless" {
		t.Fatalf("headless owner: %v", err)
	}
	h.procs.set(id, nil)

	// The flock is held (another cmdrd, or an orphan that inherited it).
	other, err := acquireSessionLock(filepath.Join(h.home, "chat-locks"), id, "elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Start(ctx, "manager-t", StartOptions{}); !errors.As(err, &owned) || owned.By != "lock" || !strings.Contains(owned.Detail, "role=elsewhere") {
		t.Fatalf("lock owner: %v", err)
	}
	other.release()

	// Two roles on one session id inside the engine.
	h.start("manager-t")
	h.ids["manager-u"] = id
	if _, err := h.e.Start(ctx, "manager-u", StartOptions{}); !errors.As(err, &owned) || owned.By != "engine" || owned.Role != "manager-t" {
		t.Fatalf("engine owner: %v", err)
	}
	// Starting the running role again is a no-op.
	if st, err := h.e.Start(ctx, "manager-t", StartOptions{}); err != nil || !st.Running {
		t.Fatalf("restart running: %v %+v", err, st)
	}
	// After Stop the lock is free again.
	if err := h.e.Stop(ctx, "manager-t"); err != nil {
		t.Fatal(err)
	}
	l, err := acquireSessionLock(filepath.Join(h.home, "chat-locks"), id, "x")
	if err != nil {
		t.Fatalf("lock not released after stop: %v", err)
	}
	l.release()
}

func TestResumeHydratesTranscriptOnce(t *testing.T) {
	h := newHarness(t)
	id := "00000000-0000-4000-8000-000000000001"
	h.ids["manager-r"] = id
	raw, err := os.ReadFile("testdata/transcript_synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, h.projects, id, string(raw))
	// The fake continues the cumulative cost like Claude does.
	_ = os.WriteFile(filepath.Join(h.fakeDir, id+".cost"), []byte("0.04"), 0o644)

	h.start("manager-r")
	hist, sub, err := h.e.Subscribe("manager-r", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	transcriptEvents := 0
	for _, e := range hist {
		if e.Source == SourceTranscript {
			transcriptEvents++
		}
	}
	if transcriptEvents < 20 || hist[0].Source != SourceTranscript {
		t.Fatalf("hydrated %d events: %s", transcriptEvents, summarize(hist))
	}
	_ = h.e.Send(context.Background(), "manager-r", "hello again")
	seen := waitFor(t, sub, isKind(KindResult))
	if !strings.Contains(strings.Join(toStrings(h.fakeRecord("argv-" + id)["argv"]), " "), "--resume "+id) {
		t.Fatal("did not resume")
	}
	if d := seen[len(seen)-1].Result.CostDeltaUSD; d < 0.0099 || d > 0.0101 {
		t.Fatalf("delta after resume = %v (baseline from the transcript cost-state)", d)
	}
	if err := h.e.Stop(context.Background(), "manager-r"); err != nil {
		t.Fatal(err)
	}

	// A fresh engine (cmdrd restart) on the same home: no second hydration.
	e2 := New(h.opts)
	if _, err := e2.Start(context.Background(), "manager-r", StartOptions{}); err != nil {
		t.Fatal(err)
	}
	defer e2.Stop(context.Background(), "manager-r")
	hist2, sub2, _ := e2.Subscribe("manager-r", 0)
	defer sub2.Close()
	n := 0
	for _, e := range hist2 {
		if e.Source == SourceTranscript {
			n++
		}
	}
	if n != transcriptEvents {
		t.Fatalf("transcript events after restart = %d, want %d", n, transcriptEvents)
	}
}

func TestEventLogReplayAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.role("operator-l")
	h.start("operator-l")
	sub := h.subscribe("operator-l")
	_ = h.e.Send(context.Background(), "operator-l", "one")
	waitFor(t, sub, isKind(KindResult))
	_ = h.e.Send(context.Background(), "operator-l", "two")
	waitFor(t, sub, isKind(KindResult))

	// In-process replay includes the live-only deltas.
	mem, s1, _ := h.e.Subscribe("operator-l", 0)
	s1.Close()
	if len(kinds(mem, KindTextDelta)) == 0 {
		t.Fatal("ring replay lacks deltas")
	}
	if err := h.e.Stop(context.Background(), "operator-l"); err != nil {
		t.Fatal(err)
	}

	e2 := New(h.opts)
	all, s2, err := e2.Subscribe("operator-l", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(kinds(all, KindTextDelta)) != 0 || len(kinds(all, KindStatus)) != 0 {
		t.Fatal("deltas/status must not be persisted")
	}
	if len(kinds(all, KindUser)) != 2 || len(kinds(all, KindResult)) != 2 || len(kinds(all, KindExit)) != 1 {
		t.Fatalf("replay = %s", summarize(all))
	}
	var last uint64
	for _, e := range all {
		if e.Seq <= last {
			t.Fatalf("seq %d after %d", e.Seq, last)
		}
		last = e.Seq
	}
	mid := all[len(all)/2].Seq
	tail, s3, _ := e2.Subscribe("operator-l", mid)
	s3.Close()
	if len(tail) == 0 || tail[0].Seq <= mid {
		t.Fatalf("since=%d replay starts at %+v", mid, tail)
	}
	// Seqs continue after restart.
	st, _ := e2.Status("operator-l")
	if st.LastSeq != last || st.TotalCostUSD < 0.0199 {
		t.Fatalf("status after restart = %+v", st)
	}
	if list := e2.List(); len(list) != 1 || list[0].Role != "operator-l" || list[0].Running {
		t.Fatalf("list = %+v", list)
	}
}

func TestCommandsNeedARunningProcess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.e.Send(ctx, "operator-z", "hi"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("send: %v", err)
	}
	if err := h.e.Interrupt(ctx, "operator-z"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("interrupt: %v", err)
	}
	if _, err := h.e.Start(ctx, "../etc", StartOptions{}); err == nil {
		t.Fatal("invalid role accepted")
	}
}

func writeTranscript(t *testing.T, projects, id, body string) {
	t.Helper()
	dir := filepath.Join(projects, "-tmp-work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusHydratesTerminalHeldTranscript(t *testing.T) {
	h := newHarness(t)
	id := "00000000-0000-4000-8000-000000000001"
	h.ids["manager-t"] = id
	raw, err := os.ReadFile("testdata/transcript_synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	half := strings.Join(lines[:len(lines)/2], "")
	writeTranscript(t, h.projects, id, half)
	h.procs.set(id, []Proc{{PID: 4242, Command: "claude --resume " + id}})

	st, err := h.e.Status("manager-t")
	if err != nil {
		t.Fatal(err)
	}
	if st.OwnedBy != "terminal" || st.LastSeq == 0 {
		t.Fatalf("status = owned_by %q last_seq %d, want the terminal's history", st.OwnedBy, st.LastSeq)
	}
	first := st.LastSeq
	if again, _ := h.e.Status("manager-t"); again.LastSeq != first {
		t.Fatalf("an unchanged transcript republished: %d -> %d", first, again.LastSeq)
	}

	// The terminal keeps talking: only the new turns are published (after the owner cache expires).
	writeTranscript(t, h.projects, id, string(raw))
	h.e.mu.Lock()
	delete(h.e.ownerCache, "manager-t")
	h.e.mu.Unlock()
	st, _ = h.e.Status("manager-t")
	hist, sub, err := h.e.Subscribe("manager-t", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	want, _, _ := ReadTranscript(FindTranscript(h.projects, id), "manager-t")
	n := 0
	for _, e := range hist {
		if e.Source == SourceTranscript {
			n++
		}
	}
	if st.LastSeq <= first || n != len(want) {
		t.Fatalf("transcript events = %d, want %d (last_seq %d -> %d)", n, len(want), first, st.LastSeq)
	}
}

func TestNeverStarted(t *testing.T) {
	e := New(Options{Home: t.TempDir()})
	if !e.NeverStarted("manager-x") {
		t.Fatal("no log = never started")
	}
	if err := os.MkdirAll(e.logDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.logDir(), "manager-x.jsonl")
	os.WriteFile(path, nil, 0o644)
	if !e.NeverStarted("manager-x") {
		t.Fatal("empty log = never started")
	}
	os.WriteFile(path, []byte("{}\n"), 0o644)
	if e.NeverStarted("manager-x") {
		t.Fatal("a log with events = started before")
	}
}
