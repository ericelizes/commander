package store

import (
	"context"
	"testing"
)

func TestPostgresClassificationRejectsSupersededEvidenceAndBoundsUsage(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "classification", Type: "code", Path: "/classification", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'ws-1','Workstream','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if e = s.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "ws-1", AmRole: "manager-ws-1", SessionFile: "session"}); e != nil {
		t.Fatal(e)
	}
	if e = s.ObserveClassification(ctx, "manager-ws-1", "session-a", 10, "Waiting for review", true); e != nil {
		t.Fatal(e)
	}
	if e = s.ObserveClassification(ctx, "manager-ws-1", "session-a", 10, "Duplicate replay", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE classification_jobs SET updated_at=0`); e != nil {
		t.Fatal(e)
	}
	job, e := s.LeaseClassification(ctx)
	if e != nil || job == nil || job.SourceSeq != 10 {
		t.Fatalf("job=%#v err=%v", job, e)
	}
	if e = s.ObserveClassification(ctx, "manager-ws-1", "session-b", 11, "New session working", true); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteClassification(ctx, *job, "waiting_for_review", "Old result"); e != nil {
		t.Fatal(e)
	}
	var text string
	if e = s.DB().QueryRow(`SELECT status_text FROM bead_tasks WHERE id='ws-1'`).Scan(&text); e != nil || text != "" {
		t.Fatalf("old result applied: %s %v", text, e)
	}
	allowed, e := s.ReserveClassification(ctx, "attempt-1", "codex", "codex", "gpt-6-luna", 32768)
	if e != nil || !allowed {
		t.Fatalf("reserve: %v %v", allowed, e)
	}
	if allowed, e = s.ReserveClassification(ctx, "attempt-2", "codex", "codex", "gpt-6-luna", 32768); e != nil || allowed {
		t.Fatalf("budget exceeded: %v %v", allowed, e)
	}
	if e = s.SettleClassificationUsage(ctx, "attempt-1", 100, 10, 0, "ok"); e != nil {
		t.Fatal(e)
	}
	if allowed, e = s.ReserveClassification(ctx, "attempt-2", "codex", "codex", "gpt-6-luna", 33000); e != nil || !allowed {
		t.Fatalf("usage settlement: %v %v", allowed, e)
	}
}

func TestPostgresClaimPreventsDuplicateDispatchAndLateHooks(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "claims", Type: "code", Path: "/claims", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task-1','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	r := AgentRun{ProjectID: pid, TaskBeadID: "task-1", AgentName: "operator-task-1", OpencodeSessionID: "first", SpawnTime: 1}
	first, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.InsertAgentRun(ctx, r); e == nil {
		t.Fatal("duplicate live claim accepted")
	}
	if e = s.MarkClosed(ctx, first, "closed"); e != nil {
		t.Fatal(e)
	}
	r.OpencodeSessionID = "second"
	r.SpawnTime = 2
	if _, e = s.InsertAgentRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e = s.SetAgentStateForSession(ctx, r.AgentName, "first", AgentIdle, ""); e == nil {
		t.Fatal("late hook updated a replacement")
	}
	if e = s.SetAgentStateForSession(ctx, r.AgentName, "second", AgentWorking, "tests"); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresReplacementRunInvalidatesActivityAndSummaryEvenWhenSessionIsResumed(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "replacement", Path: "/replacement", Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	r := AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-task", OpencodeSessionID: "resumed-session", SpawnTime: 1}
	first, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetAgentStateForRun(ctx, r.AgentName, first, r.OpencodeSessionID, AgentWorking, "old tool"); e != nil {
		t.Fatal(e)
	}
	if e = s.ObserveClassification(ctx, r.AgentName, r.OpencodeSessionID, 1, "Old result", true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE classification_jobs SET updated_at=0`); e != nil {
		t.Fatal(e)
	}
	job, e := s.LeaseClassification(ctx)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if e = s.CompleteClassification(ctx, *job, "working", "Old summary"); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, first, "exited"); e != nil {
		t.Fatal(e)
	}
	r.SpawnTime = 2
	second, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteClassification(ctx, *job, "working", "Late old summary"); e != nil {
		t.Fatal(e)
	}
	var summary string
	if e = s.DB().QueryRow(`SELECT status_text FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&summary); e != nil || summary != "" {
		t.Fatalf("old summary survived: %q %v", summary, e)
	}
	states, e := s.ListAgentStates(ctx)
	if e != nil || len(states) != 1 || states[0].State != "unknown" {
		t.Fatalf("old activity survived: %#v %v", states, e)
	}
	if e = s.SetAgentStateForRun(ctx, r.AgentName, first, r.OpencodeSessionID, AgentIdle, ""); e == nil {
		t.Fatal("late resumed-session hook accepted")
	}
	if e = s.SetAgentStateForRun(ctx, r.AgentName, second, r.OpencodeSessionID, AgentWorking, "new tool"); e != nil {
		t.Fatal(e)
	}
}

func TestClassificationCapturedRunRejectsReusedSessionAndGenericRole(t *testing.T) {
	for _, role := range []string{"executor-generic", "manager-task"} {
		t.Run(role, func(t *testing.T) {
			s := postgresTestStore(t)
			ctx := context.Background()
			pid, e := s.InsertProject(ctx, Project{Name: "captured", Type: "code", Path: t.TempDir(), Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			r := AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: role, OpencodeSessionID: "resumed", SpawnTime: 1}
			first, e := s.InsertAgentRun(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.ObserveClassificationForRun(ctx, role, first, "resumed", 10, "First run evidence", true); e != nil {
				t.Fatal(e)
			}
			if _, e = s.DB().Exec(`UPDATE classification_jobs SET updated_at=0`); e != nil {
				t.Fatal(e)
			}
			old, e := s.LeaseClassification(ctx)
			if e != nil || old == nil || old.RunID != first {
				t.Fatalf("lost execution origin: %+v %v", old, e)
			}
			if e = s.MarkClosed(ctx, first, "exited"); e != nil {
				t.Fatal(e)
			}
			r.SpawnTime = 2
			second, e := s.InsertAgentRun(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.ObserveClassificationForRun(ctx, role, second, "resumed", 20, "New run evidence", true); e != nil {
				t.Fatal(e)
			}
			if e = s.ObserveClassificationForRun(ctx, role, first, "resumed", 1000, "Late old evidence", true); e != nil {
				t.Fatal(e)
			}
			if e = s.ObserveClassificationForRun(ctx, role, 0, "resumed", 1001, "Role-only evidence", true); e != nil {
				t.Fatal(e)
			}
			if e = s.CompleteClassification(ctx, *old, "working", "Late old summary"); e != nil {
				t.Fatal(e)
			}
			var rid, seq int64
			var evidence, summary string
			if e = s.DB().QueryRow(`SELECT run_id,source_seq,evidence FROM agent_sessions WHERE role=?`, role).Scan(&rid, &seq, &evidence); e != nil || rid != second || seq != 20 || evidence != "New run evidence" {
				t.Fatalf("late event corrupted cursor: %d %d %q %v", rid, seq, evidence, e)
			}
			if e = s.DB().QueryRow(`SELECT status_text FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&summary); e != nil || summary != "" {
				t.Fatalf("old result published: %q %v", summary, e)
			}
			if _, e = s.DB().Exec(`UPDATE classification_jobs SET updated_at=0`); e != nil {
				t.Fatal(e)
			}
			current, e := s.LeaseClassification(ctx)
			if e != nil || current == nil || current.RunID != second {
				t.Fatal("new job vanished", current, e)
			}
			var before, after int64
			if e = s.DB().QueryRow(`SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&before); e != nil {
				t.Fatal(e)
			}
			if e = s.CompleteClassification(ctx, *current, "working", "Current run summary"); e != nil {
				t.Fatal(e)
			}
			if e = s.DB().QueryRow(`SELECT progress_seq,status_text FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&after, &summary); e != nil || before != after || summary != "Current run summary" {
				t.Fatalf("interpretation changed progress or lost current summary: %d %d %q %v", before, after, summary, e)
			}
		})
	}
}
