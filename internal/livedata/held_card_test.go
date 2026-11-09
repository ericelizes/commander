package livedata

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func heldBoard(t *testing.T, cardText string, noPitch bool) (Display, WorkflowStep) {
	t.Helper()
	return heldBoardOf(t, cardText, noPitch, gateStep("w.1.2", "PR #4116 confirmed done", []string{"user"}))
}

func heldBoardOf(t *testing.T, cardText string, noPitch bool, gate SnapStep) (Display, WorkflowStep) {
	t.Helper()
	track := SnapStep{BeadID: "w.1", Title: "Track", Status: "open", State: "needs-user", Track: true, Children: []SnapStep{gate}}
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{track}}
	card := &store.Decision{Question: cardText, Options: []store.DecisionOption{{Label: "Yes"}}}
	snap := snapOf(ws, idle, NeedsYou{Project: "p", BeadID: "w.1", Workstream: "WS w", Decision: card})
	if noPitch {
		snap.NoPitch = []NoPitch{{Project: "p", BeadID: gate.BeadID, Title: gate.Title, Gate: true, AskedAt: nowSec - 60}}
	}
	d := Board(snap, nowSec)
	var found WorkflowStep
	var walk func([]WorkflowStep)
	walk = func(ss []WorkflowStep) {
		for _, s := range ss {
			if s.ID == gate.BeadID {
				found = s
			}
			walk(s.Children)
		}
	}
	if d.Workstreams[0].Workflow != nil {
		walk(d.Workstreams[0].Workflow.Steps)
	}
	return d, found
}

func TestHeldCard_GateReadsNeedsYouWhenTheTrackHoldsItsCard(t *testing.T) {
	_, g := heldBoard(t, "Is PR #4116 confirmed done?", true)
	if g.Text != "Needs you" || g.Kind != "needs" || g.CardOn != "w.1" {
		t.Fatalf("gate step = %+v, want Needs you with the card on w.1", g)
	}
	_, g = heldBoard(t, "Approve w.1.2?", true)
	if g.Text != "Needs you" {
		t.Fatalf("a card naming the gate by id: %+v", g)
	}
}

func TestHeldCard_UnrelatedCardStillMeansTheManagerWritesThePitch(t *testing.T) {
	_, g := heldBoard(t, "Is PR #9999 confirmed done?", true)
	if g.Text == "Needs you" || g.CardOn != "" || !strings.Contains(g.Text, "pitch") {
		t.Fatalf("an unrelated PR must not adopt the card: %+v", g)
	}
}

func TestHeldCard_DiagramHandIsYours(t *testing.T) {
	d, _ := heldBoard(t, "Is PR #4116 confirmed done?", true)
	ds := diagramStep(d.Workstreams[0].Diagram, "w.1.2")
	if ds == nil {
		for _, tr := range d.Workstreams[0].Diagram.Tracks {
			if ds = diagramStep(&tr, "w.1.2"); ds != nil {
				break
			}
		}
	}
	if ds == nil || ds.Actor != ActorYou {
		t.Fatalf("diagram step = %+v, want the user's line", ds)
	}
}

func TestHeldCard_GateMatchedOnItsPRLabelNotItsTitle(t *testing.T) {
	gate := gateStep("w.1.2", "Confirmed done", []string{"user"})
	gate.PR = []string{"4116"}
	_, g := heldBoardOf(t, "Is PR #4116 confirmed done?", true, gate)
	if g.Text != "Needs you" || g.CardOn != "w.1" {
		t.Fatalf("a gate labelled pr:4116 must adopt the card: %+v", g)
	}
	other := gateStep("w.1.2", "Sign off", []string{"user"})
	other.PR = []string{"4000"}
	_, g = heldBoardOf(t, "Is PR #4116 ready?", true, other)
	if g.Text == "Needs you" || g.CardOn != "" {
		t.Fatalf("another PR's label must not adopt it: %+v", g)
	}
}

func TestHeldCard_BeadIdIsAWholeToken(t *testing.T) {
	// w.1.20 is not w.1.2.
	_, g := heldBoard(t, "Approve w.1.20 now?", true)
	if g.CardOn != "" || g.Text == "Needs you" {
		t.Fatalf("a card naming w.1.20 adopted gate w.1.2: %+v", g)
	}
	for _, c := range []string{"Approve (w.1.2)?", "Approve w.1.2.", "w.1.2"} {
		if _, g = heldBoard(t, c, true); g.Text != "Needs you" {
			t.Fatalf("%q should name the gate: %+v", c, g)
		}
	}
	if hasToken("x-w.1.2 ok", "w.1.2") || hasToken("w.1.2x", "w.1.2") || !hasToken("see w.1.20 and w.1.2", "w.1.2") {
		t.Fatal("hasToken boundaries wrong")
	}
}
