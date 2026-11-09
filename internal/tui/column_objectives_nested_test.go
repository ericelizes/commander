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

func nestedObjectivesColumn(t *testing.T) *ObjectivesColumn {
	t.Helper()
	st := openTestStore(t)
	projectPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := st.InsertProject(context.Background(), store.Project{
		Name: "manifest", Type: "code", Path: projectPath, ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{objectives: []beads.Bead{
			{ID: "obj-1.1", Title: "Child A", Status: "open"},
			{ID: "obj-2", Title: "Other root", Status: "open"},
			{ID: "obj-1", Title: "Workstream", Status: "open"},
			{ID: "obj-9.1", Title: "Orphan", Status: "open"}, // parent not loaded: stays a root
		}},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(*ObjectivesColumn)
}

func press(c *ObjectivesColumn, keys ...string) *ObjectivesColumn {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		updated, _ := c.Update(msg)
		c = updated.(*ObjectivesColumn)
	}
	return c
}

func TestObjectivesColumn_NestsChildrenUnderParent(t *testing.T) {
	c := nestedObjectivesColumn(t)
	var order []string
	depth := map[string]int{}
	for _, n := range c.tree.Nodes() {
		if m, ok := c.nodeMeta[n.ID]; ok && m.kind == "objective" {
			order = append(order, m.objective.Title)
			depth[m.objective.Title] = n.Depth
		}
	}
	if got := strings.Join(order, ","); got != "Workstream,Child A,Other root,Orphan" {
		t.Fatalf("order = %s", got)
	}
	_ = depth
}

func TestObjectivesColumn_RenamePromptPrefillsTitle(t *testing.T) {
	c := nestedObjectivesColumn(t)
	c = press(c, "r") // first row is the first workstream
	if c.prompt != "rename" {
		t.Fatalf("prompt = %q, want rename", c.prompt)
	}
	if c.input.Value() != c.promptTarget.objective.Title {
		t.Fatalf("input = %q, want %q", c.input.Value(), c.promptTarget.objective.Title)
	}
	c = press(c, "esc")
	if c.prompt != "" {
		t.Fatalf("esc should cancel prompt")
	}
}

func TestObjectivesColumn_CloseNeedsConfirm(t *testing.T) {
	c := nestedObjectivesColumn(t)
	c = press(c, "x")
	if c.prompt != "close" {
		t.Fatalf("prompt = %q, want close", c.prompt)
	}
	if view := stripANSI(c.View()); !strings.Contains(view, "mark \"Workstream\" done? y/n") {
		t.Fatalf("expected close confirm in view:\n%s", view)
	}
	c = press(c, "n")
	if c.prompt != "" {
		t.Fatalf("n should cancel close")
	}
}

func TestObjectivesColumn_ChildPromptShowsParent(t *testing.T) {
	c := nestedObjectivesColumn(t)
	c = press(c, "n")
	if c.prompt != "child" {
		t.Fatalf("prompt = %q, want child", c.prompt)
	}
	if view := stripANSI(c.View()); !strings.Contains(view, "new step under \"Workstream\"") {
		t.Fatalf("expected child prompt in view:\n%s", view)
	}
}

func TestObjectivesColumn_WorkstreamHeadersAreNumberedWithoutArrow(t *testing.T) {
	c := nestedObjectivesColumn(t)
	view := stripANSI(c.View())
	if strings.Contains(view, "▶") {
		t.Errorf("the rank number replaces the arrow:\n%s", view)
	}
	for _, want := range []string{"1  Workstream", "2  Other root", "3  Orphan"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in:\n%s", want, view)
		}
	}
}

func TestObjectivesColumn_StepsListInCreationOrder(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "w", Title: "W"},
		{BeadID: "w.2", ParentBeadID: "w", Title: "second"},
		{BeadID: "w.10", ParentBeadID: "w", Title: "tenth"},
		{BeadID: "w.1", ParentBeadID: "w", Title: "first"},
	}
	var got []string
	for _, n := range nestObjectives(rows) {
		got = append(got, n.row.Title)
	}
	if strings.Join(got, ",") != "W,first,second,tenth" {
		t.Fatalf("got %v", got)
	}
}

func TestObjectivesColumn_StarStartedAndDoneRender(t *testing.T) {
	st := openTestStore(t)
	projectPath := filepath.Join(t.TempDir(), "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := st.InsertProject(context.Background(), store.Project{
		Name: "manifest", Type: "code", Path: projectPath, ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{objectives: []beads.Bead{
			{ID: "w", Title: "Workstream", Status: "open"},
			{ID: "w.1", Title: "Approved", Status: "closed"},
			{ID: "w.2", Title: "Building", Status: "in_progress"},
			{ID: "w.3", Title: "Review", Status: "open", Labels: []string{"kind:objective", "star"}},
			{ID: "gone", Title: "Finished workstream", Status: "closed"},
		}},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := stripANSI(updated.(*ObjectivesColumn).View())

	for _, want := range []string{"✓ 1 done", "●  Building", "○  ★ Review"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in view:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Approved") {
		t.Errorf("done steps collapse into one line:\n%s", view)
	}
	if strings.Contains(view, "Finished workstream") {
		t.Errorf("closed workstream should be hidden:\n%s", view)
	}
}
