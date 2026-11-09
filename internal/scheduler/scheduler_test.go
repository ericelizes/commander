package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// fakeStore is a hand-rolled SchedulerStore for tests. Counters and lookups
// are scriptable; calls are not recorded because the assertions key off
// observable side effects (proposals emitted, spawns invoked).
type fakeStore struct {
	mu sync.Mutex

	projects []store.Project

	// runsByTask: presence => Task already has a running operator.
	runsByTask map[string]*store.AgentRun

	listErr error
	getErr  error
}

func (f *fakeStore) ListProjects(_ context.Context) ([]store.Project, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]store.Project(nil), f.projects...), nil
}

func (f *fakeStore) GetAgentRunByTaskBeadID(_ context.Context, taskBeadID string) (*store.AgentRun, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runsByTask[taskBeadID], nil
}

// fakeSpawner records every Spawn call for assertion. Returns a synthetic
// successful result by default; set Err to test the failure path.
type fakeSpawner struct {
	mu    sync.Mutex
	calls []spawn.SpawnRequest
	err   error
}

func (f *fakeSpawner) Spawn(_ context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &spawn.SpawnResult{RunID: 1, ContainerID: "c-fake", AgentName: "operator-fake"}, nil
}

func (f *fakeSpawner) Calls() []spawn.SpawnRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]spawn.SpawnRequest(nil), f.calls...)
}

// fakeConfig answers AutoSpawnEnabled / CheckCapacity from maps; full holds
// the Task ids whose workstream is at its Operator cap.
type fakeConfig struct {
	autoSpawn map[string]bool
	full      map[string]bool
}

func (f *fakeConfig) AutoSpawnEnabled(name string) bool {
	return f.autoSpawn[name]
}

func (f *fakeConfig) CheckCapacity(_ context.Context, _ store.Project, taskBeadID string) error {
	if f.full[taskBeadID] {
		return errors.New("workstream at its operator cap")
	}
	return nil
}

// drain reads up to n events from ch with a timeout.
func drain(t *testing.T, ch <-chan ProposalEvent, n int, timeout time.Duration) []ProposalEvent {
	t.Helper()
	out := make([]ProposalEvent, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

func newProject(id int64, name string) store.Project {
	return store.Project{
		ID:   id,
		Name: name,
		Type: "code",
		Path: "/tmp/projects/" + name,
	}
}

func newTaskBead(id, title string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Title:  title,
		Status: "open",
		Type:   "feature",
		Labels: []string{"kind:task"},
	}
}

// TestU17_ProposalEmittedAndConfirmTriggersSpawn: AutoSpawn=false, 1 ready
// task, capacity available => one ProposalEvent on Out(); calling Confirm
// triggers Spawner.Spawn with the event's identity fields.
func TestU17_ProposalEmittedAndConfirmTriggersSpawn(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(1, "frontend")},
		runsByTask: map[string]*store.AgentRun{},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead("cmdr-aaa", "wire auth")},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"frontend": false},
	}
	s := New(bd, st, sp, cfg, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	events := drain(t, s.Out(), 1, time.Second)
	if len(events) != 1 {
		t.Fatalf("want 1 proposal, got %d", len(events))
	}
	ev := events[0]
	if ev.TaskBeadID != "cmdr-aaa" || ev.ProjectName != "frontend" || ev.AutoSpawn {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if calls := sp.Calls(); len(calls) != 0 {
		t.Fatalf("U17: spawn must NOT be called before Confirm; got %d calls", len(calls))
	}

	if err := s.Confirm(ctx, ev); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	calls := sp.Calls()
	if len(calls) != 1 {
		t.Fatalf("Confirm should trigger 1 spawn; got %d", len(calls))
	}
	if calls[0].TaskBeadID != "cmdr-aaa" || calls[0].ProjectID != 1 || calls[0].ProjectType != "code" {
		t.Errorf("spawn req mismatch: %+v", calls[0])
	}
}

// TestU18_AutoSpawnsWithoutProposal: AutoSpawn=true, 1 ready task =>
// Spawner.Spawn called directly; no ProposalEvent on Out().
func TestU18_AutoSpawnsWithoutProposal(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(2, "survey")},
		runsByTask: map[string]*store.AgentRun{},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead("cmdr-bbb", "summarize talks")},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"survey": true},
	}
	s := New(bd, st, sp, cfg, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// Wait for the spawn to land.
	deadline := time.After(time.Second)
	for {
		if len(sp.Calls()) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("U18: spawn never invoked")
		case <-time.After(10 * time.Millisecond):
		}
	}
	calls := sp.Calls()
	if calls[0].TaskBeadID != "cmdr-bbb" {
		t.Errorf("spawn req mismatch: %+v", calls[0])
	}

	// No proposal should have been emitted on the happy path.
	select {
	case ev := <-s.Out():
		t.Fatalf("U18 happy path emitted a proposal: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestCapacityFull_NoSpawnNoProposal: the Task's workstream is at its cap =>
// the scheduler skips it for this scan (no proposal, no spawn).
func TestCapacityFull_NoSpawnNoProposal(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(3, "saturated")},
		runsByTask: map[string]*store.AgentRun{},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead("cmdr-ccc", "ignored")},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"saturated": true},
		full:      map[string]bool{"cmdr-ccc": true},
	}
	s := New(bd, st, sp, cfg, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// Give the loop two ticks to be confident nothing happens.
	time.Sleep(150 * time.Millisecond)

	if calls := sp.Calls(); len(calls) != 0 {
		t.Errorf("capacity full should block spawn; got %d calls", len(calls))
	}
	select {
	case ev := <-s.Out():
		t.Errorf("capacity full should suppress proposals; got %+v", ev)
	default:
	}
}

