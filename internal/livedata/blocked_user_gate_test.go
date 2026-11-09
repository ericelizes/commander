package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestBlockedUserGate_FlaggedGateNotListedWhileBlocked(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.6", ParentBeadID: "a", Title: "Independent review", ProjectID: 1, State: beads.StateNotStarted,
			Gate: true, Approvers: []string{"operator"}},
		ObjectiveRow{BeadID: "a.7", ParentBeadID: "a", Title: "Cleanup approval", ProjectID: 1, State: beads.StateNeedsUser,
			Gate: true, Approvers: []string{"user"}, BlockedBy: []string{"a.6"}, Note: "approve cleanup"},
	)
	listed := func() bool {
		for _, n := range NeedsYouList(d) {
			if n.BeadID == "a.7" {
				return true
			}
		}
		return false
	}
	if listed() {
		t.Fatal("a flagged user gate blocked by an open gate must not be in NEEDS YOU")
	}
	for i := range d.Objectives {
		if id := d.Objectives[i].BeadID; id == "a.6" || id == "a.1" || id == "a.2" {
			d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
		}
	}
	if !listed() {
		t.Fatal("the flagged gate should be listed once its blockers close")
	}
}

func TestBlockedUserGate_DeferredBlockerIsReady(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.6", ParentBeadID: "a", Title: "Parked", ProjectID: 1, State: beads.StateNotStarted, Status: beads.StatusDeferred},
		ObjectiveRow{BeadID: "a.7", ParentBeadID: "a", Title: "Cleanup approval", ProjectID: 1, State: beads.StateNeedsUser,
			Gate: true, Approvers: []string{"user"}, BlockedBy: []string{"a.6"}, Note: "approve cleanup"},
	)
	for i := range d.Objectives {
		if id := d.Objectives[i].BeadID; id == "a.1" || id == "a.2" {
			d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
		}
	}
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.7" {
			return
		}
	}
	t.Fatal("a gate blocked only by a deferred bead is ready")
}
