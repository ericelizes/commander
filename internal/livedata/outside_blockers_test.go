package livedata

import "testing"

// outsideSnap is workstream w (an open step w.1 and a user gate w.9 behind it)
// with w.1 blocked by x.1, the only step of workstream x. edit changes x.1.
func outsideSnap(edit func(*SnapStep)) Snapshot {
	g := gateStep("w.9", "Merge approval", []string{"user"}, "w.1")
	w1 := leaf("w.1", "", "", "")
	w1.State = "not-started"
	w1.BlockedBy = []string{"x.1"}
	ws := SnapWorkstream{BeadID: "w", Steps: []SnapStep{w1, g}}
	x1 := leaf("x.1", "", "", "review (reviewer-a)")
	if edit != nil {
		edit(&x1)
	}
	snap := snapOf(ws, idle)
	snap.Workstreams = append(snap.Workstreams, SnapWorkstream{BeadID: "x", Title: "X", Project: "p", Steps: []SnapStep{x1}})
	return snap
}

func outsideLine(t *testing.T, snap Snapshot) line {
	t.Helper()
	r := Rows(snap, nowSec)[0]
	return line{kind: r.Line.Kind, text: r.Line.Text}
}

func TestOutsideBlockers_WaitElsewhereHoldsTheRow(t *testing.T) {
	if l := outsideLine(t, outsideSnap(nil)); l.kind != "waiting" {
		t.Fatalf("line %+v", l)
	}
}

func TestOutsideBlockers_NoWaitStillStalled(t *testing.T) {
	l := outsideLine(t, outsideSnap(func(s *SnapStep) { s.Waiting = "" }))
	if l.kind != "stalled" {
		t.Fatalf("line %+v", l)
	}
}

func TestOutsideBlockers_ClosedParkedSupersededDoNotHold(t *testing.T) {
	for name, edit := range map[string]func(*SnapStep){
		"closed":     func(s *SnapStep) { s.Status, s.State = "closed", "done" },
		"parked":     func(s *SnapStep) { s.Status = statusDeferred },
		"superseded": func(s *SnapStep) { s.Superseded = true },
	} {
		if l := outsideLine(t, outsideSnap(edit)); l.kind != "stalled" {
			t.Errorf("%s outside blocker held the row: %+v", name, l)
		}
	}
}

func TestOutsideBlockers_UsersWaitIsNotWaiting(t *testing.T) {
	snap := outsideSnap(func(s *SnapStep) { s.Waiting, s.State = "you", "needs-user" })
	if l := outsideLine(t, snap); l.kind == "waiting" {
		t.Fatalf("a wait on the user read as Waiting: %+v", l)
	}
}

func TestOutsideBlockers_LiveOperatorHoldsWithoutWaitLabel(t *testing.T) {
	l := outsideLine(t, outsideSnap(func(s *SnapStep) { s.Waiting, s.Owner, s.OwnerState = "", "operator-x-1", "working" }))
	if l.kind != "waiting" {
		t.Fatalf("line %+v", l)
	}
	l = outsideLine(t, outsideSnap(func(s *SnapStep) { s.Waiting, s.Owner, s.OwnerState = "", "operator-x-1", "died" }))
	if l.kind != "stalled" {
		t.Fatalf("a dead Operator held the row: %+v", l)
	}
}

func TestOutsideBlockers_CycleTerminates(t *testing.T) {
	snap := outsideSnap(func(s *SnapStep) { s.Waiting = ""; s.BlockedBy = []string{"w.9"} })
	if l := outsideLine(t, snap); l.kind != "stalled" {
		t.Fatalf("line %+v", l)
	}
}

func TestOutsideBlockers_SeededFromOpenStepsNotOnlyGates(t *testing.T) {
	snap := outsideSnap(nil)
	snap.Workstreams[0].Steps[1].BlockedBy = nil // the gate no longer reaches w.1
	if l := outsideLine(t, snap); l.kind != "waiting" {
		t.Fatalf("a step's own outside blocker did not hold the row: %+v", l)
	}
}