// TestDecline_SuppressesReproposalWithin24h: a declined Task should not be
// proposed again within the suppression window.
func TestDecline_SuppressesReproposalWithin24h(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(4, "polite")},
		runsByTask: map[string]*store.AgentRun{},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead("cmdr-ddd", "skip me")},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"polite": false},
	}
	s := New(bd, st, sp, cfg, 30*time.Millisecond)

	// Pre-decline before starting so the very first scan must already
	// suppress the proposal.
	s.Decline("cmdr-ddd")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// Wait for at least one scan tick to complete; we expect zero proposals.
	time.Sleep(150 * time.Millisecond)

	select {
	case ev := <-s.Out():
		t.Errorf("declined task should not be reproposed; got %+v", ev)
	default:
	}

	// And no spawn should have occurred.
	if calls := sp.Calls(); len(calls) != 0 {
		t.Errorf("declined task triggered spawn: %d calls", len(calls))
	}
}

// TestExistingOperator_TaskSkipped: a Task already attached to a running
// operator must not be reproposed.
func TestExistingOperator_TaskSkipped(t *testing.T) {
	taskID := "cmdr-eee"
	st := &fakeStore{
		projects: []store.Project{newProject(5, "busy")},
		runsByTask: map[string]*store.AgentRun{
			taskID: {ID: 99, ProjectID: 5, TaskBeadID: taskID, Status: "running"},
		},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead(taskID, "already in flight")},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"busy": false},
	}
	s := New(bd, st, sp, cfg, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	time.Sleep(150 * time.Millisecond)

	select {
	case ev := <-s.Out():
		t.Errorf("task with running operator should be skipped; got %+v", ev)
	default:
	}
	if calls := sp.Calls(); len(calls) != 0 {
		t.Errorf("running-operator task triggered spawn: %d calls", len(calls))
	}
}

// TestConfirm_RejectsWhenCapacityFilledMidWait: Confirm re-checks capacity,
// so if the workstream filled while the user thought about it, the spawn is
// rejected with a clear error.
func TestConfirm_RejectsWhenCapacityFilledMidWait(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(6, "raceful")},
		runsByTask: map[string]*store.AgentRun{},
	}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"raceful": false},
		full:      map[string]bool{"cmdr-fff": true},
	}
	s := New(&beads.MockClient{}, st, sp, cfg, time.Second)

	ev := ProposalEvent{ProjectID: 6, ProjectName: "raceful", ProjectType: "code", TaskBeadID: "cmdr-fff"}
	err := s.Confirm(context.Background(), ev)
	if err == nil {
		t.Fatalf("expected capacity rejection, got nil")
	}
	if calls := sp.Calls(); len(calls) != 0 {
		t.Errorf("Confirm must not spawn past capacity; got %d calls", len(calls))
	}
}

// TestStop_Idempotent: double-Stop must not panic; channel must close.
func TestStop_Idempotent(t *testing.T) {
	st := &fakeStore{}
	bd := &beads.MockClient{}
	sp := &fakeSpawner{}
	cfg := &fakeConfig{}
	s := New(bd, st, sp, cfg, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Stop()
	s.Stop() // must not panic

	// Out() must be closed.
	select {
	case _, ok := <-s.Out():
		if ok {
			// Drain anything left, then re-check closure.
			for range s.Out() {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Out() not closed after Stop")
	}
}

// TestSpawnFailureUnderAutoSpawn_EmitsProposal: when AutoSpawn=true but the
// underlying Spawn errors, the scheduler emits a ProposalEvent so the Lead
// view can surface the failure to the user instead of swallowing it.
func TestSpawnFailureUnderAutoSpawn_EmitsProposal(t *testing.T) {
	st := &fakeStore{
		projects:   []store.Project{newProject(7, "broken")},
		runsByTask: map[string]*store.AgentRun{},
	}
	bd := &beads.MockClient{
		ListResult: []beads.Bead{newTaskBead("cmdr-ggg", "fails to spawn")},
	}
	sp := &fakeSpawner{err: errors.New("docker offline")}
	cfg := &fakeConfig{
		autoSpawn: map[string]bool{"broken": true},
	}
	s := New(bd, st, sp, cfg, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	events := drain(t, s.Out(), 1, time.Second)
	if len(events) != 1 {
		t.Fatalf("want 1 fallback proposal, got %d", len(events))
	}
	if events[0].TaskBeadID != "cmdr-ggg" || !events[0].AutoSpawn {
		t.Errorf("fallback proposal mismatch: %+v", events[0])
	}
}
