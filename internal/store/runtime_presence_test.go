package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresRuntimePresenceExpiresAndDoesNotInferActivity(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "presence", Path: "/presence", Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	id, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-task", OpencodeSessionID: "session", SpawnTime: 1})
	if e != nil {
		t.Fatal(e)
	}
	_, _, known, e := s.RuntimePresence(ctx)
	if e != nil || known {
		t.Fatalf("missing observation is known: %v %v", known, e)
	}
	if e = s.SetAgentState(ctx, "manager-task", AgentIdle, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordRuntimePresence(ctx, map[int64]bool{id: true}, time.Now(), []string{"manager-task"}); e != nil {
		t.Fatal(e)
	}
	alive, native, known, e := s.RuntimePresence(ctx)
	if e != nil || !known || len(alive) != 1 || len(native) != 1 || native[0].State != AgentIdle {
		t.Fatalf("presence: %v %#v %v %v", alive, native, known, e)
	}
	if e = s.RecordRuntimePresence(ctx, nil, time.Now(), []string{}); e != nil {
		t.Fatal(e)
	}
	alive, native, known, e = s.RuntimePresence(ctx)
	if e != nil || known || len(alive) != 0 || len(native) != 0 {
		t.Fatalf("failed observation preserved: %v %#v %v %v", alive, native, known, e)
	}
}
