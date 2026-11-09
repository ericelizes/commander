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

func stateColumn(t *testing.T) *ObjectivesColumn {
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
	obj := func(id, title, status string, labels ...string) beads.Bead {
		return beads.Bead{ID: id, Title: title, Status: status, Labels: append([]string{"kind:objective"}, labels...)}
	}
	c := NewObjectivesColumn(&Services{
		Store: st,
		Beads: selectiveBeadsClient{objectives: []beads.Bead{
			obj("w-1", "Workstream", "open"),
			obj("w-1.1", "Idle step", "open"),
			obj("w-1.2", "Working step", "in_progress", "owner:operator-x"),
			obj("w-1.3", "Stuck step", "in_progress", "blocked"),
			obj("w-1.4", "Ask step", "open", "needs-user", "owner:user"),
			obj("w-1.5", "Done step", "closed", "owner:user"),
		}},
	})
	updated, _ := c.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	return updated.(*ObjectivesColumn)
}

func TestObjectivesColumn_StepStateGlyphsAndOwner(t *testing.T) {
	view := stripANSI(stateColumn(t).View())
	for _, want := range []string{
		"○  Idle step",
		"●  Working step  → operator-x",
		"✗  Stuck step",
		"⚑  Ask step  → user",
		"✓ 1 done",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in view:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Done step") {
		t.Fatalf("done steps collapse into the count:\n%s", view)
	}
}

func TestObjectivesColumn_MarkAndOwnerPrompts(t *testing.T) {
	c := stateColumn(t)
	c = press(c, "j") // workstream -> Idle step
	c = press(c, "m")
	if c.prompt != "state" {
		t.Fatalf("prompt = %q, want state", c.prompt)
	}
	if view := stripANSI(c.View()); !strings.Contains(view, "b blocked") {
		t.Fatalf("expected state menu:\n%s", view)
	}
	c = press(c, "esc", "o")
	if c.prompt != "owner" {
		t.Fatalf("prompt = %q, want owner", c.prompt)
	}
}
