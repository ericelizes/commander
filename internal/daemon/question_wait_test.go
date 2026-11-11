package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type questionNoRespawn struct{ calls int }

func (s *questionNoRespawn) Spawn(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	s.calls++
	return nil, nil
}

func TestPostgresQuestionCheckpointIsNotSettledAsACrash(t *testing.T) {
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "questions", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,'task','Task','Brief','Evidence','["kind:task"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-question", OpencodeSessionID: "session", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "task", Actor: "operator-question", RunID: rid, Session: "session", PlanRevision: 1, ClientKey: "question", Question: "Which integration?", Checkpoint: "Prepared work", Blocking: true}); e != nil {
		t.Fatal(e)
	}
	if e = st.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	run, e := st.GetAgentRun(ctx, rid)
	if e != nil {
		t.Fatal(e)
	}
	spawner := &questionNoRespawn{}
	mails := 0
	settler := runSettler{projects: st, beads: &beads.MockClient{ShowResult: &beads.Bead{ID: "task", Status: "open"}}, spawner: spawner, comments: func(context.Context, string, string) ([]beadComment, error) { return nil, nil }, mail: func(context.Context, string, string, string) error { mails++; return nil }}
	if e = settler.exited(ctx, *run); e != nil {
		t.Fatal(e)
	}
	if e = settler.diedBecause(ctx, *run, false, "session ended after its checkpoint"); e != nil {
		t.Fatal(e)
	}
	if spawner.calls != 0 || mails != 0 {
		t.Fatalf("question became crash recovery: spawns=%d mails=%d", spawner.calls, mails)
	}
	var ready bool
	if e = st.DB().QueryRowContext(ctx, `SELECT execution_ready FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&ready); e != nil || ready {
		t.Fatal("checkpoint resumed without a recorded answer", e)
	}
}
