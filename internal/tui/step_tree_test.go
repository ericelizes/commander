package tui

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func nest(depth int, id, title, state string) nestedObjective {
	return nestedObjective{row: ObjectiveRow{BeadID: id, Title: title, State: state, ProjectName: "p"}, depth: depth}
}

func TestStepTree_ActivePhaseExpandedOthersCollapsedInPlanOrder(t *testing.T) {
	ws := panelWS("",
		nest(1, "w.1", "Build", beads.StateInProgress),
		nest(2, "w.1.1", "U1 spike", beads.StateNeedsUser),
		nest(2, "w.1.2", "Commit plan", beads.StateDone),
		nest(2, "w.1.3", "U3 worker", beads.StateInProgress),
		nest(1, "w.2", "Go live", beads.StateNotStarted),
		nest(2, "w.2.1", "Deploy", beads.StateNotStarted),
		nest(1, "w.3", "First pilot", beads.StateNotStarted),
	)
	got := titles(rowsOf(1, ws, false, true, true))
	want := []string{"Keep pipeline healthy", "Build", "U1 spike", "U3 worker", "✓ 1 done", "Go live  · not started", "First pilot"}
	if len(got) != len(want) {
		t.Fatalf("rows = %q", got)
	}
	for i := range want {
		if stripANSI(got[i]) != want[i] {
			t.Errorf("row %d = %q, want %q (all: %q)", i, stripANSI(got[i]), want[i], got)
		}
	}
}

func TestStepTree_HiddenStepsAreNamed(t *testing.T) {
	var steps []nestedObjective
	steps = append(steps, nest(1, "w.0", "U1 now", beads.StateInProgress))
	for i := 2; i <= 9; i++ {
		steps = append(steps, nest(1, "w."+string(rune('0'+i)), "U"+string(rune('0'+i))+" later", beads.StateNotStarted))
	}
	rows := stepRows(panelWS("", steps...), false, animState{}, false)
	last := ""
	for _, r := range rows {
		if r.kind == "summary" {
			last = stripANSI(r.node.Title)
		}
	}
	if !strings.HasPrefix(last, "+3 more · ") || !strings.Contains(last, "U7–U9") {
		t.Fatalf("hidden summary = %q", last)
	}
}

func TestStepTree_OnlyTheNowStepSpinsAndOthersAreStatic(t *testing.T) {
	ws := panelWS("",
		nest(1, "w.1", "First", beads.StateInProgress),
		nest(1, "w.2", "Second", beads.StateInProgress),
	)
	anim := animState{on: true, frame: 1}
	spinning := stepRows(ws, false, anim, true)
	if m := stripANSI(spinning[0].node.Marker); m == "●" {
		t.Errorf("the now step should show a spinner frame, got %q", m)
	}
	if m := stripANSI(spinning[1].node.Marker); m != "●" {
		t.Errorf("other in-progress steps stay static, got %q", m)
	}
	idle := stepRows(ws, false, anim, false)
	if m := stripANSI(idle[0].node.Marker); m != "●" {
		t.Errorf("no live agent, no spinner: got %q", m)
	}
}

func TestStepTree_InReviewShowsPRAndClosedNeverSpins(t *testing.T) {
	ws := panelWS("", nest(1, "w.1", "Ship stack", beads.StateInProgress), nest(1, "w.2", "Old", beads.StateInProgress))
	ws.steps[0].row.Review = "4159"
	ws.steps[1].row.Status = "closed"
	rows := stepRows(ws, true, animState{on: true}, true)
	if stripANSI(rows[0].node.Marker) != "⎇" || !strings.Contains(stripANSI(rows[0].node.Title), "#4159") {
		t.Errorf("in-review row = %q %q", stripANSI(rows[0].node.Marker), stripANSI(rows[0].node.Title))
	}
	if m := stripANSI(rows[1].node.Marker); m != "✓" {
		t.Errorf("a closed bead with a stale in-progress state shows ✓, got %q", m)
	}
}

func TestReviewPR(t *testing.T) {
	if reviewPR([]string{"x"}) != "" || reviewPR([]string{"in-review", "pr:4159"}) != "4159" || reviewPR([]string{"in-review"}) != "?" {
		t.Fatal("reviewPR")
	}
}
