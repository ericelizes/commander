package livedata

import (
	"strings"
	"testing"
)

// Nov 20, 20:56 (4.1, RCM SharePoint move): the agent on "Revise the test
// write" reported it was blocked (no Modal access on agent VMs), and the
// Commander put a hold label on the step. The row read "Paused" with nobody's
// move: no agent, no watched wait, no card. Every step is working, in a watched
// wait, or a card for the user; anything else is an invalid state, shown red
// and listed by the open-bead invariant (which mails the step's Manager).
func TestAHeldOrParkedStepIsAnInvalidState(t *testing.T) {
	step := func(f func(*SnapStep)) Snapshot {
		s := SnapStep{BeadID: "w.6.18", Title: "Revise the test write", Status: "open", State: "not-started", Lane: "agents"}
		f(&s)
		return Snapshot{Storage: "postgres", Projects: []SnapProject{{ID: 1, Name: "p"}}, OrchestrationMode: "active", Agents: map[string]SnapAgent{"manager-w": {State: "idle"}}, Workstreams: []SnapWorkstream{{N: 1, Project: "p", ProjectID: 1, BeadID: "w", Title: "Vibe apps", Status: "open", ManagerRole: "manager-w", Steps: []SnapStep{
			{BeadID: "w.6", Title: "RCM SharePoint move", Status: "open", Track: true, Children: []SnapStep{s}},
		}}}}
	}
	for name, c := range map[string]struct {
		snap      Snapshot
		text, why string
	}{
		"held":   {step(func(s *SnapStep) { s.Hold, s.HoldReason = true, "No Modal access for agents" }), HeldStepText, HeldStepWhy},
		"parked": {step(func(s *SnapStep) { s.Status = "deferred" }), ParkedStepText, ParkedStepWhy},
	} {
		found := false
		for _, v := range SnapshotViolations(c.snap) {
			found = found || v.BeadID == "w.6.18" && v.Why == c.why
		}
		if !found {
			t.Fatalf("%s: violations = %+v, want w.6.18 with %q", name, SnapshotViolations(c.snap), c.why)
		}
		d := Board(c.snap, 1)
		row := d.Workstreams[0]
		if row.Kind != "stuck" || !strings.HasPrefix(row.Text, "Invalid state:") {
			t.Fatalf("%s: row = %s %q, want red Invalid state", name, row.Kind, row.Text)
		}
		var leaf *WorkflowStep
		for i := range row.Workflow.Steps {
			for j := range row.Workflow.Steps[i].Children {
				if row.Workflow.Steps[i].Children[j].ID == "w.6.18" {
					leaf = &row.Workflow.Steps[i].Children[j]
				}
			}
		}
		if leaf == nil || leaf.Text != c.text {
			t.Fatalf("%s: workflow step = %+v, want %q", name, leaf, c.text)
		}
	}
	// The legal shape: the laptop-only work is its own step with the user's
	// card, and the agent step is blocked by it. Nothing is invalid.
	legal := step(func(s *SnapStep) { s.BlockedBy = []string{"w.6.20"} })
	track := &legal.Workstreams[0].Steps[0]
	track.Children = append([]SnapStep{{BeadID: "w.6.20", Title: "Run the Modal test write from your laptop", Status: "open", State: "needs-user"}}, track.Children...)
	if v := SnapshotViolations(legal); len(v) != 0 {
		t.Fatalf("a step blocked by the user's step is legal: %+v", v)
	}
	// A park with a time cmdrd ends is a watched wait, and a step under a
	// parked track is out of play with it: neither step is listed.
	listed := func(s Snapshot) bool {
		for _, v := range SnapshotViolations(s) {
			if v.BeadID == "w.6.18" {
				return true
			}
		}
		return false
	}
	if listed(step(func(s *SnapStep) { s.Status, s.ParkedUntil = "deferred", 2000000000 })) {
		t.Fatal("a timed park is a watched wait")
	}
	quiet := step(func(s *SnapStep) { s.Status = "deferred" })
	quiet.Workstreams[0].Steps[0].Status = "deferred"
	if listed(quiet) {
		t.Fatal("a step under a parked track is out of play with it")
	}
}
