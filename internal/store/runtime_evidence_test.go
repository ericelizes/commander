package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresNativeEvidenceRequiresCurrentRunAndSession(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "exact-presence", Path: t.TempDir(), Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	id, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "executor.task", OpencodeSessionID: "session", SpawnTime: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE agent_runs SET execution_driver='native' WHERE id=?`, id); e != nil {
		t.Fatal(e)
	}
	check := func(known bool) {
		t.Helper()
		rows, e := s.ObservedExecutions(ctx)
		if e != nil || len(rows) != 1 {
			t.Fatalf("observations: %+v %v", rows, e)
		}
		if rows[0].Known != known || (known && !rows[0].Alive) {
			t.Fatalf("wrong evidence: %+v", rows[0])
		}
	}
	record := func(obs ...NativeRuntimeObservation) {
		t.Helper()
		if e := s.RecordRuntimeEvidence(ctx, map[int64]bool{id: true}, time.Now(), obs); e != nil {
			t.Fatal(e)
		}
	}
	var before int64
	if e = s.DB().QueryRow(`SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&before); e != nil {
		t.Fatal(e)
	}
	check(false)
	record(NativeRuntimeObservation{Role: "executor-task", SessionID: "session", PID: 123})
	check(false) // a planning conversation cannot stand in for execution
	record(NativeRuntimeObservation{Role: "executor-task", RunID: id, SessionID: "wrong", PID: 123})
	check(false)
	record(NativeRuntimeObservation{Role: "executor-task", RunID: id, SessionID: "session", PID: 123})
	check(true)
	if e = s.RecordRuntimeEvidence(ctx, nil, time.Now(), nil); e != nil {
		t.Fatal(e)
	}
	check(true) // a disconnected daemon is not evidence of process death
	var progress int64
	if e = s.DB().QueryRow(`SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&progress); e != nil || progress != before {
		t.Fatalf("presence advanced work: %d %v", progress, e)
	}
	if _, e = s.DB().Exec(`UPDATE run_observations SET checked_at=? WHERE run_id=?`, time.Now().Add(-time.Minute).Unix(), id); e != nil {
		t.Fatal(e)
	}
	check(false)
}
