package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// A bead with children shows what they add up to; a flag on the bead itself wins.
func TestDerivedState(t *testing.T) {
	leaf := func(state, status string) *StepNode {
		return &StepNode{Row: ObjectiveRow{State: state, Status: status}}
	}
	parent := func(own string, kids ...*StepNode) *StepNode {
		return &StepNode{Row: ObjectiveRow{State: own, Status: "open"}, Children: kids}
	}
	cases := []struct {
		name string
		n    *StepNode
		want string
	}{
		{"leaf is itself", leaf(beads.StateReview, "in_progress"), beads.StateReview},
		{"all children done, parent still open: progress, not done", parent(beads.StateNotStarted, leaf("", "closed"), leaf("", "closed")), beads.StateInProgress},
		{"all children done, parent closed", &StepNode{Row: ObjectiveRow{State: beads.StateNotStarted, Status: "closed"}, Children: []*StepNode{leaf("", "closed")}}, beads.StateDone},
		{"all children done keeps the parent's own review state", parent(beads.StateReview, leaf("", "closed")), beads.StateReview},
		{"a working child", parent(beads.StateNotStarted, leaf("", "closed"), leaf(beads.StateInProgress, "in_progress")), beads.StateInProgress},
		{"needs you beats working", parent(beads.StateInProgress, leaf(beads.StateInProgress, "in_progress"), leaf(beads.StateNeedsUser, "open")), beads.StateNeedsUser},
		{"set in progress by hand, nobody on it", parent(beads.StateInProgress, leaf(beads.StateNotStarted, "open")), beads.StateNotStarted},
		{"review under a parent reads in progress", parent(beads.StateNotStarted, leaf(beads.StateReview, "in_progress"), leaf("", "closed")), beads.StateInProgress},
		{"own flag wins", parent(beads.StateNeedsUser, leaf(beads.StateInProgress, "in_progress")), beads.StateNeedsUser},
		{"nested", parent(beads.StateNotStarted, parent(beads.StateNotStarted, leaf(beads.StateInProgress, "in_progress"))), beads.StateInProgress},
		{"partly done, rest not started", parent(beads.StateNotStarted, leaf("", "closed"), leaf(beads.StateNotStarted, "open")), beads.StateInProgress},
		{"partly done with a blocked child", parent(beads.StateNotStarted, leaf("", "closed"), leaf(beads.StateBlocked, "open")), beads.StateBlocked},
	}
	for _, c := range cases {
		if got := DerivedState(c.n); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// A live agent under a bead lifts its row: working shows, an owner makes it in progress.
func TestDerivedLive(t *testing.T) {
	owned := func(owner string) *StepNode {
		return &StepNode{Row: ObjectiveRow{State: beads.StateNotStarted, Status: "open", Owner: owner}}
	}
	live := func(owner string) string {
		return map[string]string{"operator-a": "working", "operator-b": "idle"}[owner]
	}
	parent := func(own string, kids ...*StepNode) *StepNode {
		return &StepNode{Row: ObjectiveRow{State: own, Status: "open"}, Children: kids}
	}
	cases := []struct {
		name        string
		n           *StepNode
		want        string
		wantWorking bool
	}{
		{"working owner", parent(beads.StateNotStarted, parent(beads.StateNotStarted, owned("operator-a"))), beads.StateInProgress, true},
		{"idle owner is in progress, not working", parent(beads.StateNotStarted, owned("operator-b")), beads.StateInProgress, false},
		{"a claim with no live process is still in progress", parent(beads.StateNotStarted, owned("operator-gone")), beads.StateInProgress, false},
		{"an open gate whose children are done is not done", &StepNode{Row: ObjectiveRow{State: beads.StateInProgress, Status: "open", Gate: true}, Children: []*StepNode{{Row: ObjectiveRow{State: beads.StateDone, Status: "closed"}}}}, beads.StateInProgress, false},
		{"needs you wins, working still shows", parent(beads.StateNeedsUser, owned("operator-a")), beads.StateNeedsUser, true},
	}
	for _, c := range cases {
		got, working := DerivedLive(c.n, live)
		if got != c.want || working != c.wantWorking {
			t.Errorf("%s: got %s/%v, want %s/%v", c.name, got, working, c.want, c.wantWorking)
		}
	}
}

// A parked (deferred) bead is out of play: it never moves its parent, and a
// parent with only parked children reads as itself.
func TestDerivedState_DeferredIsOutOfPlay(t *testing.T) {
	parked := &StepNode{Row: ObjectiveRow{State: beads.StateInProgress, Status: beads.StatusDeferred}}
	live := &StepNode{Row: ObjectiveRow{State: beads.StateNotStarted, Status: "open"}}
	if got := DerivedState(&StepNode{Row: ObjectiveRow{State: beads.StateNotStarted, Status: "open"}, Children: []*StepNode{parked}}); got != beads.StateNotStarted {
		t.Errorf("only parked children: got %s", got)
	}
	if got := DerivedState(&StepNode{Row: ObjectiveRow{State: beads.StateNotStarted, Status: "open"}, Children: []*StepNode{parked, live}}); got != beads.StateNotStarted {
		t.Errorf("parked beside open: got %s", got)
	}
	if got := DerivedState(parked); got != beads.StateNotStarted {
		t.Errorf("parked itself: got %s", got)
	}
}

func TestNeedsYouList_DeferredIsSkipped(t *testing.T) {
	d := fleet()
	before := len(NeedsYouList(d))
	d.Objectives = append(d.Objectives,
		ObjectiveRow{BeadID: "a.3", ParentBeadID: "a", Title: "Workflow approved", ProjectID: 1, State: beads.StateNotStarted, Status: beads.StatusDeferred,
			Gate: true, Approvers: []string{"user"}},
		ObjectiveRow{BeadID: "a.4", ParentBeadID: "a", Title: "Ask", ProjectID: 1, State: beads.StateNeedsUser, Status: beads.StatusDeferred},
	)
	if got := NeedsYouList(d); len(got) != before {
		t.Fatalf("parked gate and ask must not be listed: %+v", got)
	}
}

// A parked (deferred) user gate, with a legacy owner label too, is out of
// NEEDS YOU, is not the next checkpoint and never makes the row red.
func TestParkUserGateNotInNeedsYouNorNextCheckpoint(t *testing.T) {
	d := fleet()
	before := len(NeedsYouList(d))
	d.Objectives = append(d.Objectives, ObjectiveRow{BeadID: "a.5", ParentBeadID: "a", Title: "Merge approval", ProjectID: 1,
		State: beads.StateNotStarted, Status: beads.StatusDeferred, Gate: true, Approvers: []string{"user"}, ParkedUntil: 2_000_000})
	if got := NeedsYouList(d); len(got) != before {
		t.Fatalf("parked user gate listed: %+v", got)
	}
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{
		leaf("w.1", "operator-a", "working", ""),
		{BeadID: "w.9", Title: "PR approval", Status: "deferred", State: "not-started", Gate: true, Approvers: []string{"user"}, BlockedBy: []string{"w.1"}},
	}}
	if r := Rows(snapOf(ws, idle), nowSec)[0]; r.Line.Kind != "working" {
		t.Fatalf("parked gate changed the row: %+v", r.Line)
	}
}

func TestNestObjectives_ClosedRootsAreNotWorkstreams(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Title: "open ws", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Title: "done step", Status: "closed"},
		{BeadID: "p-2", Title: "closed ws", Status: "closed"},
		{BeadID: "p-2.1", ParentBeadID: "p-2", Title: "its step", Status: "closed"},
		// a promoted/moved step: closed, its parent is not in the list
		{BeadID: "p-3.13.1", ParentBeadID: "p-3.13", Title: "promoted", Status: "closed"},
		{BeadID: "p-4", Title: "superseded", Status: "open", Superseded: true},
	}
	got := NestObjectives(rows)
	var ids []string
	for _, n := range got {
		ids = append(ids, n.Row.BeadID)
	}
	if len(ids) != 2 || ids[0] != "p-1" || ids[1] != "p-1.1" {
		t.Fatalf("got %v, want [p-1 p-1.1]", ids)
	}
}

func TestNestObjectives_OpenOrphanWithUnlistedParentStaysRoot(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "w", Title: "WS", Status: "open"},
		{BeadID: "w.1", ParentBeadID: "w", Title: "Step", Status: "open"},
		{BeadID: "epic.1", ParentBeadID: "epic", Title: "Under a plain container", Status: "open"},
	}
	var roots, depth1 []string
	for _, n := range NestObjectives(rows) {
		if n.Depth == 0 {
			roots = append(roots, n.Row.BeadID)
		} else {
			depth1 = append(depth1, n.Row.BeadID)
		}
	}
	if len(roots) != 2 || roots[0] != "w" || roots[1] != "epic.1" {
		t.Fatalf("roots = %v, want [w epic.1] (open orphan must not disappear)", roots)
	}
	if len(depth1) != 1 || depth1[0] != "w.1" {
		t.Fatalf("a step under an open workstream must still nest: %v", depth1)
	}
}
