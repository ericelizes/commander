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

// Nil and empty both mean no native roles were observed. Neither may leave
// previous native evidence alive or interfere with local run observations.
func TestPostgresNativePresenceNilEmptyAndRoles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles []string
	}{{"nil", nil}, {"empty", []string{}}, {"roles", []string{"manager-trial'quoted", "operator-trial"}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := postgresTestStore(t)
			ctx := context.Background()
			const previous = "manager-previous"
			if err := s.RecordRuntimePresence(ctx, map[int64]bool{}, time.Now(), []string{previous}); err != nil {
				t.Fatal(err)
			}
			for _, role := range tc.roles {
				if err := s.SetAgentState(ctx, role, AgentIdle, "waiting"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecordRuntimePresence(ctx, map[int64]bool{}, time.Now(), tc.roles); err != nil {
				t.Fatal(err)
			}
			alive, native, known, err := s.RuntimePresence(ctx)
			if err != nil || !known || len(alive) != 0 || len(native) != len(tc.roles) {
				t.Fatalf("presence: %v %#v %v %v", alive, native, known, err)
			}
			want := map[string]bool{}
			for _, role := range tc.roles {
				want[role] = true
			}
			for _, got := range native {
				if !want[got.Role] || got.State != AgentIdle || got.Detail != "waiting" {
					t.Fatalf("native: %#v", got)
				}
				delete(want, got.Role)
			}
			if len(want) != 0 {
				t.Fatalf("missing roles: %v", want)
			}
			var stillAlive bool
			if err := s.DB().QueryRow(`SELECT alive FROM native_presence WHERE role=?`, previous).Scan(&stillAlive); err != nil || stillAlive {
				t.Fatalf("previous alive=%v: %v", stillAlive, err)
			}
		})
	}
}
