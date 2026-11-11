package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestExistingCommanderReceivesCurrentPlanningProtocolWithoutAnotherTurn(t *testing.T) {
	useInbox(t)
	st, dsn := pgtest.Open(t)
	home := appCommanderHome()
	if err := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "protocol", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetOrchestrationControl(ctx, 1, "active", "protocol fixture"); err != nil {
		t.Fatal(err)
	}
	out, code := runHook(t, "", "--role", "commander", "--event", "UserPromptSubmit")
	var message struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err = json.Unmarshal([]byte(out), &message); err != nil || code != 0 || !strings.Contains(message.Hook.Context, "[current orchestration]") || !strings.Contains(message.Hook.Context, "--acceptance") {
		t.Fatal("existing chat did not learn its current interface", code, out, err)
	}
	t.Setenv("COMMANDER_RUN_ID", "17")
	if got := planningContext("commander"); got != "" {
		t.Fatal("executor inherited planning protocol", got)
	}
	t.Setenv("COMMANDER_RUN_ID", "")
	if got := planningContext("manager-retired"); got != "" {
		t.Fatal("unowned legacy role received planning context", got)
	}
	if _, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'work','Bound work','now','now');`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO agent_runs(id,project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(17,?,'work','commander','exact-execution',1,'running')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`UPDATE bead_tasks SET active_run_id=17 WHERE project_id=? AND id='work'`, pid); err != nil {
		t.Fatal(err)
	}
	if got := planningContext("commander"); got != "" {
		t.Fatal("run-bound Commander role borrowed logical planning protocol", got)
	}
	if _, err = st.SetOrchestrationControl(ctx, 2, "shadow", "fixture inactive"); err != nil {
		t.Fatal(err)
	}
	if got := planningContext("commander"); got != "" {
		t.Fatal("shadow claimed active execution", got)
	}
}
