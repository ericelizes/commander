package livedata

import "testing"

// A gate close releases the step behind it. Closed alone, the step has nothing
// holding it and the row reads State missing; with its next wait written in the
// same command (what cmdr internal-close-objective --then-waiting does) it never does.
func TestGateCloseWithItsNextHolderNeverReadsStateMissing(t *testing.T) {
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []SnapWorkstream{{
			N: 1, BeadID: "w", Project: "p", Title: "Notify", Status: "open", ManagerRole: "manager-w",
			Steps: []SnapStep{
				{BeadID: "w.9", Title: "Send gate", Status: "open", State: "in-progress", Gate: true, Approvers: []string{"user"}},
				{BeadID: "w.2", Title: "Send it", Status: "open", State: "not-started", BlockedBy: []string{"w.9"}},
			},
		}},
	}
	closeGate := func(st *SnapStep) { st.Status, st.State = "closed", "done" }
	text := func(s Snapshot) string { return Board(s, nowSec).Workstreams[0].Text }

	if got := text(WithEdits(s, nil)); got == "State missing" {
		t.Fatalf("before the close the gate holds the work, row reads %q", got)
	}
	closed := map[string]func(*SnapStep){"w.9": closeGate}
	if got := text(WithEdits(s, closed)); got != "State missing" {
		t.Fatalf("closed alone: row %q, want State missing", got)
	}
	if v := UnheldAfter(s, closed); len(v) != 1 || v[0].BeadID != "w.2" || v[0].ManagerRole != "manager-w" {
		t.Fatalf("UnheldAfter = %+v, want w.2", v)
	}

	held := map[string]func(*SnapStep){"w.9": closeGate, "w.2": func(st *SnapStep) {
		st.Waiting, st.WaitUntil, st.WaitUntilAs = "the Manager sends the final text", nowSec+3600, "the Manager sends the final text"
	}}
	if got := text(WithEdits(s, held)); got == "State missing" {
		t.Fatalf("closed with its next wait: row reads %q", got)
	}
	if v := UnheldAfter(s, held); len(v) != 0 {
		t.Fatalf("UnheldAfter with the hold = %+v, want none", v)
	}
	if s.Workstreams[0].Steps[0].Status != "open" {
		t.Fatal("WithEdits changed the snapshot it was given")
	}
}
