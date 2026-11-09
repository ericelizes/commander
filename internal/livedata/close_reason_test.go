package livedata

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// A closed bead's close reason rides on its row from the list read (no extra call),
// capped; an open bead's stray reason is not an outcome.
func TestCloseReasonRidesOnClosedRowsOnly(t *testing.T) {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.1", ParentID: "w", Status: "closed", CloseReason: "  Regeneration ran; source unchanged  ", Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.2", ParentID: "w", Status: "open", CloseReason: "reopened", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.3", ParentID: "w", Status: "closed", CloseReason: strings.Repeat("x", 1000), Labels: []string{"kind:task"}})
	rows, err := projectObjectiveRows(context.Background(), m, store.Project{ID: 1, Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.BeadID] = r.CloseReason
	}
	if got["w.1"] != "Regeneration ran; source unchanged" || got["w.2"] != "" || len([]rune(got["w.3"])) != 401 {
		t.Fatalf("close reasons: %q", got)
	}
	steps := snapSteps([]*StepNode{{Row: rows[0]}, {Row: ObjectiveRow{BeadID: "w.1", Status: "closed", CloseReason: got["w.1"]}}}, "", nil, false)
	if steps[1].CloseReason != "Regeneration ran; source unchanged" {
		t.Fatalf("snapshot step lost it: %+v", steps[1])
	}
}

// Closing a step folds its tasks off the board; their outcomes (the latest few) ride on
// the closed step from the same list read, and a blocker folded away finished is marked
// done so no reader takes it for an outside wait.
func TestFoldedTaskOutcomesRideOnTheirClosedStep(t *testing.T) {
	ago := func(h int) string { return time.Now().Add(-time.Duration(h) * time.Hour).UTC().Format(time.RFC3339) }
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.1", ParentID: "w", Status: "closed", Title: "Regenerate from the spec", CloseReason: "aligned spec-first", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.1.1", ParentID: "w.1", Status: "closed", Title: "Old run", CloseReason: "first try", UpdatedAt: ago(30 * 24), Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.1.2", ParentID: "w.1", Status: "closed", Title: "Implement spec", CloseReason: "implement-spec done; source unchanged by regeneration", UpdatedAt: ago(1), Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.1.3", ParentID: "w.1", Status: "closed", Title: "Second run", CloseReason: "second try", UpdatedAt: ago(5), Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.1.4", ParentID: "w.1", Status: "closed", Title: "Third run", CloseReason: "third try", UpdatedAt: ago(3), Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.1.5", ParentID: "w.1", Status: "closed", Title: "Quiet", UpdatedAt: ago(0), Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "w.1.6", ParentID: "w.1", Status: "closed", Title: "Folded sub-step", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.2", ParentID: "w", Status: "open", Title: "Ship it", Labels: []string{"kind:objective"}})
	m.Edges = append(m.Edges, beads.Dep{IssueID: "w.2", DependsOnID: "w.1.6", Type: "blocks"}, beads.Dep{IssueID: "w.2", DependsOnID: "elsewhere", Type: "blocks"})
	rows, err := projectObjectiveRows(context.Background(), m, store.Project{ID: 1, Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ObjectiveRow{}
	for _, r := range rows {
		byID[r.BeadID] = r
	}
	if _, drawn := byID["w.1.2"]; drawn {
		t.Fatal("a task under a closed step is folded off the board")
	}
	got := byID["w.1"].FinishedTasks
	// The latest two with a reason; the third and a month-old one stay folded.
	if len(got) != 2 || got[0].BeadID != "w.1.2" || got[0].Outcome != "implement-spec done; source unchanged by regeneration" || got[1].BeadID != "w.1.4" {
		t.Fatalf("folded outcomes: %+v", got)
	}
	if d := byID["w.2"].DoneBlockers; len(d) != 1 || d[0] != "w.1.6" {
		t.Fatalf("done blockers: %v (blocked by %v)", d, byID["w.2"].BlockedBy)
	}
	steps := snapSteps([]*StepNode{{Row: byID["w.1"]}}, "", nil, false)
	if len(steps[0].FinishedTasks) != 2 {
		t.Fatalf("snapshot step lost them: %+v", steps[0])
	}
}
