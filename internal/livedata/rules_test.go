package livedata

import (
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func ruleChecks(fs []DiagramFinding) []string {
	var out []string
	for _, f := range fs {
		switch f.Check {
		case "started-early", "asked-early", "done-early", "stage-done", "duplicate-wait", "duplicate-gate":
			out = append(out, f.Check+" "+f.Bead)
		}
	}
	slices.Sort(out)
	return out
}

func TestPlanRules(t *testing.T) {
	open := func(id string, mut ...func(*SnapStep)) SnapStep {
		s := SnapStep{BeadID: id, Title: id, State: "not-started", Status: "open"}
		for _, m := range mut {
			m(&s)
		}
		return s
	}
	done := func(s *SnapStep) { s.State, s.Status = "done", "closed" }
	after := func(ids ...string) func(*SnapStep) { return func(s *SnapStep) { s.BlockedBy = ids } }
	started := func(s *SnapStep) { s.State, s.Status, s.Owner = "in-progress", "in_progress", "manager-lab-x" }
	review := func(pr string) func(*SnapStep) {
		return func(s *SnapStep) { s.Review, s.Waiting = pr, "review (reviewer-a)" }
	}
	gate := func(title string) func(*SnapStep) {
		return func(s *SnapStep) { s.Gate, s.Title, s.Approvers, s.Criteria = true, title, []string{"user"}, "ok" }
	}
	parkedStep := func(s *SnapStep) { s.Status = "deferred" }

	ws := SnapWorkstream{Project: "lab", BeadID: "lab-x", Title: "X", Steps: []SnapStep{
		open("lab-x.1", gate("Workflow approved"), done),
		open("lab-x.2", gate("Workflow approved"), func(s *SnapStep) { s.State = "needs-user" }),
		// waits on an open step, yet started, asking, and done
		open("lab-x.3"),
		open("lab-x.4", after("lab-x.3"), started),
		open("lab-x.5", after("lab-x.3"), func(s *SnapStep) { s.State = "needs-user" }),
		open("lab-x.6", after("lab-x.3"), done),
		// a stage whose steps are all done
		open("lab-x.7", started, func(s *SnapStep) { s.Children = []SnapStep{open("lab-x.7.1", done)} }),
		// one review on two steps
		open("lab-x.8", review("70")),
		open("lab-x.9", review("70"), after("lab-x.8")),
		// a parked blocker holds nothing; started behind it is fine
		open("lab-x.10", parkedStep),
		open("lab-x.11", after("lab-x.10"), started),
		// a step whose parent waits on open work cannot start either
		open("lab-x.12", after("lab-x.3"), func(s *SnapStep) { s.Children = []SnapStep{open("lab-x.12.1", started)} }),
	}}
	got := ruleChecks(CheckDiagram(ws, ws.BeadID))
	want := []string{
		"asked-early lab-x.5",
		"done-early lab-x.6",
		"duplicate-gate lab-x.2",
		"duplicate-wait lab-x.8",
		"stage-done lab-x.7",
		"started-early lab-x.12.1",
		"started-early lab-x.4",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n got %v\nwant %v", got, want)
	}
}

// started-early judges the bead's own state, never the rollup: an open stage
// whose children are done derives in-progress (DerivedLive), and no state write
// on the bead can clear that. Rows go through snapSteps, as in a real snapshot.
func TestPlanRules_StartedEarlyJudgesTheBeadsOwnState(t *testing.T) {
	row := func(id, status string, mut ...func(*ObjectiveRow)) *StepNode {
		state, _ := beads.StepState(status, nil)
		r := ObjectiveRow{BeadID: id, Title: id, Status: status, State: state}
		for _, m := range mut {
			m(&r)
		}
		return &StepNode{Row: r}
	}
	after := func(ids ...string) func(*ObjectiveRow) { return func(r *ObjectiveRow) { r.BlockedBy = ids } }
	owner := func(r *ObjectiveRow) { r.Owner = "manager-lab-e" }
	dropped := func(r *ObjectiveRow) { r.Superseded = true }
	with := func(n *StepNode, kids ...*StepNode) *StepNode { n.Children = kids; return n }

	for _, tc := range []struct {
		name  string
		step  *StepNode
		state string // what the rollup makes of the step
		want  bool
	}{
		{"open stage, closed child (the reported shape)",
			with(row("lab-e.2", "open", after("lab-e.1")), row("lab-e.2.1", "closed")), "in-progress", false},
		{"open stage, closed child and a dropped open child",
			with(row("lab-e.2", "open", after("lab-e.1")), row("lab-e.2.1", "closed"), row("lab-e.2.2", "open", dropped)), "in-progress", false},
		{"open stage, only a dropped child still in progress",
			with(row("lab-e.2", "open", after("lab-e.1")), row("lab-e.2.1", "in_progress", dropped)), "in-progress", false},
		{"open leaf", row("lab-e.2", "open", after("lab-e.1")), "not-started", false},

		{"in progress leaf", row("lab-e.2", "in_progress", after("lab-e.1")), "in-progress", true},
		{"in progress stage, closed child",
			with(row("lab-e.2", "in_progress", after("lab-e.1")), row("lab-e.2.1", "closed")), "in-progress", true},
		{"claimed open leaf", row("lab-e.2", "open", after("lab-e.1"), owner), "in-progress", true},
		{"claimed open stage, closed child",
			with(row("lab-e.2", "open", after("lab-e.1"), owner), row("lab-e.2.1", "closed")), "in-progress", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := snapSteps([]*StepNode{row("lab-e.1", "open"), tc.step}, "", nil, false)
			if got := steps[1].State; got != tc.state {
				t.Fatalf("derived state %q, want %q", got, tc.state)
			}
			ws := SnapWorkstream{Project: "lab", BeadID: "lab-e", Steps: steps}
			got := slices.Contains(ruleChecks(CheckDiagram(ws, ws.BeadID)), "started-early lab-e.2")
			if got != tc.want {
				t.Errorf("started-early = %v, want %v", got, tc.want)
			}
			// A claim is also why it fires, and --state not-started alone leaves
			// the owner label on: the advice has to name both.
			for _, f := range CheckDiagram(ws, ws.BeadID) {
				if f.Check != "started-early" {
					continue
				}
				if !strings.Contains(f.Why, "it is claimed or in progress while") {
					t.Errorf("why: %q", f.Why)
				}
				if !strings.Contains(f.Fix, "--bead lab-e.2 --state not-started --owner none") {
					t.Errorf("fix: %q", f.Fix)
				}
			}
		})
	}
}

