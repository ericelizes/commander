package livedata

import "testing"

func boardWS(mod func(*SnapWorkstream), steps ...SnapStep) BoardRow {
	w := SnapWorkstream{N: 1, Project: "p", BeadID: "w", Title: "W", Status: "open", ManagerRole: "manager-w", Steps: steps}
	mod(&w)
	s := Snapshot{Workstreams: []SnapWorkstream{w}, Agents: map[string]SnapAgent{"manager-w": caseIdle}}
	return Board(s, caseNow).Workstreams[0]
}

func parkedFor(until int64) func(*SnapStep) {
	return func(s *SnapStep) { s.Status = "deferred"; s.ParkedUntil = until }
}

func TestOwnTimedWaitReadsWaiting(t *testing.T) {
	at := int64(caseNow + 3600)
	r := boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitUntil, w.WaitUntilAs = "door-checks back", at, "door-checks back"
	}, st("a", closedStep), st("b", parkedFor(at)))
	expectLine(t, r, "waiting", "Waiting for door-checks back · "+clock(at, caseNow, BoardZone()))
}

func TestOwnTypedWaitReadsWaiting(t *testing.T) {
	r := boardWS(func(w *SnapWorkstream) { w.Waiting = "review (a-reviewer)" }, st("a", closedStep))
	expectLine(t, r, "waiting", "Waiting for review")
}

func TestExpiredOwnWaitIsNothingToDo(t *testing.T) {
	r := boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitUntil, w.WaitUntilAs = "x", caseNow-60, "x"
	}, st("a", closedStep))
	expectLine(t, r, "quiet", "Nothing to do")
}

func TestParkedWorkReturnsEarliest(t *testing.T) {
	r := boardWS(func(*SnapWorkstream) {}, st("a", closedStep), st("b", parkedFor(caseNow+7200)), st("c", parkedFor(caseNow+3600)))
	expectLine(t, r, "quiet", "Parked work returns "+clock(caseNow+3600, caseNow, BoardZone()))
}

func TestParkedWithoutTimeOrPastIsNothingToDo(t *testing.T) {
	r := boardWS(func(*SnapWorkstream) {}, st("a", closedStep), st("b", parkedFor(0)), st("c", parkedFor(caseNow-5)))
	expectLine(t, r, "quiet", "Nothing to do")
}

func TestOpenUnheldStepBeatsOwnWait(t *testing.T) {
	r := boardWS(func(w *SnapWorkstream) { w.Waiting = "review (x)" }, st("a", inProgress))
	expectLine(t, r, "stalled", "Stalled")
	r = boardWS(func(*SnapWorkstream) {}, st("a", inProgress))
	expectLine(t, r, "stalled", "Stalled")
}

func TestTwoUntilWaitsJudgedEach(t *testing.T) {
	soon, late := int64(caseNow-60), int64(caseNow+3600)
	r := boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitUntil, w.WaitUntilAs = "a-back · b-back", soon, "a-back"
		w.WaitUntils = map[string]int64{"a-back": soon, "b-back": late}
	}, st("a", closedStep))
	expectLine(t, r, "waiting", "Waiting for b-back · "+clock(late, caseNow, BoardZone()))
	r = boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitUntil, w.WaitUntilAs = "a-back · b-back", soon, "a-back"
		w.WaitUntils = map[string]int64{"a-back": soon, "b-back": soon}
	}, st("a", closedStep))
	expectLine(t, r, "quiet", "Nothing to do")
}

func TestOwnWaitOnTrackMatchesWorkflowAndDiagram(t *testing.T) {
	at := int64(caseNow + 3600)
	tr := func(id string, f ...func(*SnapStep)) SnapStep {
		return st(id, append([]func(*SnapStep){asTrack, kids(st(id+"-k", closedStep))}, f...)...)
	}
	t1 := tr("t1", func(s *SnapStep) { s.Waiting, s.WaitUntil, s.WaitUntilAs = "reconcile", at, "reconcile" })
	t2 := tr("t2", func(s *SnapStep) { s.Waiting = "review (x)" })
	r := boardWS(func(*SnapWorkstream) {}, t1, t2)
	want := map[string]string{"t1": "Waiting for reconcile · " + clock(at, caseNow, BoardZone()), "t2": "Waiting for review"}
	for _, tk := range r.Tracks {
		if tk.Text != want[tk.BeadID] {
			t.Errorf("track %s reads %q, want %q", tk.BeadID, tk.Text, want[tk.BeadID])
		}
	}
	if len(r.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(r.Tracks))
	}
	for _, s := range r.Workflow.Steps {
		if w, ok := want[s.ID]; ok && s.Text != w {
			t.Errorf("workflow %s reads %q, want %q", s.ID, s.Text, w)
		}
	}
	for _, d := range r.Diagram.Tracks {
		if w, ok := want[d.ID]; ok {
			for _, s := range d.Steps {
				if s.ID == d.ID && s.Text != w {
					t.Errorf("diagram %s reads %q, want %q", s.ID, s.Text, w)
				}
			}
		}
	}
}