func TestOutsideBlockers_TrackRowCountsSiblingTrackAsOutside(t *testing.T) {
	t1 := SnapStep{BeadID: "w.t1", Title: "T1", Status: "open", State: "not-started", Track: true,
		Children: []SnapStep{{BeadID: "w.t1.1", Title: "a", Status: "open", State: "not-started", BlockedBy: []string{"w.t2.1"}}}}
	t2 := SnapStep{BeadID: "w.t2", Title: "T2", Status: "open", State: "in-progress", Track: true,
		Children: []SnapStep{leaf("w.t2.1", "", "", "review (reviewer-a)")}}
	snap := snapOf(SnapWorkstream{BeadID: "w", Steps: []SnapStep{t1, t2}}, idle)
	d := Board(snap, nowSec)
	for _, tr := range d.Workstreams[0].Tracks {
		if tr.BeadID == "w.t1" && tr.Kind != "waiting" {
			t.Fatalf("track %+v", tr)
		}
	}
}

// The board reads the bead an edge names, never what is under it, and nothing of
// a top-level workstream (no step holds its bead). prompts/commander.md teaches this.
func TestBoardReadsOnlyTheBeadAnEdgePointsAt(t *testing.T) {
	live := func(id string) SnapStep { return leaf(id, "operator-x-1", "working", "") }
	held := func(id string) SnapStep { return leaf(id, "", "", "review (reviewer-a)") }
	waiter := func(id string, on ...string) SnapStep {
		s := leaf(id, "", "", "")
		s.State, s.BlockedBy = "not-started", on
		return s
	}
	parent := func(under SnapStep) SnapStep {
		p := waiter("x.p")
		p.Children = []SnapStep{under}
		return p
	}
	two := func(a SnapStep) []SnapStep { return []SnapStep{a, nested("x.m", "M", waiter("x.m.1"))} }
	for _, tc := range []struct {
		name, on string
		far      []SnapStep
		want     string
	}{
		{"step with a live Operator", "x.1", []SnapStep{live("x.1")}, "waiting"},
		{"step with a wait", "x.1", []SnapStep{held("x.1")}, "waiting"},
		{"step under a nested workstream", "x.n.1", two(nested("x.n", "N", live("x.n.1"))), "waiting"},
		{"nested workstream's own wait", "x.n", two(func() SnapStep {
			n := nested("x.n", "N", waiter("x.n.1"))
			n.Waiting = "review (reviewer-a)"
			return n
		}()), "waiting"},
		{"live work under a nested workstream", "x.n", two(nested("x.n", "N", live("x.n.1"))), "stalled"},
		{"a wait under a nested workstream", "x.n", two(nested("x.n", "N", held("x.n.1"))), "stalled"},
		{"live work under a top-level workstream", "x", []SnapStep{live("x.1")}, "stalled"},
		{"a wait under a top-level workstream", "x", []SnapStep{held("x.1")}, "stalled"},
		{"live work under a parent step", "x.p", []SnapStep{parent(live("x.p.1"))}, "stalled"},
		{"a wait under a parent step", "x.p", []SnapStep{parent(held("x.p.1"))}, "stalled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := snapOf(SnapWorkstream{BeadID: "w", Steps: []SnapStep{waiter("w.1", tc.on)}}, idle)
			snap.Workstreams = append(snap.Workstreams, SnapWorkstream{N: 2, BeadID: "x", Title: "X", Project: "p", Steps: tc.far})
			r := Board(snap, nowSec).Workstreams[0]
			t.Logf("row reads %s %q", r.Kind, r.Text)
			if r.Kind != tc.want {
				t.Fatalf("row %s %q, want %s", r.Kind, r.Text, tc.want)
			}
		})
	}
	// Inside one workstream: a sibling nested workstream reads the same way.
	sibling := func(a SnapStep, b SnapStep) BoardTrack {
		return Board(snapOf(SnapWorkstream{BeadID: "w", Steps: []SnapStep{a, b}}, idle), nowSec).Workstreams[0].Tracks[0]
	}
	onB := nested("w.a", "A", waiter("w.a.1"))
	onB.BlockedBy = []string{"w.b"}
	for name, b := range map[string]SnapStep{"live": nested("w.b", "B", live("w.b.1")), "wait": nested("w.b", "B", held("w.b.1"))} {
		if tr := sibling(onB, b); tr.Kind != "stalled" {
			t.Errorf("edge to sibling B with a %s under it: %s %q", name, tr.Kind, tr.Text)
		}
	}
	if tr := sibling(nested("w.a", "A", waiter("w.a.1", "w.b.1")), nested("w.b", "B", live("w.b.1"))); tr.Kind != "waiting" {
		t.Errorf("edge on the step doing the work: %s %q", tr.Kind, tr.Text)
	}
}
