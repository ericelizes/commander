package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func row(id string, gate bool, status string, viz int, blockedBy ...string) *StepNode {
	return &StepNode{Row: ObjectiveRow{BeadID: id, Gate: gate, Status: status, Viz: viz, BlockedBy: blockedBy}}
}

// A gate is level 1, a stage (tied to a gate by a blocks edge) 2, history and
// tasks 3; a viz label wins over all of it.
func TestEffectiveVizAndStages(t *testing.T) {
	nodes := []*StepNode{
		row("w.1", false, "closed", 0),        // a finished spike: history
		row("w.2", true, "closed", 0),         // gate
		row("w.3", false, "open", 0, "w.2"),   // waits on the gate: stage
		row("w.4", false, "open", 0, "w.3"),   // waits on a stage: stage
		row("w.5", false, "open", 3, "w.3"),   // stage by shape, pinned to 3
		row("w.6", false, "closed", 0, "w.3"), // closed, tied only through an open stage: history
	}
	stages := stageIDs(nodes)
	want := map[string]int{"w.1": 3, "w.2": 1, "w.3": 2, "w.4": 2, "w.5": 3, "w.6": 3}
	for _, n := range nodes {
		if got := EffectiveViz(n.Row, stages[n.Row.BeadID]); got != want[n.Row.BeadID] {
			t.Errorf("%s viz = %d, want %d", n.Row.BeadID, got, want[n.Row.BeadID])
		}
	}
	if got := EffectiveViz(ObjectiveRow{Gate: true, Viz: 2}, true); got != 2 {
		t.Errorf("a pinned gate = %d, want 2", got)
	}
}

func TestSupersededBead(t *testing.T) {
	for name, tc := range map[string]struct {
		b    beads.Bead
		want bool
	}{
		"label":       {beads.Bead{Labels: []string{"gate", "superseded"}}, true},
		"retired":     {beads.Bead{Labels: []string{"retired"}}, true},
		"closed why":  {beads.Bead{Status: "closed", CloseReason: "Superseded by import pruning"}, true},
		"done":        {beads.Bead{Status: "closed", CloseReason: "done"}, false},
		"open reason": {beads.Bead{Status: "open", CloseReason: "superseded"}, false},
	} {
		if got := supersededBead(tc.b); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

// A track is a stage of its workstream, with or without a blocks edge.
func TestTrackIsAStage(t *testing.T) {
	track := row("w.7", false, "open", 0)
	track.Row.Track = true
	stages := stageIDs([]*StepNode{row("w.1", false, "open", 0), track})
	if !stages["w.7"] || stages["w.1"] || EffectiveViz(track.Row, stages["w.7"]) != 2 {
		t.Fatalf("stages = %v", stages)
	}
}

func TestStageIDs_SupersededGateDoesNotAnchor(t *testing.T) {
	oldGate := row("w.9", true, "closed", 0, "w.4")
	oldGate.Row.Superseded = true
	nodes := []*StepNode{
		oldGate,
		row("w.4", false, "open", 0),
		row("w.3", false, "open", 0, "w.4"),
		row("w.15", false, "open", 0, "w.3"),
		row("w.16", false, "open", 0, "w.15"),
		row("w.20", true, "open", 0),
		row("w.21", false, "open", 0, "w.20"),
	}
	stages := stageIDs(nodes)
	for _, n := range nodes[:5] {
		if stages[n.Row.BeadID] {
			t.Errorf("superseded gate anchored %s as a stage", n.Row.BeadID)
		}
		if !n.Row.Gate && EffectiveViz(n.Row, stages[n.Row.BeadID]) != 3 {
			t.Errorf("%s should remain a task", n.Row.BeadID)
		}
	}
	if !stages["w.20"] || !stages["w.21"] {
		t.Errorf("live gate and its stage lost: %v", stages)
	}
	// Filtering is local: callers still own the full plan.
	if len(nodes) != 7 || nodes[0] != oldGate {
		t.Fatal("stageIDs changed its input")
	}
}
