package livedata

import "testing"

// A row held by a blocker in another track or workstream says who it waits on, never the blocker's own line.
func TestOutsideBlockerRowNamesTheBlocker(t *testing.T) {
	flow := func(id string) SnapStep { return st(id, gateOf("Workflow approved", "user"), closedStep) }
	mk := func(b SnapStep) []SnapStep {
		return []SnapStep{
			st("w.1", title("A. Move jobs"), asTrack, kids(flow("w.1.1"), st("w.1.2", blockedBy("w.1.1", "w.2.2")))),
			st("w.2", title("B. DNS fix"), asTrack, kids(flow("w.2.1"), b)),
		}
	}
	live := st("w.2.2", title("Read-only watcher: poll the broker"), inProgress, ownedBy("operator-b", "working"), blockedBy("w.2.1"))
	row := func(steps []SnapStep) BoardRow {
		s := Snapshot{Workstreams: []SnapWorkstream{{N: 2, Project: "p", BeadID: "w", Title: "W", ManagerRole: "manager-w", Steps: steps}},
			Agents: map[string]SnapAgent{"manager-w": caseIdle}, Live: &SnapLive{Operators: []string{"operator-b"}}}
		return Board(s, caseNow).Workstreams[0]
	}
	r := row(mk(live))
	if got := r.Tracks[0].Text; got != "Waiting on #2.2 (read-only watcher: poll the broker)" || r.Tracks[0].Kind != "waiting" {
		t.Fatalf("track A %s %q", r.Tracks[0].Kind, r.Tracks[0].Text)
	}
	// A recorded wait on the other track's step reads the same, not "Waiting for <wait>".
	wait := st("w.2.2", title("Mail DNS fix confirmed"), waiting("review (reviewer-a)"), blockedBy("w.2.1"))
	if got := row(mk(wait)).Tracks[0].Text; got != "Waiting on #2.2 (mail DNS fix confirmed)" {
		t.Fatalf("wait on B: %q", got)
	}
}

func TestOutsideBlockerAcrossWorkstreams(t *testing.T) {
	a := SnapWorkstream{N: 1, Project: "p", BeadID: "a", Title: "A", ManagerRole: "manager-a",
		Steps: []SnapStep{st("a.1", title("Cut over"), blockedBy("b.1"))}}
	b := SnapWorkstream{N: 2, Project: "p", BeadID: "b", Title: "B", ManagerRole: "manager-b",
		Steps: []SnapStep{st("b.1", title("Fix mail DNS"), waiting("CI #4"))}}
	s := Snapshot{Workstreams: []SnapWorkstream{a, b}, Agents: map[string]SnapAgent{"manager-a": caseIdle, "manager-b": caseIdle}}
	if got := Board(s, caseNow).Workstreams[0].Text; got != "Waiting on #2 (fix mail DNS)" {
		t.Fatalf("got %q", got)
	}
}

// Negatives: a wait on the row's own step still reads "Waiting for ...", work inside still reads "Working on".
func TestOwnWaitAndOwnOperatorStillRead(t *testing.T) {
	r := boardOne([]SnapStep{st("w.1", waiting("CI #4"))}, &caseIdle)
	expectLine(t, r, "waiting", "Waiting for CI")
	r = boardOne([]SnapStep{st("w.1", inProgress, ownedBy("operator-x", "working"), title("Build it"))}, &caseIdle)
	if r.Kind != "working" || r.Text[:10] != "Working on" {
		t.Fatalf("got %s %q", r.Kind, r.Text)
	}
}
