package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestAUserGateWaitingOnOthersIsNotTheUsersYet(t *testing.T) {
	gate := ObjectiveRow{BeadID: "p-1.10", Title: "Greenlight the popup route", Gate: true, Approvers: []string{"user"}}
	if waitsOnOthers(gate) {
		t.Fatal("no waits: it is the user's")
	}
	gate.Waits = []beads.Wait{{Kind: beads.WaitReview, Ref: "reviewer-team"}}
	if !waitsOnOthers(gate) {
		t.Fatal("waiting on a review: not the user's yet")
	}
	gate.Waits = []beads.Wait{{Kind: beads.WaitUser}}
	if waitsOnOthers(gate) {
		t.Fatal("a wait on the user is still the user's")
	}
}

func TestNeedsYouList_UserGateWithNonUserWait(t *testing.T) {
	listed := func(gate ObjectiveRow) bool {
		d := fleet()
		for i := range d.Objectives {
			if id := d.Objectives[i].BeadID; id == "a.1" || id == "a.2" {
				d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
			}
		}
		gate.BeadID, gate.ParentBeadID, gate.ProjectID = "a.9", "a", 1
		gate.Title, gate.Gate, gate.Approvers, gate.GateNote = "Spec approved", true, []string{"user"}, "The spec."
		d.Objectives = append(d.Objectives, gate)
		for _, n := range NeedsYouList(d) {
			if n.BeadID == "a.9" {
				return true
			}
		}
		return false
	}
	for _, kind := range []string{beads.WaitReview, beads.WaitCI, beads.WaitOperator, beads.WaitUntil, beads.WaitMainCI, beads.WaitProdTag} {
		if listed(ObjectiveRow{State: beads.StateNotStarted, Waits: []beads.Wait{{Kind: kind, Ref: "x"}}}) {
			t.Errorf("gate waiting on %s must not be in NEEDS YOU", kind)
		}
	}
	if !listed(ObjectiveRow{State: beads.StateNotStarted}) {
		t.Error("gate with no wait must be listed")
	}
	if !listed(ObjectiveRow{State: beads.StateNotStarted, Waits: []beads.Wait{{Kind: beads.WaitUser}}}) {
		t.Error("gate waiting on the user must be listed")
	}
	if !listed(ObjectiveRow{State: beads.StateNeedsUser, Ask: "Approve?", Note: "Approve the spec.", Waits: []beads.Wait{{Kind: beads.WaitReview, Ref: "x"}}}) {
		t.Error("gate explicitly flagged needs-user must be listed")
	}
	if listed(ObjectiveRow{State: beads.StateNotStarted, Status: beads.StatusDeferred}) {
		t.Error("deferred gate must stay out")
	}
}

// A ready ask with nothing to decide on is the Manager's to write, never the user's.
func TestNeedsYouList_AskWithNoPitchIsTheManagers(t *testing.T) {
	d := fleet()
	for i := range d.Objectives {
		if id := d.Objectives[i].BeadID; id == "a.1" || id == "a.2" {
			d.Objectives[i].Status, d.Objectives[i].State = "closed", beads.StateDone
		}
	}
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.8", ParentBeadID: "a", ProjectID: 1, Title: "Greenlight merging the fix", Gate: true, Approvers: []string{"user"}, State: beads.StateNotStarted},
		ObjectiveRow{BeadID: "a.9", ParentBeadID: "a", ProjectID: 1, Title: "Pick a name", State: beads.StateNeedsUser, Ask: "Look at this"},
	)
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.8" || n.BeadID == "a.9" {
			t.Errorf("an ask with no pitch is in NEEDS YOU: %+v", n)
		}
	}
	got := map[string]NoPitch{}
	for _, p := range NoPitchList(d) {
		got[p.BeadID] = p
	}
	if g := got["a.8"]; !g.Gate || g.ManagerRole != "manager-a" || g.WorkstreamID != "a" {
		t.Errorf("gate with no write-up = %+v", g)
	}
	if s := got["a.9"]; s.BeadID == "" || s.Gate {
		t.Errorf("needs-user step with no card = %+v", s)
	}
}
