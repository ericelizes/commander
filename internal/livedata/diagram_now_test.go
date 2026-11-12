package livedata

import (
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// reportBoard is the shape of a real workstream: a live step (w.4, in
// progress, waiting review) under a human gate chain (w.5 reviewer-b, then w.1 the
// user's greenlight, legacy approver label) with a plain step (w.10) behind
// the greenlight that carries a recorded wait on the user.
func reportBoard(t *testing.T, mut func(rows []ObjectiveRow)) *Diagram {
	t.Helper()
	now := time.Unix(1_000_000, 0)
	rows := []ObjectiveRow{
		{BeadID: "w", Title: "Nightly report", ProjectID: 1, CreatedAt: "1"},
		{BeadID: "w.3", ParentBeadID: "w", Title: "Workflow approved", ProjectID: 1, State: beads.StateDone, Status: "closed", Gate: true, Approvers: []string{"user"}},
		{BeadID: "w.4", ParentBeadID: "w", Title: "Report PR ready", ProjectID: 1, State: beads.StateBlocked, Status: "in_progress", // label blocked, bead in progress
			Review: "4170", Waits: []beads.Wait{{Kind: "review", Ref: "reviewer-b", Since: now}}},
		{BeadID: "w.5", ParentBeadID: "w", Title: "PR approved by reviewer-b", ProjectID: 1, State: beads.StateNotStarted, Gate: true,
			Approvers: []string{"reviewer-b"}, BlockedBy: []string{"w.4"}},
		{BeadID: "w.1", ParentBeadID: "w", Title: "Greenlight merging", ProjectID: 1, State: beads.StateNotStarted, Gate: true,
			Approvers: []string{"user"}, BlockedBy: []string{"w.3", "w.5"}},
		{BeadID: "w.10", ParentBeadID: "w", Title: "Merge approved PR", ProjectID: 1, State: beads.StateNotStarted,
			BlockedBy: []string{"w.1"}, Waits: []beads.Wait{{Kind: "user", Since: now}}},
	}
	if mut != nil {
		mut(rows)
	}
	snap := BuildSnapshot(Data{Projects: []store.Project{{ID: 1, Name: "p1"}}, Objectives: rows, LoadedAt: now}, nil)
	d := Board(snap, now.Unix())
	if len(d.Workstreams) == 0 || d.Workstreams[0].Diagram == nil {
		t.Fatalf("no diagram: %+v", d.Workstreams)
	}
	return d.Workstreams[0].Diagram
}

func TestDiagramNow_LiveStepIsCurrent_NeverAnUnreachedGate(t *testing.T) {
	dg := reportBoard(t, nil)
	if dg.Current != "w.4" {
		t.Errorf("current = %q, want the live step w.4", dg.Current)
	}
	if s := diagramStep(dg, "w.1"); s == nil || s.Text != "Not reached" {
		t.Errorf("greenlight = %+v, want Not reached", s)
	}
}

func TestDiagramNow_StepBehindOpenGateIsNotNeedsYou(t *testing.T) {
	dg := reportBoard(t, nil)
	s := diagramStep(dg, "w.10")
	if s == nil || s.Kind == "needs" || s.Text == "Needs you" {
		t.Errorf("w.10 = %+v, want Not started, not Needs you", s)
	}
}

func TestDiagramNow_ReadyUserGateIsCurrentAndNeedsYou(t *testing.T) {
	dg := reportBoard(t, func(rows []ObjectiveRow) {
		for i := range rows {
			if rows[i].BeadID == "w.4" || rows[i].BeadID == "w.5" {
				rows[i].State, rows[i].Status, rows[i].Waits = beads.StateDone, "closed", nil
			}
			if rows[i].BeadID == "w.1" {
				rows[i].State, rows[i].Ask = beads.StateNeedsUser, "Approve the merge"
				rows[i].Decision = &store.Decision{Question: "Approve the merge", Options: []store.DecisionOption{{Label: "Approve", Approves: true}}}
			}
		}
	})
	if dg.Current != "w.1" {
		t.Errorf("current = %q, want the ready greenlight w.1", dg.Current)
	}
	if s := diagramStep(dg, "w.1"); s == nil || s.Kind != "needs" {
		t.Errorf("w.1 = %+v, want needs", s)
	}
}

// A card behind an open gate is not the user's move yet: it waits on the gate.
func TestDiagramNow_FlaggedStepBehindGateWaitsOnIt(t *testing.T) {
	dg := reportBoard(t, func(rows []ObjectiveRow) {
		for i := range rows {
			if rows[i].BeadID == "w.10" {
				rows[i].State, rows[i].Waits, rows[i].Ask = beads.StateNeedsUser, nil, "Pick one"
				rows[i].Decision = &store.Decision{Question: "Pick one", Options: []store.DecisionOption{{Label: "A", Recommended: true}}}
			}
		}
	})
	if s := diagramStep(dg, "w.10"); s == nil || s.Kind != "waiting" || s.Text != "Waiting on greenlight merging" {
		t.Errorf("w.10 = %+v, want waiting on the gate", s)
	}
}
