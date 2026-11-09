package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wsmgr"
)

// Entirely in-process fixture: no chat, tmux, external CLI or real store.
func TestManagerRegistrationScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/manager-registration.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Root  string
		Cases []struct {
			ID, Parent, Kind string
			Allowed          bool
		}
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", t.TempDir())
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Path: dir, Type: "code"})
	if err != nil {
		t.Fatal(err)
	}
	project := store.Project{ID: pid, Name: "sample", Path: dir}
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, c := range scenario.Cases {
		bc.Seed(beads.Bead{ID: c.ID, Status: "open", Labels: strings.Split(c.Kind, ",")})
		if c.Parent != "" {
			bc.Edges = append(bc.Edges, beads.Dep{IssueID: c.ID, DependsOnID: c.Parent, Type: "parent-child"})
		}
	}
	m := &wsmgr.Manager{Store: st, Beads: bc, Reg: &agentmail.MockRegistrar{NextName: "FixtureFox"}, AmKey: "/fixture"}
	runID, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "sample-task", AgentName: "operator-fixture", OpencodeSessionID: "fixture-session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range scenario.Cases {
		_, err := m.Prepare(ctx, wsmgr.Target{Project: project, BeadID: c.ID})
		if (err == nil) != c.Allowed {
			t.Fatalf("%s: allowed=%v err=%v", c.ID, c.Allowed, err)
		}
		t.Logf("registration %s allowed=%v", c.ID, err == nil)
	}
	// cmdr start lists every display row: the invalid ones are skipped, the
	// real root still gets its window.
	m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) {
		var rows []tui.NumberedWorkstream
		for i, c := range scenario.Cases {
			rows = append(rows, tui.NumberedWorkstream{N: i + 1, Project: "sample", BeadID: c.ID})
		}
		return rows, nil
	}
	windows, err := m.PrepareAll(ctx)
	if len(windows) != 1 || windows[0].BeadID != scenario.Root || err == nil {
		t.Fatalf("prepare all: windows=%+v err=%v", windows, err)
	}
	t.Logf("prepare-all windows=%d root=%s skipped=%d", len(windows), windows[0].BeadID, len(scenario.Cases)-1)
	// Simulate an old bad registration, then use the supported exact store path.
	if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "sample-task", SessionFile: "/fixture/session"}); err != nil {
		t.Fatal(err)
	}
	root, err := wsmgr.StrictRoot(ctx, bc, dir, "sample-task")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		removed, err := st.UnregisterNestedManager(ctx, pid, "sample-task", root, "manager-"+root)
		if err != nil || removed != (i == 0) {
			t.Fatalf("cleanup %d: %v %v", i, removed, err)
		}
		t.Logf("cleanup attempt=%d removed=%v", i+1, removed)
	}
	run, err := st.GetAgentRun(ctx, runID)
	if err != nil || run == nil || run.Status != "running" {
		t.Fatalf("operator changed: %+v %v", run, err)
	}
	rows, err := st.ListWorkstreamManagers(ctx)
	if err != nil || len(rows) != 1 || rows[0].WorkstreamBeadID != scenario.Root {
		t.Fatalf("registrations=%+v err=%v", rows, err)
	}
	b, _ := bc.Show(ctx, dir, "sample-task")
	if b == nil {
		t.Fatal("underlying task deleted")
	}
	t.Logf("retained root=%s operator_status=%s task=%s parent=%s", rows[0].WorkstreamBeadID, run.Status, b.ID, root)
}
