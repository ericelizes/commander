package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type fakeSpawner func(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error)

func (f fakeSpawner) Spawn(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	return f(ctx, r)
}

func fixture(t *testing.T) (*store.Store, int64, context.Context) {
	t.Helper()
	st, _ := pgtest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	if _, e := st.SetOrchestrationControl(ctx, 1, "active", "fake-runtime execution fixture"); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "execution", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"one", "two"} {
		if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,?,?,'Do the bounded work','Evidence is recorded','["kind:task"]','now','now')`, pid, id, id); e != nil {
			t.Fatal(e)
		}
		if _, e = st.SetExecutionReady(ctx, pid, id, "commander", 1, 1, true, nil); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"vm-0", "vm-1"} {
		if e = st.ConfigureExecutionSlot(ctx, id, []string{"knowledge"}, true); e != nil {
			t.Fatal(e)
		}
	}
	return st, pid, ctx
}

func recordRun(st *store.Store, ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: r.ExecutionClaimToken, ResumeSourceRunID: r.ResumeSourceRunID, ProjectID: r.ProjectID, TaskBeadID: r.TaskBeadID, AgentName: "operator-" + r.Name, OpencodeSessionID: r.Name, SpawnTime: time.Now().Unix()})
	return &spawn.SpawnResult{RunID: rid}, e
}

func TestPostgresExecutionWorkerClaimsDirectlyAndRetainsLiveRunAcrossRestart(t *testing.T) {
	st, _, ctx := fixture(t)
	calls := 0
	adapter := fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		calls++
		if r.ExecutionClaimToken == "" || !strings.Contains(r.TaskText, "Acceptance criteria") || !strings.Contains(r.TaskText, "Planning owner: commander") {
			t.Fatal("missing intent or launch authority")
		}
		return recordRun(st, ctx, r)
	})
	w := &Worker{Store: st, Spawner: adapter, Capacity: 2}
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	restarted := &Worker{Store: st, Spawner: adapter, Capacity: 2}
	if e := restarted.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("live claim launched twice across restart: %d", calls)
	}
}

func TestPostgresExecutionSlowPreparationDoesNotBlockAnotherSlot(t *testing.T) {
	st, _, ctx := fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	adapter := fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		if strings.Contains(r.Name, "vm-0") {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return recordRun(st, ctx, r)
	})
	w := &Worker{Store: st, Spawner: adapter, Capacity: 2}
	go func() { done <- w.PassSlot(ctx, "vm-0") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first slot never reached preparation")
	}
	e := w.PassSlot(ctx, "vm-1")
	close(release)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("first preparation never finished")
	}
}

func TestPostgresExecutionLaunchFailureRetainsCauseAndBoundedRetry(t *testing.T) {
	st, pid, ctx := fixture(t)
	now := time.Now()
	calls := 0
	w := &Worker{Store: st, Capacity: 1, Now: func() time.Time { return now }, Spawner: fakeSpawner(func(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		calls++
		return nil, errors.New("required local adapter is unavailable")
	})}
	if e := w.PassSlot(ctx, "vm-0"); e == nil {
		t.Fatal("launch error discarded")
	}
	var detail string
	var attempts, due int64
	if e := st.DB().QueryRowContext(ctx, `SELECT execution_error,execution_attempts,execution_due_at FROM bead_tasks WHERE project_id=? AND id='one'`, pid).Scan(&detail, &attempts, &due); e != nil || detail != "required local adapter is unavailable" || attempts != 1 || due <= now.Unix() {
		t.Fatalf("failure not durably recoverable: %q %d %d %v", detail, attempts, due, e)
	}
	if e := w.PassSlot(ctx, "vm-0"); e == nil {
		t.Fatal("second independent bead should expose same missing adapter")
	}
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("ignored retry backoff: %d calls", calls)
	}
}
