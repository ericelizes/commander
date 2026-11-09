package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestWorkstreamsInOrder_CreationOrderAcrossProjects(t *testing.T) {
	projects := []store.Project{{ID: 1, Name: "example-app"}, {ID: 2, Name: "demo"}}
	rows := map[int64][]ObjectiveRow{
		1: {
			{BeadID: "app-b", Title: "Reduce import backlog", ProjectID: 1, CreatedAt: "2026-10-02T00:00:00Z"},
			{BeadID: "app-a", Title: "Keep pipeline healthy", ProjectID: 1, CreatedAt: "2026-10-01T00:00:00Z"},
			{BeadID: "app-a.1", ParentBeadID: "app-a", Title: "step", ProjectID: 1},
		},
		2: {{BeadID: "demo-a", Title: "Import pipeline", ProjectID: 2, CreatedAt: "2026-10-01T12:00:00Z"}},
	}
	var titles []string
	for _, ws := range workstreamsInOrder(projects, func(id int64) []ObjectiveRow { return rows[id] }, nil) {
		titles = append(titles, ws.root.Title)
	}
	if got := strings.Join(titles, ","); got != "Keep pipeline healthy,Import pipeline,Reduce import backlog" {
		t.Fatalf("order = %s", got)
	}
}

func TestNumberWorkstreams(t *testing.T) {
	data := CommanderLiveData{
		Projects: []store.Project{{ID: 1, Name: "example-app"}},
		Objectives: []ObjectiveRow{
			{BeadID: "app-a", Title: "A", ProjectID: 1, CreatedAt: "1"},
			{BeadID: "app-b", Title: "B", ProjectID: 1, CreatedAt: "2"},
			{BeadID: "app-a.1", ParentBeadID: "app-a", Title: "step", ProjectID: 1},
		},
	}
	got := numberWorkstreams(data)
	if len(got) != 2 || got[1].N != 2 || got[1].BeadID != "app-b" || got[1].Project != "example-app" {
		t.Fatalf("got %+v", got)
	}
}

func TestWorkstreamsInOrder_RanksBeatCreationAndNewGoLast(t *testing.T) {
	projects := []store.Project{{ID: 1, Name: "example-app"}}
	rows := []ObjectiveRow{
		{BeadID: "a", Title: "A", ProjectID: 1, CreatedAt: "1"},
		{BeadID: "b", Title: "B", ProjectID: 1, CreatedAt: "2"},
		{BeadID: "c", Title: "C (new)", ProjectID: 1, CreatedAt: "0"},
	}
	ranks := map[store.WorkstreamKey]int{{ProjectID: 1, BeadID: "b"}: 1, {ProjectID: 1, BeadID: "a"}: 2}
	var titles []string
	for _, ws := range workstreamsInOrder(projects, func(int64) []ObjectiveRow { return rows }, ranks) {
		titles = append(titles, ws.root.Title)
	}
	if got := strings.Join(titles, ","); got != "B,A,C (new)" {
		t.Fatalf("order = %s", got)
	}
}

func TestReorderKeys(t *testing.T) {
	mk := func(ids ...string) []workstream {
		var out []workstream
		for _, id := range ids {
			out = append(out, workstream{project: store.Project{ID: 1}, root: ObjectiveRow{BeadID: id}})
		}
		return out
	}
	ids := func(keys []store.WorkstreamKey) string {
		var s []string
		for _, k := range keys {
			s = append(s, k.BeadID)
		}
		return strings.Join(s, "")
	}
	if got := ids(reorderKeys(mk("a", "b", "c", "d"), 3, 0)); got != "dabc" {
		t.Fatalf("to top = %s", got)
	}
	if got := ids(reorderKeys(mk("a", "b", "c", "d"), 0, 1)); got != "bacd" {
		t.Fatalf("down one = %s", got)
	}
	if got := ids(reorderKeys(mk("a", "b", "c", "d"), 1, 3)); got != "acdb" {
		t.Fatalf("to bottom = %s", got)
	}
}

