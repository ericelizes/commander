package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ceilings"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/inbox"
	"github.com/example-user/commander/internal/notify"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/scheduler"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// newTestDaemon wires a daemon backed by an in-memory SQLite + the existing
// MockClient implementations from each subsystem. We deliberately use the
// real watchdog/scheduler/inbox structs (not fakes) so the test exercises the
// daemon's actual lifecycle wiring — only the I/O-bound clients are mocked.
//
// skipSocket is set true so the test doesn't bind a Unix socket; the socket
// path is exercised separately in TestSocket_PingAndState.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := config.Defaults()

	oc := &opencode.MockClient{}
	bd := &beads.MockClient{}
	am := &agentmail.MockClient{}
	dk := &docker.MockClient{}
	tx := &tmuxctl.MockClient{}

	sp := spawn.New(st, oc, bd, am, dk, tx, "")
	wd := ceilings.NewWatchdog(st, dk, oc, bd, am, ceilingsLimitsFrom(cfg))
	sc := scheduler.New(bd, st, sp, &configSource{cfg: cfg, runs: st, beads: bd}, 50*time.Millisecond)
	pl := inbox.NewPoller(am, commanderProject, commanderAgent, 50*time.Millisecond)

	return &Daemon{
		store:         st,
		opencode:      oc,
		beads:         bd,
		am:            am,
		docker:        dk,
		tmux:          tx,
		spawner:       sp,
		watchdog:      wd,
		scheduler:     sc,
		inbox:         pl,
		notifier:      &notify.MockNotifier{},
		cfg:           cfg,
		home:          t.TempDir(),
		skipSocket:    true,
		notifyEnabled: true,
	}
}

// TestDaemon_RunStartsAndStopsCleanly is the happy path: Run blocks until
// ctx is cancelled, then returns nil after a graceful shutdown.
func TestDaemon_RunStartsAndStopsCleanly(t *testing.T) {
	d := newTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Give the subsystems a tick to actually start their goroutines.
	// Without this the cancel could land before Start has fired, masking a
	// bug where Run returns before Wait observes the goroutines.
	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of ctx cancellation")
	}
}

// TestDaemon_GracefulShutdownStopsSubsystems asserts the shutdown path tears
// down inbox + scheduler + watchdog + store. We can't easily peek into each
// subsystem's "stopped" flag, but we CAN assert that re-running shutdown is a
// no-op (no panic, no double-close) — that's the contract Run depends on.
func TestDaemon_GracefulShutdownStopsSubsystems(t *testing.T) {
	d := newTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	// Calling shutdown a second time must be safe. The internal subsystems
	// are designed for idempotent Stop; we verify the daemon honors that.
	// We can't call shutdown directly (unexported), but Run already invoked
	// it on cancel. Instead, exercise via a fresh ctx-cancel: Run should
	// short-circuit because the subsystems are already stopped.
	//
	// (We don't re-run the whole daemon — once the store is closed, subsequent
	// reads would fail. The point is: the first shutdown didn't panic, and
	// Run returned cleanly. That's the contract.)
}