// Two passed gates with the same title are history, not a pending checkpoint.
func TestPlanRules_ClosedDuplicateGatesAreHistory(t *testing.T) {
	g := func(id string) SnapStep {
		return SnapStep{BeadID: id, Title: "Workflow approved", State: "done", Status: "closed", Gate: true, Approvers: []string{"user"}, Criteria: "ok"}
	}
	ws := SnapWorkstream{Project: "lab", BeadID: "lab-y", Steps: []SnapStep{g("lab-y.1"), g("lab-y.2")}}
	if got := ruleChecks(CheckDiagram(ws, ws.BeadID)); len(got) != 0 {
		t.Errorf("findings %v, want none", got)
	}
}

// il9.155.1: stage-done never fires on a gate, a bead with an open card for
// the user, or a bead with no (live) children; an ordinary container whose
// steps are all done still fires.
func TestPlanRules_StageDoneSkipsGatesCardsAndChildless(t *testing.T) {
	closedKid := SnapStep{BeadID: "", Title: "kid", State: "done", Status: "closed"}
	kid := func(id string) []SnapStep { k := closedKid; k.BeadID = id; return []SnapStep{k} }
	superseded := func(id string) []SnapStep {
		return []SnapStep{{BeadID: id, Title: "old", State: "done", Status: "closed", Superseded: true}}
	}
	ws := SnapWorkstream{Project: "lab", BeadID: "lab-z", Steps: []SnapStep{
		// the reported shape: an open user gate with an open card, its task moved away
		{BeadID: "lab-z.3", Title: "Workflow approved", State: "needs-user", Status: "open", Gate: true,
			Approvers: []string{"user"}, Criteria: "ok", Decision: &store.Decision{Question: "Approve?"}, Children: superseded("lab-z.3.1")},
		// a gate with a closed child and no card
		{BeadID: "lab-z.4", Title: "Spec approved", State: "not-started", Status: "open", Gate: true,
			Approvers: []string{"user"}, Criteria: "ok", Children: kid("lab-z.4.1")},
		// an ordinary bead with an open card whose children are done
		{BeadID: "lab-z.5", Title: "Pick a route", State: "needs-user", Status: "open",
			Decision: &store.Decision{Question: "Which?"}, Children: kid("lab-z.5.1")},
		// only superseded children = no children
		{BeadID: "lab-z.6", Title: "Empty", State: "in-progress", Status: "in_progress", Owner: "manager-lab-z", Children: superseded("lab-z.6.1")},
		// an ordinary container whose steps are all done: still a finding
		{BeadID: "lab-z.7", Title: "Build", State: "in-progress", Status: "in_progress", Owner: "manager-lab-z", Children: kid("lab-z.7.1")},
	}}
	var got []string
	for _, c := range ruleChecks(CheckDiagram(ws, ws.BeadID)) {
		if strings.HasPrefix(c, "stage-done ") {
			got = append(got, c)
		}
	}
	if want := []string{"stage-done lab-z.7"}; !slices.Equal(got, want) {
		t.Errorf("stage-done findings %v, want %v", got, want)
	}
}

