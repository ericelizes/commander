package livedata

import (
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// A Manager's proposed ask is Commander's to review: it never reaches NEEDS
// YOU, and the row says Commander is reviewing it, until it is accepted.
func TestProposedAsk_StaysOutOfNeedsYouUntilAccepted(t *testing.T) {
	d := fleet()
	d.Objectives = append(d.Objectives, ObjectiveRow{BeadID: "a.6", ParentBeadID: "a", Title: "Pick the VMs", ProjectID: 1,
		State: beads.StateNeedsUser, Note: "which VMs?", Proposed: beads.ProposedAsk, ProposedAt: nowSec - 60})
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.6" {
			t.Fatal("a proposed ask reached NEEDS YOU")
		}
	}
	var p *NoPitch
	for _, np := range NoPitchList(d) {
		if np.BeadID == "a.6" {
			p = &np
		}
	}
	if p == nil || p.Proposed != beads.ProposedAsk {
		t.Fatalf("the proposed ask is not held for Commander: %+v", NoPitchList(d))
	}

	ask := flagged("w.2")
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{leaf("w.1", "", "", ""), ask}}
	snap := snapOf(ws, idle)
	snap.NoPitch = []NoPitch{{Project: "p", BeadID: "w.2", Title: "Pick the VMs", Proposed: beads.ProposedAsk, ProposedAt: nowSec - 60}}
	r := Rows(snap, nowSec)[0]
	if r.Line.Kind == "needs" || !strings.Contains(r.Line.Text, "Commander reviewing the proposed ask") {
		t.Fatalf("row line %+v", r.Line)
	}
	snap.NoPitch[0].ProposedAt = nowSec - pitchDeadline - 1
	if r := Rows(snap, nowSec)[0]; r.Line.Kind != "stalled" || !strings.Contains(r.Line.Text, "Commander's move") {
		t.Fatalf("an unreviewed proposal must turn red on the row: %+v", r.Line)
	}

	d.Objectives[len(d.Objectives)-1].Proposed = "" // accepted
	for _, n := range NeedsYouList(d) {
		if n.BeadID == "a.6" {
			return
		}
	}
	t.Fatal("an accepted ask must reach NEEDS YOU")
}

func TestProposalLabelRoundTrip(t *testing.T) {
	l := beads.ProposedLabel(beads.ProposedAsk, 1700000000, beads.StateInProgress)
	p, ok := beads.ProposalOf([]string{"kind:objective", l})
	if !ok || p.Kind != beads.ProposedAsk || p.At != 1700000000 || p.Prev != beads.StateInProgress || p.Label != l {
		t.Fatalf("%q -> %+v", l, p)
	}
	if _, ok := beads.ProposalOf([]string{"kind:objective"}); ok {
		t.Fatal("no label, no proposal")
	}
}

// A bead reads as proposed only by its own live proposed: label. An accepted
// or overruled gate (label gone), a gate never proposed, and a gate whose
// parent still carries a proposal all read as ordinary asks: never "Commander
// reviewing", and the gate's own card is not held for Commander.
func TestProposedIsOnlyTheBeadsOwnLabel(t *testing.T) {
	gate := func(id string, labels ...string) ObjectiveRow {
		p, _ := beads.ProposalOf(labels)
		return ObjectiveRow{BeadID: id, ParentBeadID: "a", Title: "Greenlight " + id, ProjectID: 1, Gate: true,
			Approvers: []string{"user"}, State: beads.StateNeedsUser, Note: "push it?",
			Proposed: p.Kind, ProposedAt: p.At}
	}
	cases := map[string]struct {
		row      ObjectiveRow
		proposed bool
	}{
		"accepted":           {gate("a.7"), false},
		"overruled":          {gate("a.8"), false},
		"never proposed":     {gate("a.9", "gate", "needs-user"), false},
		"proposed":           {gate("a.10", beads.ProposedLabel(beads.ProposedGate, nowSec-60, "")), true},
		"parent is labelled": {gate("a.11", "gate"), false},
	}
	for name, c := range cases {
		d := fleet()
		parent := ObjectiveRow{BeadID: "a", Title: "Track", ProjectID: 1, Proposed: beads.ProposedGate, ProposedAt: nowSec - 60}
		_ = parent // the parent's label never copies onto a child row
		d.Objectives = append(d.Objectives, c.row)
		held := false
		for _, np := range NoPitchList(d) {
			if np.BeadID == c.row.BeadID && np.Proposed != "" {
				held = true
			}
		}
		if held != c.proposed {
			t.Errorf("%s: held for Commander = %v, want %v", name, held, c.proposed)
		}
		shown := false
		for _, n := range NeedsYouList(d) {
			if n.BeadID == c.row.BeadID {
				shown = true
			}
		}
		if shown == c.proposed {
			t.Errorf("%s: in NEEDS YOU = %v with proposed = %v", name, shown, c.proposed)
		}
	}
}

// With the label gone and no pitch, the row says the Manager is writing it,
// never that Commander is reviewing a proposal.
func TestNoPitchWithoutProposalNeverReadsCommanderReviewing(t *testing.T) {
	ask := flagged("w.2")
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{leaf("w.1", "", "", ""), ask}}
	snap := snapOf(ws, idle)
	snap.NoPitch = []NoPitch{{Project: "p", BeadID: "w.2", Title: "Pick the VMs", Gate: true}}
	r := Rows(snap, nowSec)[0]
	if strings.Contains(r.Line.Text, "Commander reviewing") {
		t.Fatalf("row line %+v", r.Line)
	}
}

// A recorded wait holds a step whether or not the step carries a proposed:
// label (a review step or a long wait awaiting Commander): the holder check
// never reads the proposal. A slug-labelled timer is the one wait that holds
// nothing, proposed or not (TestInvariantFlagsAnUntypedWait).
func TestProposedStepWithATimerWaitIsHeld(t *testing.T) {
	until := time.Now().Add(6 * time.Hour)
	waits := map[string][]beads.Wait{
		"words":     {{Kind: beads.WaitUntil, Ref: "the quiet window opens", Until: until}},
		"unlabeled": {{Kind: beads.WaitUntil, Until: until}},
	}
	for name, w := range waits {
		for _, kind := range []string{beads.ProposedReview, beads.ProposedWait, ""} {
			rows := []ObjectiveRow{
				{BeadID: "p-1", Status: "open"},
				{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Waits: w, Proposed: kind, ProposedAt: nowSec - 60},
			}
			if got := OpenBeadInvariant(rows, nil); len(got) != 0 {
				t.Errorf("%s/%q: a proposed step with a timer wait flagged %v", name, kind, ids(got))
			}
		}
	}
	step := leaf("w.1", "", "", "")
	step.Status, step.Waiting, step.WaitUntil = "open", "reconcile · soon", until.Unix()
	snap := snapOf(SnapWorkstream{BeadID: "w", Steps: []SnapStep{step}}, idle)
	snap.NoPitch = []NoPitch{{Project: "p", BeadID: "w.1", Title: "Re-review", Proposed: beads.ProposedReview, ProposedAt: nowSec - 60}}
	for _, v := range SnapshotViolations(snap) {
		if v.BeadID == "w.1" {
			t.Errorf("snapshot reads a proposed step with a timer wait as unheld: %+v", v)
		}
	}
}
