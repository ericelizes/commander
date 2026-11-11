package layout

import (
	"context"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
	promptassets "github.com/example-user/commander/prompts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommanderSessionRefreshesPlanningPromptAfterActivation(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	first, e := EnsureClaudeCommanderSessionID(ctx, home)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.InsertProject(ctx, store.Project{Name: "canonical-project", Path: t.TempDir(), Type: "code", Status: "active"}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(e) {
		t.Fatalf("fixture unexpectedly has a retired state file: %v", e)
	}
	projects, e := listStartupProjects(ctx)
	if e != nil || len(projects) != 1 || projects[0].Name != "canonical-project" {
		t.Fatal("canonical startup depended on retired state.db", projects, e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "planning cockpit fixture"); e != nil {
		t.Fatal(e)
	}
	second, e := EnsureClaudeCommanderSessionID(ctx, home)
	if e != nil || first != second {
		t.Fatal("prompt refresh changed conversation identity", e)
	}
	body, e := os.ReadFile(agentrt.ClaudePromptPath("commander"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(body), "cmdr plan apply") || strings.Contains(string(body), "bias toward delegation") {
		t.Fatal("existing cockpit retained the Manager orchestration prompt")
	}
	if template, e := CommanderPromptTemplate(ctx, home); e != nil || template != promptassets.RolePlanningCommander {
		t.Fatal("cockpit selected the wrong planning contract", template, e)
	}
	oldProjects, oldWindows := listStartupProjectsFn, workstreamWindowsFn
	t.Cleanup(func() { listStartupProjectsFn = oldProjects; workstreamWindowsFn = oldWindows })
	calls := 0
	listStartupProjectsFn = func(context.Context) ([]store.Project, error) { calls++; return projects, nil }
	workstreamWindowsFn = func(context.Context) ([]WorkstreamWindow, error) { calls++; return nil, nil }
	if e = (&Director{}).ensureLinkedManagerWindows(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("active cockpit tried to recreate permanent Manager processes")
	}
}
