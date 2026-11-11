package chat

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// restart is what a cmdrd restart does: the old engine lets go (Close), a new
// engine on the same home resumes the running roles.
func (h *harness) restart() *Engine {
	h.t.Helper()
	h.e.Close()
	e2 := New(h.opts)
	e2.ResumeRunning(context.Background())
	h.e = e2
	return e2
}

// seqsConsecutive fails when persisted seqs repeat or go backwards (live-only
// deltas take seqs too, so gaps between them are fine).
func seqsConsecutive(t *testing.T, e *Engine, role string) []Event {
	t.Helper()
	all, sub, err := e.Subscribe(role, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()
	var persistedEvents []Event
	var last uint64
	for _, ev := range all {
		if !persisted(ev.Kind) {
			continue
		}
		if ev.Seq <= last {
			t.Fatalf("seq %d after %d: %s", ev.Seq, last, summarize(all))
		}
		last = ev.Seq
		persistedEvents = append(persistedEvents, ev)
	}
	return persistedEvents
}

// A restart keeps the same claude pid and a turn waiting on an approval
// continues with the new engine: the pending request comes back and the turn
// finishes, with a log that has no gap and no repeat.
func TestRestartAdoptsSameClaudeMidTurn(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h1")
	st := h.start("operator-h1")
	sub := h.subscribe("operator-h1")
	_ = h.e.Send(context.Background(), "operator-h1", "tool please")
	waitFor(t, sub, isKind(KindPermission))

	e2 := h.restart()
	// A live attachment precedes asynchronous journal replay. Wait for the
	// pending permission to be reconstructed, not merely for the process pointer.
	waitUntil(t, "adopted permission replay", func() bool {
		current, err := e2.Status("operator-h1")
		return err == nil && current.Running && len(current.Pending) == 1 && current.Pending[0].ToolName == "Bash" && current.State == store.AgentWaiting
	})
	st2, _ := e2.Status("operator-h1")
	if st2.PID != st.PID || !alive(st.PID) {
		t.Fatalf("pid %d -> %d (alive %v)", st.PID, st2.PID, alive(st.PID))
	}
	if len(st2.Pending) != 1 || st2.Pending[0].ToolName != "Bash" || st2.State != store.AgentWaiting {
		t.Fatalf("status after adopt = %+v", st2)
	}

	_, sub2, _ := e2.Subscribe("operator-h1", st2.LastSeq)
	defer sub2.Close()
	if err := e2.Approve(context.Background(), "operator-h1", st2.Pending[0].ID, Decision{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub2, isKind(KindResult))

	evs := seqsConsecutive(t, e2, "operator-h1")
	if n := len(kinds(evs, KindPermission)); n != 1 {
		t.Fatalf("%d permission events: %s", n, summarize(evs))
	}
	if n := len(kinds(evs, KindResult)); n != 1 {
		t.Fatalf("%d results: %s", n, summarize(evs))
	}
	if n := len(kinds(evs, KindExit)); n != 0 {
		t.Fatalf("restart produced an exit: %s", summarize(evs))
	}
	// And it keeps working: a second turn on the same pid.
	_ = e2.Send(context.Background(), "operator-h1", "again")
	waitFor(t, sub2, isKind(KindResult))
	if st3, _ := e2.Status("operator-h1"); st3.PID != st.PID {
		t.Fatalf("pid changed to %d", st3.PID)
	}
}

// Output claude produces while no cmdrd is attached is not lost.
func TestRestartReplaysOutputFromTheGap(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h2")
	st := h.start("operator-h2")
	sub := h.subscribe("operator-h2")
	_ = h.e.Send(context.Background(), "operator-h2", "long answer")
	waitFor(t, sub, isKind(KindTextDelta))

	h.e.Close()
	time.Sleep(300 * time.Millisecond) // deltas keep flowing with nobody attached
	e2 := New(h.opts)
	e2.ResumeRunning(context.Background())
	h.e = e2
	waitUntil(t, "adopted", func() bool { return e2.Running("operator-h2") })
	_, sub2, _ := e2.Subscribe("operator-h2", 0)
	defer sub2.Close()
	if err := e2.Interrupt(context.Background(), "operator-h2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub2, isKind(KindResult))
	if st2, _ := e2.Status("operator-h2"); st2.PID != st.PID || !st2.Running {
		t.Fatalf("status = %+v", st2)
	}
	evs := seqsConsecutive(t, e2, "operator-h2")
	if len(kinds(evs, KindResult)) != 1 || len(kinds(evs, KindUser)) != 1 {
		t.Fatalf("events = %s", summarize(evs))
	}
}

// A claude that dies while nobody is attached is reported by the next engine.
func TestRestartReportsClaudeThatDiedWhileDetached(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h3")
	h.start("operator-h3")
	_ = h.e.Send(context.Background(), "operator-h3", "crash now")
	h.e.Close()
	e2 := New(h.opts)
	_, sub, _ := e2.Subscribe("operator-h3", 0)
	defer sub.Close()
	e2.ResumeRunning(context.Background())
	h.e = e2
	seen := waitFor(t, sub, isKind(KindExit))
	ex := seen[len(seen)-1].Exit
	if ex.Code != 3 || ex.Expected {
		t.Fatalf("exit = %+v", ex)
	}
	if e2.Running("operator-h3") {
		t.Fatal("still running after crash")
	}
}

// Close leaves claude and its host running; Stop ends both and frees the lock.
func TestCloseKeepsClaudeAndStopEndsHost(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h4")
	st := h.start("operator-h4")
	sock := hostSock(h.home, "operator-h4")
	h.e.Close()
	time.Sleep(200 * time.Millisecond)
	if !alive(st.PID) {
		t.Fatal("claude died with the engine")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("host socket gone: %v", err)
	}
	if _, err := acquireSessionLock(filepath.Join(h.home, "chat-locks"), h.ids["operator-h4"], "x"); err == nil {
		t.Fatal("session lock free while claude runs")
	}

	e2 := h.restart()
	waitUntil(t, "adopted", func() bool { return e2.Running("operator-h4") })
	if err := e2.Stop(context.Background(), "operator-h4"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "host gone", func() bool { _, err := os.Stat(sock); return err != nil })
	waitUntil(t, "claude gone", func() bool { return !alive(st.PID) })
	l, err := acquireSessionLock(filepath.Join(h.home, "chat-locks"), h.ids["operator-h4"], "next")
	if err != nil {
		t.Fatalf("lock still held after Stop: %v", err)
	}
	l.release()
	// A stopped chat is not resumed by the next restart.
	e3 := New(h.opts)
	e3.ResumeRunning(context.Background())
	time.Sleep(200 * time.Millisecond)
	if e3.Running("operator-h4") {
		t.Fatal("stopped chat resumed")
	}
}

// A host whose role is no longer marked running (a Stop that never reached it)
// is ended at the next resume, and a dead host's socket is removed.
func TestOrphanHostCleanup(t *testing.T) {
	h := newHarness(t)
	h.role("operator-h5")
	st := h.start("operator-h5")
	sock := hostSock(h.home, "operator-h5")
	h.e.Close()
	logDir := filepath.Join(h.home, "chat")
	m := readMeta(logDir, "operator-h5")
	m.Running = false
	if err := writeMeta(logDir, "operator-h5", m); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(logDir, "operator-stale.jsonl"), nil, 0o644) // known role, dead host
	stale := hostSock(h.home, "operator-stale")
	l, err := netListenUnix(stale)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close() // leaves the socket file with nobody behind it

	e2 := New(h.opts)
	e2.ResumeRunning(context.Background())
	waitUntil(t, "orphan host gone", func() bool { _, err := os.Stat(sock); return err != nil })
	waitUntil(t, "orphan claude gone", func() bool { return !alive(st.PID) })
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("stale socket left behind")
	}
	if e2.Running("operator-h5") {
		t.Fatal("orphan was adopted")
	}
}

// With no host and a pre-host orphan holding the session lock, resume waits and
// then starts a host.
func TestResumeWaitsForPreviousOwnersLock(t *testing.T) {
	h := newHarness(t)
	id := h.role("operator-w")
	logDir := filepath.Join(h.home, "chat")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMeta(logDir, "operator-w", meta{SessionID: id, Running: true}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(logDir, "operator-w.jsonl"), nil, 0o644)
	held, err := acquireSessionLock(filepath.Join(h.home, "chat-locks"), id, "old-cmdrd")
	if err != nil {
		t.Fatal(err)
	}
	h.opts.ResumeRetry = 10 * time.Millisecond
	e2 := New(h.opts)
	h.e = e2
	e2.ResumeRunning(context.Background())
	time.Sleep(100 * time.Millisecond)
	if e2.Running("operator-w") {
		t.Fatal("resumed while the old owner held the lock")
	}
	held.release()
	waitUntil(t, "resumed", func() bool { return e2.Running("operator-w") })
}

// seedLog writes a Running role whose event log is the given events.
func (h *harness) seedLog(role string, events ...Event) {
	h.t.Helper()
	id := h.role(role)
	logDir := filepath.Join(h.home, "chat")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := writeMeta(logDir, role, meta{SessionID: id, Running: true}); err != nil {
		h.t.Fatal(err)
	}
	var raw []byte
	for i, ev := range events {
		ev.Seq = uint64(i + 1)
		line, err := json.Marshal(ev)
		if err != nil {
			h.t.Fatal(err)
		}
		raw = append(raw, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(logDir, role+".jsonl"), raw, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func resumeMessages(e *Engine, role string) int {
	all, sub, err := e.Subscribe(role, 0)
	if err != nil {
		return 0
	}
	sub.Close()
	n := 0
	for _, ev := range all {
		if ev.Kind == KindUser && ev.Text == ResumeMessage && ev.Origin == "mail" {
			n++
		}
	}
	return n
}

// A restart that killed claude mid-turn leaves nothing to start another turn:
// the resumed role is told once to continue.
func TestResumeSendsContinueAfterCutShortTurn(t *testing.T) {
	h := newHarness(t)
	h.seedLog("manager-cut",
		Event{Kind: KindUser, Source: SourceEngine, Text: "do the thing", Origin: "user"},
		Event{Kind: KindToolInput, Source: SourceEngine, Tool: &Tool{ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}})
	e2 := New(h.opts)
	h.e = e2
	e2.ResumeRunning(context.Background())
	waitUntil(t, "resume message", func() bool { return resumeMessages(e2, "manager-cut") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := resumeMessages(e2, "manager-cut"); n != 1 {
		t.Fatalf("resume messages = %d, want exactly 1", n)
	}
}

func TestResumeStaysQuietWhenTurnFinished(t *testing.T) {
	h := newHarness(t)
	h.seedLog("manager-done",
		Event{Kind: KindUser, Source: SourceEngine, Text: "do the thing", Origin: "user"},
		Event{Kind: KindResult, Source: SourceEngine, Result: &Result{}})
	e2 := New(h.opts)
	h.e = e2
	e2.ResumeRunning(context.Background())
	waitUntil(t, "resumed", func() bool { return e2.Running("manager-done") })
	time.Sleep(200 * time.Millisecond)
	if n := resumeMessages(e2, "manager-done"); n != 0 {
		t.Fatalf("resume messages = %d, want 0", n)
	}
}

// Receiving exit is the Engine's permission to restart immediately. The old
// host must already be unreachable, and its final cleanup must spare the new one.
func TestHostRetiresSocketBeforePublishingExit(t *testing.T) {
	sock := hostSock(t.TempDir(), "retire")
	ln, err := netListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	h := &host{listener: ln, exited: &hostExit{Code: 0}}
	h.cond = sync.NewCond(&h.mu)
	cc := &hostClientConn{c: server}
	done := make(chan struct{})
	go func() { h.stream(cc, json.NewEncoder(server), 0); close(done) }()
	defer func() {
		h.mu.Lock()
		cc.closed = true
		h.cond.Broadcast()
		h.mu.Unlock()
		client.Close()
		<-done
	}()
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var msg hostMsg
	if err := json.NewDecoder(client).Decode(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.T != "exit" {
		t.Fatalf("message = %+v", msg)
	}
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		t.Fatal("exiting host is still available for adoption")
	}
	replacement, err := netListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	ln.Close() // runHost's deferred cleanup after the successor started
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("old cleanup removed successor: %v", err)
	}
}
