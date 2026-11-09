package livedata

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// A step whose Operator died or exited is broken whatever wait sits on it; a
// finished step waiting on its Manager is held until the wait ends.
func TestInvariantGoneOperatorHoldsNothing(t *testing.T) {
	free := []beads.Wait{{Kind: beads.WaitUntil, Ref: "operator-a busy", Since: time.Now()}}
	toManager := []beads.Wait{{Kind: beads.WaitUntil, Ref: beads.ManagerCloseWait, Until: time.Now().Add(time.Hour), Since: time.Now()}}
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open", Owner: "manager-p-1"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "in_progress", Owner: "operator-a", OwnerState: OwnerDied, Waits: free},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "in_progress", State: beads.StateReview, Owner: "operator-b", OwnerState: OwnerFinished, Waits: free},
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", OwnerState: OwnerDied, Waits: free},
		{BeadID: "p-1.4", ParentBeadID: "p-1", Status: "in_progress", State: beads.StateReview, Owner: "manager-p-1", Waits: toManager},
		{BeadID: "p-1.5", ParentBeadID: "p-1", Status: "open", State: beads.StateNeedsUser, Owner: "operator-c", OwnerState: OwnerDied},
	}
	got := map[string]bool{}
	for _, id := range ids(OpenBeadInvariant(rows, func(string) string { return "" })) {
		got[id] = true
	}
	if len(got) != 3 || !got["p-1.1"] || !got["p-1.2"] || !got["p-1.3"] {
		t.Fatalf("violations = %v, want p-1.1, p-1.2, p-1.3", got)
	}
}

// The died marker cmdrd leaves on an unclaimed step reads as died on its row;
// once someone claims it the marker says nothing.
func TestRowsReadTheDiedMarker(t *testing.T) {
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "p-1.1", Title: "Build", Status: "open", Labels: []string{"kind:objective", beads.OperatorDiedLabel}},
		{ID: "p-1.2", Title: "Ship", Status: "in_progress", Labels: []string{"kind:objective", beads.OperatorDiedLabel, "owner:operator-z"}},
	}}
	rows, err := objectiveRowsFromBeads(context.Background(), bc, []store.Project{{ID: 1, Name: "p", Path: "/p"}})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]string{}
	for _, r := range rows {
		state[r.BeadID] = r.OwnerState
	}
	if state["p-1.1"] != OwnerDied || state["p-1.2"] != "" || state["p-1"] != "" {
		t.Fatalf("owner states = %v", state)
	}
	if v := ids(OpenBeadInvariant(rows, nil)); len(v) != 1 || v[0] != "p-1.1" {
		t.Fatalf("violations = %v, want [p-1.1]", v)
	}
}