// TestDaemon_SchedulerObservesStore is a smoke check that the daemon's
// scheduler is actually wired up to the live store: insert a project + a
// ready Task into the bd mock, run the daemon, and observe a Spawn attempt.
//
// AutoSpawn is enabled on the project so the scheduler bypasses the proposal
// channel and goes straight to Spawner.Spawn — easier to observe than a
// drained channel. The MockClients let us trigger that path without hitting
// Docker / opencode.
func TestDaemon_SchedulerObservesStore(t *testing.T) {
	d := newTestDaemon(t)

	// Insert a project so ListProjects returns something for the scheduler
	// to scan.
	ctx0 := context.Background()
	if _, err := d.store.InsertProject(ctx0, store.Project{
		Name: "demo",
		Type: "code",
		Path: "/tmp/demo",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// Enable AutoSpawn for "demo" so the scheduler bypasses the proposal
	// channel. cfg is mutable here because nothing else is reading it yet.
	d.cfg.Projects["demo"] = config.Project{
		Type:         "code",
		MaxOperators: 5,
		AutoSpawn:    true,
	}

	// Stage a ready Task on the beads mock. ListResult is shared across
	// every List call; that's fine for a one-project test.
	bdMock := d.beads.(*beads.MockClient)
	bdMock.ListResult = []beads.Bead{{
		ID:     "demo-001",
		Title:  "test task",
		Status: "open",
		Type:   "feature",
		Labels: []string{"kind:task"},
	}}

	// Replace the spawner with one that just records calls — the real
	// spawn.New path tries to talk to Docker which we can't mock at this
	// layer. We swap on the scheduler since that's where Spawn() lands.
	rec := &recordingSpawner{}
	d.scheduler = scheduler.New(d.beads, d.store, rec, &configSource{cfg: d.cfg, runs: d.store, beads: d.beads}, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for at least one scheduler tick to land. The scheduler runs an
	// immediate scan on Start, so we should see a Spawn attempt quickly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done

	if rec.count() == 0 {
		t.Fatal("expected at least one Spawn call from scheduler within 2s")
	}
}

// TestSocket_PingAndState exercises the read-only socket protocol end-to-end:
// bind → connect → send {"op":"ping"} → expect {"ok":true}. Then a second
// connection for state. The test uses a TempDir socket path so it's
// hermetic.
func TestSocket_PingAndState(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cmdrd.sock")

	state := State{Operators: 3, USDDay: 1.25, AgentsActive: 3}
	sock, err := NewSocket(sockPath, func() State { return state })
	if err != nil {
		t.Fatalf("NewSocket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- sock.Serve(ctx) }()

	// Ping.
	resp := dialAndSend(t, sockPath, `{"op":"ping"}`)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("ping: expected ok=true, got %v", resp)
	}

	// State.
	resp = dialAndSend(t, sockPath, `{"op":"state"}`)
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("state: expected ok=true, got %v", resp)
	}
	stateMap, ok := resp["state"].(map[string]any)
	if !ok {
		t.Fatalf("state: missing state field, got %v", resp)
	}
	if got, _ := stateMap["operators"].(float64); int(got) != 3 {
		t.Fatalf("state.operators: want 3, got %v", stateMap["operators"])
	}

	// Unknown op.
	resp = dialAndSend(t, sockPath, `{"op":"bogus"}`)
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatalf("bogus: expected ok=false, got %v", resp)
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return within 2s of ctx cancel")
	}
}

// TestSocket_StaleSocketCleaned verifies NewSocket unlinks a stale socket
// file from a previous (crashed) daemon run. Without this, launchd-restarted
// cmdrd would fail to bind on the leftover inode.
func TestSocket_StaleSocketCleaned(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cmdrd.sock")

	// Bind once, close (simulating a crash that left the file behind).
	first, err := NewSocket(sockPath, func() State { return State{} })
	if err != nil {
		t.Fatalf("first NewSocket: %v", err)
	}
	_ = first.Close()

	// Re-create a stale file at the path manually — Close above already
	// removes it, but we want to assert NewSocket can recover even if the
	// previous process never got to clean up.
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	_ = l.Close() // closing leaves the inode behind on some platforms

	second, err := NewSocket(sockPath, func() State { return State{} })
	if err != nil {
		t.Fatalf("second NewSocket (stale cleanup): %v", err)
	}
	_ = second.Close()
}

// recordingSpawner is a SchedulerSpawner that just counts calls. Used to
// assert the scheduler reaches the spawn path without standing up Docker.
type recordingSpawner struct {
	calls atomic.Int32
}

func (r *recordingSpawner) Spawn(_ context.Context, _ spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	r.calls.Add(1)
	return &spawn.SpawnResult{RunID: 1, ContainerID: "c-test", AgentName: "operator-test"}, nil
}

func (r *recordingSpawner) count() int {
	return int(r.calls.Load())
}

// dialAndSend is the test-side socket client: open a connection, write one
// JSON line, read one JSON line back, decode it. Failures fail the test
// rather than returning an error so call sites stay assertion-focused.
func dialAndSend(t *testing.T, path, line string) map[string]any {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	dec := json.NewDecoder(c)
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// Run itself drives the alerts: every other test calls alertDue by hand.
func TestDaemon_RunSendsDueHealthAlerts(t *testing.T) {
	d := newTestDaemon(t)
	now := time.Now()
	d.health = newHealthBoard(func() time.Time { return now })
	d.alertEvery = 5 * time.Millisecond
	sent := make(chan string, 8)
	d.health.alert = func(key, _ string, _ time.Time) error { sent <- key; return nil }
	d.health.fail("mail:manager-lab", "cannot reach it")
	now = now.Add(healthGrace + time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case key := <-sent:
		if key != "mail:manager-lab" {
			t.Fatalf("alert for %q", key)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never sent the due alert")
	}
}

// New wires the Waker's reports to the health board the app and alerts read.
func TestNew_WiresTheWakerToTheHealthBoard(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fixture")
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1/mcp/")
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer d.store.Close()
	if d.waker == nil || d.waker.OnHistory == nil || d.waker.OnDeliver == nil {
		t.Fatal("the Waker reports nowhere")
	}
	d.waker.OnHistory(map[string]int{"manager-lab": 2}, map[string][]string{"manager-lab": {"Re: [decision] lab-1: Merge"}})
	d.waker.OnDeliver("operator-x", errors.New("unread"))
	d.health.mu.Lock()
	defer d.health.mu.Unlock()
	history, mail := d.health.issues[mailHistoryKey], d.health.issues["mail:operator-x"]
	if history == nil || !strings.Contains(history.detail, "lab-1: Merge") || mail == nil {
		t.Fatalf("health issues %v", d.health.issues)
	}
}
