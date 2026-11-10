package daemon

import (
	"context"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostgresSurvivingOperatorOutputUsesCurrentRun(t *testing.T) {
	s, _ := pgtest.Open(t)
	ctx := context.Background()
	dir := t.TempDir()
	pid, e := s.InsertProject(ctx, store.Project{Name: "fixture", Path: dir, Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	run := store.AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-task", OpencodeSessionID: "session", Status: "running", SpawnTime: time.Now().Unix()}
	run.ID, e = s.InsertAgentRun(ctx, run)
	if e != nil {
		t.Fatal(e)
	}
	path := spawn.RunOutputPath(run, dir)
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	raw := []byte("{\"type\":\"assistant\",\"session_id\":\"session\",\"message\":{\"id\":\"msg\",\"content\":[{\"type\":\"text\",\"text\":\"Reviewing the result\"}]}}\n")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	d := &Daemon{store: s, home: t.TempDir()}
	d.readLegacyOperatorOutput(ctx, run, dir)
	states, e := s.ListAgentStates(ctx)
	if e != nil || len(states) != 1 || states[0].State != store.AgentWorking {
		t.Fatalf("activity=%#v %v", states, e)
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("{\"type\":\"result\",\"session_id\":\"session\",\"subtype\":\"success\",\"result\":\"Done reviewing\"}\n")
	f.Close()
	d.readLegacyOperatorOutput(ctx, run, dir)
	states, e = s.ListAgentStates(ctx)
	if e != nil || states[0].State != store.AgentIdle {
		t.Fatalf("stop=%#v %v", states, e)
	}
	if e = s.MarkClosed(ctx, run.ID, "exited"); e != nil {
		t.Fatal(e)
	}
	next := run
	next.OpencodeSessionID = "replacement"
	next.ID, e = s.InsertAgentRun(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	d.readLegacyOperatorOutput(ctx, run, dir)
	states, e = s.ListAgentStates(ctx)
	if e != nil || states[0].State != "unknown" {
		t.Fatalf("old run overwrote replacement=%#v %v", states, e)
	}
}
