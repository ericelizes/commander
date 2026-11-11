package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresRunHooksFenceGenericAndCommanderExecutions(t *testing.T) {
	var scenario struct{ Actors []string }
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-generic-run-hooks.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Actors) != 2 {
		t.Fatal("generic runtime scenario missing")
	}
	for _, actor := range scenario.Actors {
		t.Run(actor, func(t *testing.T) {
			st := postgresTestStore(t)
			ctx := context.Background()
			pid, e := st.InsertProject(ctx, Project{Name: "generic-hooks", Path: t.TempDir(), Type: "code", Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'task','Task','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			run := AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: actor, OpencodeSessionID: "resumed-session", SpawnTime: 1}
			first, e := st.InsertAgentRun(ctx, run)
			if e != nil {
				t.Fatal(e)
			}
			if e = st.SetAgentStateForRun(ctx, actor, first, run.OpencodeSessionID, AgentWorking, "first tool"); e != nil {
				t.Fatal(e)
			}
			if e = st.MarkClosed(ctx, first, "exited"); e != nil {
				t.Fatal(e)
			}
			run.SpawnTime = 2
			second, e := st.InsertAgentRun(ctx, run)
			if e != nil {
				t.Fatal(e)
			}
			if e = st.SetAgentStateForRun(ctx, actor, first, run.OpencodeSessionID, AgentIdle, "late old hook"); e == nil {
				t.Fatal("role spelling bypassed immutable run identity")
			}
			if e = st.SetAgentStateForRun(ctx, actor, second, "foreign-session", AgentWorking, "wrong session"); e == nil {
				t.Fatal("generic execution accepted another native session")
			}
			if e = st.SetAgentStateForSession(ctx, actor, "foreign-session", AgentWorking, ""); e == nil {
				t.Fatal("session-only hook bypassed known execution history")
			}
			states, e := st.ListAgentStates(ctx)
			if e != nil || len(states) != 1 || states[0].State != "unknown" {
				t.Fatalf("late hook changed replacement state: %+v %v", states, e)
			}
			if e = st.SetAgentStateForRun(ctx, actor, second, run.OpencodeSessionID, AgentWorking, "new tool"); e != nil {
				t.Fatal(e)
			}
			if e = st.MarkClosed(ctx, second, "exited"); e != nil {
				t.Fatal(e)
			}
			if e = st.SetAgentStateForRun(ctx, actor, second, run.OpencodeSessionID, AgentWorking, "late closed hook"); e == nil {
				t.Fatal("closed execution resumed through a hook")
			}
		})
	}
}
