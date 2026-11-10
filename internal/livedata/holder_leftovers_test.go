package livedata

import (
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// An expired time wait (I1): a Manager handoff wait holds through its deadline and the handoff
// window after it (cmdrd wakes the Manager at the deadline); left unanswered past that it is the
// Manager's overdue move, held by nothing. Any other time wait past its time still holds until
// cmdrd's wake clears it.
func TestOverdueHandoffStopsHolding(t *testing.T) {
	due := time.Unix(1_800_000_000, 0)
	handoff := beads.Wait{Kind: beads.WaitUntil, Since: due.Add(-time.Hour), Until: due, Ref: beads.ManagerCloseWait}
	plain := beads.Wait{Kind: beads.WaitUntil, Since: due.Add(-time.Hour), Until: due, Ref: "the import finishes"}
	rows := func(w beads.Wait) []ObjectiveRow {
		return []ObjectiveRow{
			{BeadID: "w", Title: "Area", Status: "open", Owner: "manager-w"},
			{BeadID: "w.1", ParentBeadID: "w", Title: "Report in", Status: "in_progress", State: beads.StateReview, Owner: "manager-w", Waits: []beads.Wait{w}},
		}
	}
	idle := func(string) string { return "idle" }
	for _, c := range []struct {
		name   string
		w      beads.Wait
		at     time.Time
		broken bool
	}{
		{"handoff before its deadline", handoff, due.Add(-time.Minute), false},
		{"handoff inside the window", handoff, due.Add(30 * time.Minute), false},
		{"handoff unanswered past the window", handoff, due.Add(beads.HandoffWindow + time.Minute), true},
		{"plain time wait past its time", plain, due.Add(48 * time.Hour), false},
		{"no clock: never expired", handoff, time.Time{}, false},
	} {
		v := OpenBeadInvariantAt(rows(c.w), idle, c.at)
		if got := len(v) == 1 && v[0].BeadID == "w.1"; got != c.broken {
			t.Errorf("%s: violations %+v, want broken=%v", c.name, v, c.broken)
		}
	}
}

// Store vs overlay (one state): the invariant reads the agent state the board shows, so a Manager
// the chat overlay has mid-turn holds its step, as on the board's line.
func TestOverlaidAgentStateIsTheInvariants(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "w", Title: "Area", Status: "open", Owner: "manager-w"},
		{BeadID: "w.1", ParentBeadID: "w", Title: "Plan it", Status: "in_progress", State: beads.StateInProgress, Owner: "manager-w"},
	}
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []SnapWorkstream{{BeadID: "w", Title: "Area", Status: "open", ManagerRole: "manager-w", Steps: []SnapStep{
			{BeadID: "w.1", Title: "Plan it", Status: "in_progress", State: beads.StateInProgress, Owner: "manager-w"},
		}}},
	}
	markOverlaid(&s, rows, time.Time{})
	if !s.Workstreams[0].Steps[0].Broken {
		t.Fatal("an idle Manager's name holds nothing")
	}
	overlayChats(&s, map[string]ChatState{"manager-w": {State: "working"}}, map[string]SnapAgent{}, 100)
	markOverlaid(&s, rows, time.Time{})
	if s.Workstreams[0].Steps[0].Broken || len(s.Broken) != 0 {
		t.Fatalf("the overlay's working Manager holds its step: %+v", s.Broken)
	}
}
