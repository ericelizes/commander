package evals

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

// An explicit acceptance run, separate from shared-runner unit tests. It uses
// 1,000 synthetic executions, not 1,000 paid model processes.
func TestPostgresThousandAgentLoad(t *testing.T) {
	if os.Getenv("COMMANDER_POSTGRES_LOAD_TEST") != "1" {
		t.Skip("explicit load acceptance run")
	}
	s, dsn := pgtest.Open(t)
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pid, e := s.InsertProject(ctx, store.Project{Name: "load", Type: "code", Path: dir, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	alive := make([]string, 1000)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	e = s.WithTx(ctx, func(q store.Querier) error {
		for i := 0; i < 50; i++ {
			_, e := q.ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,?,?,'["kind:objective","kind:workstream"]',?,?)`, pid, fmt.Sprintf("ws-%d", i), fmt.Sprintf("Workstream %d", i), created, created)
			if e != nil {
				return e
			}
		}
		for i := 0; i < 1000; i++ {
			id, root := fmt.Sprintf("task-%d", i), fmt.Sprintf("ws-%d", i/20)
			role := fmt.Sprintf("operator-task-%d", i)
			alive[i] = role
			labels := fmt.Sprintf(`["kind:objective","owner:%s"]`, role)
			if _, e := q.ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,status,labels,parent_id,created_at,updated_at) VALUES(?,?,?,'in_progress',?::jsonb,?,?,?)`, pid, id, fmt.Sprintf("Task %d", i), labels, root, created, created); e != nil {
				return e
			}
			if _, e := q.ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,?,?,'parent-child')`, pid, id, root); e != nil {
				return e
			}
			if _, e := q.ExecContext(ctx, `INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(?,?,?,?,?,'running')`, pid, id, role, fmt.Sprintf("session-%d", i), time.Now().Unix()); e != nil {
				return e
			}
			if _, e := q.ExecContext(ctx, `INSERT INTO agent_state(role,state,detail,since,updated) VALUES(?,'working','',?,?)`, role, time.Now().Unix(), time.Now().Unix()); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	runs, e := s.ListRunningRuns(ctx)
	if e != nil {
		t.Fatal(e)
	}
	observations := map[int64]bool{}
	for _, run := range runs {
		observations[run.ID] = true
	}
	if e = s.RecordRuntimePresence(ctx, observations, time.Now(), []string{}); e != nil {
		t.Fatal(e)
	}
	source := &livedata.Source{Deps: livedata.Deps{Store: s, Beads: beads.New()}, Live: func() ([]string, time.Time, bool) { return alive, time.Now(), true }}
	warm := source.Snapshot(ctx)
	if warm.Degraded != nil || len(warm.Executions) != 1000 {
		t.Fatalf("fixture: executions=%d degraded=%#v", len(warm.Executions), warm.Degraded)
	}
	var writes atomic.Int64
	writeErrors := make(chan error, 1)
	var wg sync.WaitGroup
	runCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		i := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-tick.C:
				if e := s.SetAgentState(runCtx, alive[i%1000], store.AgentWorking, fmt.Sprintf("event %d", i)); e != nil {
					if runCtx.Err() == nil {
						writeErrors <- e
					}
					return
				}
				writes.Add(1)
				i++
			}
		}
	}()
	var samples []time.Duration
	for runCtx.Err() == nil {
		start := time.Now()
		snap := source.Snapshot(ctx)
		elapsed := time.Since(start)
		if elapsed > 200*time.Millisecond {
			t.Logf("slow read=%s phases=%s", elapsed, snap.Phases.String())
		}
		if snap.Degraded != nil {
			t.Fatalf("load read degraded: %#v", snap.Degraded)
		}
		if len(snap.Executions) != 1000 {
			t.Fatalf("lost executions: %d", len(snap.Executions))
		}
		samples = append(samples, elapsed)
		time.Sleep(25 * time.Millisecond)
	}
	wg.Wait()
	select {
	case e := <-writeErrors:
		t.Fatal(e)
	default:
	}
	if len(samples) < 50 {
		t.Fatalf("insufficient reads: %d", len(samples))
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[(len(samples)-1)*95/100]
	t.Logf("1000 synthetic agents; committed runtime events=%d over 20s; reads=%d; p50=%s p95=%s max=%s", writes.Load(), len(samples), samples[len(samples)/2], p95, samples[len(samples)-1])
	if writes.Load() < 1800 {
		t.Fatalf("writer did not sustain target rate: %d events", writes.Load())
	}
	if p95 > 200*time.Millisecond {
		t.Fatalf("p95 read target exceeded: %s", p95)
	}
}