// Completed children do not complete their stage's own checks or dependencies.
func TestPlanRules_StageDoneWaitsForGatesAndDependencies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gates   []SnapGate
		blocker SnapStep
		wait    string
		nested  bool // the stage sits under a parent that waits on the blocker
		want    bool
	}{
		{name: "operator gate", gates: []SnapGate{{Name: "reviewed", Verifier: "operator", Status: "open"}}},
		{name: "merge condition", gates: []SnapGate{{Name: "merged", Verifier: "auto", Status: "open"}}},
		{name: "failed CI", gates: []SnapGate{{Name: "ci", Verifier: "auto", Status: "failed"}}},
		{name: "passed gate", gates: []SnapGate{{Name: "ci", Verifier: "auto", Status: "passed"}}, want: true},
		{name: "open dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "open"}, wait: "lab-z.1"},
		{name: "inherited open dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "open"}, wait: "lab-z.1", nested: true},
		{name: "inherited closed dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "closed"}, wait: "lab-z.1", nested: true, want: true},
		{name: "closed dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "closed"}, wait: "lab-z.1", want: true},
		{name: "parked dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "deferred"}, wait: "lab-z.1", want: true},
		{name: "superseded dependency", blocker: SnapStep{BeadID: "lab-z.1", Status: "open", Superseded: true}, wait: "lab-z.1", want: true},
		{name: "unknown dependency", wait: "missing", want: true},
		{name: "ungated", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := SnapStep{BeadID: "lab-z.7", Title: "Build", Status: "open", State: "not-started", Gates: tc.gates,
				Children: []SnapStep{{BeadID: "lab-z.7.1", Status: "closed", State: "done"}}}
			top := s
			if tc.nested {
				top = SnapStep{BeadID: "lab-z.6", Title: "Release", Status: "open", State: "not-started", Children: []SnapStep{s}}
			}
			if tc.wait != "" {
				top.BlockedBy = []string{tc.wait}
			}
			ws := SnapWorkstream{Project: "lab", BeadID: "lab-z", Steps: []SnapStep{tc.blocker, top}}
			for _, scope := range []string{ws.BeadID, s.BeadID} {
				got := false
				for _, f := range CheckDiagram(ws, scope) {
					if f.Check == "stage-done" && f.Bead == s.BeadID {
						got = true
					}
				}
				if got != tc.want {
					t.Errorf("scope %s: stage-done = %v, want %v", scope, got, tc.want)
				}
			}
		})
	}
}
