package livedata

import (
	"reflect"
	"testing"
)

// A plan: approve (approved) -> build (done) -> test, with a container whose
// child sits under it; next gate (open) -> ship. A sibling not behind the
// gate is outside it.
func coverSnapshot() Snapshot {
	return Snapshot{Workstreams: []SnapWorkstream{{
		Project: "p", BeadID: "ws",
		Steps: []SnapStep{
			{BeadID: "ws.g1", Gate: true, Approvers: []string{"user"}, Status: "closed"},
			{BeadID: "ws.build", Status: "closed", BlockedBy: []string{"ws.g1"}},
			{BeadID: "ws.test", Status: "open", BlockedBy: []string{"ws.build"}},
			{BeadID: "ws.track", Status: "open", BlockedBy: []string{"ws.g1"}, Children: []SnapStep{
				{BeadID: "ws.track.1", Status: "open"},
			}},
			{BeadID: "ws.g2", Gate: true, Approvers: []string{"user"}, Status: "open", BlockedBy: []string{"ws.test"}},
			{BeadID: "ws.ship", Status: "open", BlockedBy: []string{"ws.g2"}},
			{BeadID: "ws.side", Status: "open"},
		},
	}}}
}

func TestCoveringApprovals_ThroughDoneWorkAndIntoChildren(t *testing.T) {
	s := coverSnapshot()
	for _, step := range []string{"ws.test", "ws.track", "ws.track.1"} {
		if got := CoveringApprovals(s, step); !reflect.DeepEqual(got, []string{"ws.g1"}) {
			t.Errorf("%s: covering = %v, want [ws.g1]", step, got)
		}
	}
}

func TestCoveringApprovals_StopsAtTheNextGate(t *testing.T) {
	s := coverSnapshot()
	for _, step := range []string{"ws.ship", "ws.side", "ws.g2"} {
		if got := CoveringApprovals(s, step); len(got) != 0 {
			t.Errorf("%s: covering = %v, want none", step, got)
		}
	}
	if got := UserGatesOver(s, "ws.ship"); !reflect.DeepEqual(got, []GateOver{{Gate: "ws.g2"}}) {
		t.Errorf("ws.ship: gates over = %v, want the open next gate", got)
	}
}

func TestCoveringApprovals_ParkedOrRetiredGateCoversNothing(t *testing.T) {
	s := coverSnapshot()
	s.Workstreams[0].Steps[0].Superseded = true
	if got := CoveringApprovals(s, "ws.test"); len(got) != 0 {
		t.Errorf("superseded gate: covering = %v, want none", got)
	}
	s = coverSnapshot()
	s.Workstreams[0].Steps[0].Status = statusDeferred
	if got := CoveringApprovals(s, "ws.test"); len(got) != 0 {
		t.Errorf("parked gate: covering = %v, want none", got)
	}
}

func TestTicketLabels(t *testing.T) {
	got := TicketLabels([]string{"kind:objective", "ticket:TEAM-4321", "ticket:team-9", "ticket:"})
	if len(got) != 2 || got[0] != "TEAM-4321" || got[1] != "TEAM-9" {
		t.Fatalf("got %v", got)
	}
}
