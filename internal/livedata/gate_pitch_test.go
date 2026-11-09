package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestAGateWithNoPitchGetsNoMadeUpCard(t *testing.T) {
	ws := Workstream{Project: store.Project{Name: "p"}, Root: ObjectiveRow{Title: "W"}}
	n := gateNeedsYou(ws, ObjectiveRow{BeadID: "p-1.2", Title: "Greenlight merging the notice"}, 3)
	if n.Decision != nil {
		t.Fatalf("no pitch written must not become a card to approve: %+v", n.Decision)
	}
	if n.Ask != "Greenlight merging the notice?" {
		t.Fatalf("a greenlight is asked as itself, not 'Approve: Greenlight': %q", n.Ask)
	}
	n = gateNeedsYou(ws, ObjectiveRow{BeadID: "p-1.3", Title: "Workflow approved", GateNote: "**In short**\n\nWhy."}, 3)
	if n.Decision == nil || n.Ask != "Approve: Workflow approved?" {
		t.Fatalf("a written pitch keeps its card: %+v", n)
	}
}