func twoWorkstreamColumn(t *testing.T) *ObjectivesColumn {
	t.Helper()
	st := openTestStore(t)
	path := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(context.Background(), store.Project{
		Name: "manifest", Type: "code", Path: path, ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{objectives: []beads.Bead{
			{ID: "w1", Title: "First", Status: "open", CreatedAt: "1"},
			{ID: "w1.1", Title: "First step", Status: "open"},
			{ID: "w2", Title: "Second", Status: "open", CreatedAt: "2"},
		}},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	return updated.(*ObjectivesColumn)
}

func TestObjectivesColumn_JMovesWorkstreamWithStepsAndPersists(t *testing.T) {
	c := twoWorkstreamColumn(t)
	c = press(c, "J") // First (with its step) drops below Second
	view := stripANSI(c.View())
	iSecond, iFirst := strings.Index(view, "1  Second"), strings.Index(view, "2  First")
	if iSecond < 0 || iFirst < 0 || iSecond > iFirst {
		t.Fatalf("expected Second as #1 then First as #2:\n%s", view)
	}
	if !strings.Contains(view[iFirst:], "First step") {
		t.Fatalf("step should follow its workstream:\n%s", view)
	}
	if sel := c.tree.Selected(); sel == nil || !strings.Contains(sel.Title, "First") {
		t.Fatalf("selection should follow the moved workstream, got %+v", sel)
	}
	ranks, err := c.svc.Store.ListWorkstreamRanks(context.Background())
	if err != nil || len(ranks) != 2 {
		t.Fatalf("ranks not persisted: %v %v", ranks, err)
	}
	c = press(c, "K")
	if view := stripANSI(c.View()); strings.Index(view, "1  First") < 0 {
		t.Fatalf("K should move it back to #1:\n%s", view)
	}
}

func TestObjectivesColumn_ArrowsMoveWorkstreamToWorkstreamNotThroughSteps(t *testing.T) {
	c := twoWorkstreamColumn(t) // First (with a step) and Second
	if sel := c.tree.Selected(); sel == nil || !strings.Contains(sel.Title, "First") {
		t.Fatalf("starts on the first workstream: %+v", sel)
	}
	c = press(c, "j")
	if sel := c.tree.Selected(); sel == nil || !strings.Contains(sel.Title, "Second") {
		t.Fatalf("one down-press skips the step and lands on Second, got %+v", sel)
	}
	c = press(c, "K") // moves the highlighted workstream up one slot
	if view := stripANSI(c.View()); strings.Index(view, "1  Second") < 0 {
		t.Fatalf("Second should now be #1:\n%s", view)
	}
	c = press(c, "k", "k", "k")
	if sel := c.tree.Selected(); sel == nil || sel.Title != "Commander" {
		t.Fatalf("up past workstream 1 lands on row 0 (Commander) and stops there, got %+v", sel)
	}
}

func TestObjectivesColumn_StepsAreShownButNeverSelected(t *testing.T) {
	c := twoWorkstreamColumn(t)
	for i, n := range c.tree.Nodes() {
		if n.ID == commanderRowID {
			if n.Skip {
				t.Fatal("row 0 (Commander) must be selectable")
			}
			continue
		}
		isHeader := strings.Contains(n.ID, ":w1:") || strings.Contains(n.ID, ":w2:")
		if isHeader == n.Skip {
			t.Fatalf("node %d %q: header=%v skip=%v", i, n.Title, isHeader, n.Skip)
		}
	}
	if view := stripANSI(c.View()); !strings.Contains(view, "First step") {
		t.Fatalf("steps are still displayed:\n%s", view)
	}
}

func TestObjectivesColumn_GroupToggleKeepsRanksAndIsRemembered(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	c := twoWorkstreamColumn(t)
	c = press(c, "g")
	view := stripANSI(c.View())
	if !strings.Contains(view, "MANIFEST") || !strings.Contains(view, "1  First") || !strings.Contains(view, "2  Second") {
		t.Fatalf("grouped view should keep global rank numbers under a header:\n%s", view)
	}
	if !loadGroupPref() {
		t.Fatal("group choice should persist for the window")
	}
	c = press(c, "g")
	if view := stripANSI(c.View()); strings.Contains(view, "MANIFEST") {
		t.Fatalf("toggle back to flat:\n%s", view)
	}
	if loadGroupPref() {
		t.Fatal("flat choice should persist")
	}
}
