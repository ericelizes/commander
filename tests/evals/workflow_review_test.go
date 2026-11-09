package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// The snapshot and diagram path uses fake GitHub throughout: no real agents,
// cockpit sessions, PRs, credentials or network calls.
func TestWorkflowReviewScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/workflow-review.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Steps []struct {
			Name                                               string
			Block, Complete, Closed, Unbound, Rebound, Invalid bool
			Head, State, Expected                              string
			Fix                                                string
			// NotFix must be absent from the finding (a valid current-head
			// binding is renewed, never rebound); Reason is the display text
			// every closed check must carry.
			NotFix string `json:"not_fix"`
			Reason string
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	const pr = "https://github.com/o/r/pull/7"
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "B", Before: "M", PR: pr})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range scenario.Steps {
		t.Run(step.Name, func(t *testing.T) {
			m := livedata.SnapStep{BeadID: "M", Title: "PR merged", Status: "open", Gate: true, PR: []string{"7"}, PRRepo: "o/r", WorkflowCheck: &workflowtemplate.Node{Bead: beads.Bead{ID: "M", Title: "PR merged", Status: "open", ParentID: "B", Labels: []string{"gate", "pr:7"}}}}
			if step.Closed {
				m.Status = "closed"
			}
			steps := []livedata.SnapStep{m}
			if step.Block {
				steps[0].BlockedBy = []string{"R"}
				r := livedata.SnapStep{BeadID: "R", Status: "open", WorkflowCheck: &workflowtemplate.Node{Bead: beads.Bead{ID: "R", ParentID: "B", Labels: inst.Labels, Description: fmt.Sprintf("for %s\n\n%s", pr, inst.Snapshot)}}}
				if step.Invalid {
					r.WorkflowCheck.Bead.Description = "for " + pr + "\n\n{invalid"
				}
				if step.Complete {
					r.Status = "closed"
				}
				if !step.Unbound {
					revision := "rev"
					if step.Rebound {
						revision = "rev2"
					}
					r.WorkflowCheck.Comments = []beads.Comment{{Text: workflowtemplate.WriterBinding{Revision: revision, PR: pr, Head: "head1", Tasks: []string{"writer"}}.Comment()}}
				}
				for i, s := range tpl.Steps {
					id := fmt.Sprintf("S%d", i)
					n := livedata.SnapStep{BeadID: id, Status: "open", WorkflowCheck: &workflowtemplate.Node{Bead: beads.Bead{ID: id, ParentID: "R", Labels: inst.Steps[i].Labels}}}
					if i > 0 {
						n.BlockedBy = []string{fmt.Sprintf("S%d", i-1)}
					}
					if step.Complete {
						n.Status = "closed"
						n.WorkflowCheck.Comments = []beads.Comment{{Text: workflowtemplate.Evidence{Binding: "rev", Instance: inst.Key, Step: s.ID, Verifier: s.Verifier, PR: pr, Head: "head1", Finding: "passed"}.Comment()}}
					}
					r.Children = append(r.Children, n)
				}
				steps = append(steps, r)
			}
			snap := livedata.Snapshot{Projects: []livedata.SnapProject{{Name: "p", Repo: "o/r"}}, Workstreams: []livedata.SnapWorkstream{{Project: "p", Repo: "o/r", BeadID: "B", Steps: steps}}}
			livedata.RefreshWorkflowHeads(&snap, func(string) (workflowtemplate.Head, bool) {
				return workflowtemplate.Head{SHA: step.Head, State: step.State}, true
			})
			if step.Block && step.Complete {
				d := livedata.Board(snap, 1)
				var check func([]livedata.WorkflowStep)
				seen := false
				check = func(ss []livedata.WorkflowStep) {
					for _, s := range ss {
						if s.ID == "R" && s.Review != nil {
							seen = true
							wantDone := len(tpl.Steps)
							if step.Expected != "" {
								wantDone = 0
							}
							if s.Review.Done != wantDone {
								t.Fatalf("display done %d want %d", s.Review.Done, wantDone)
							}
							for _, c := range s.Review.Checks {
								if step.Reason != "" && c.Text != step.Reason {
									t.Fatalf("check %s reads %q want %q", c.Step, c.Text, step.Reason)
								}
							}
						}
						check(s.Children)
					}
				}
				for _, ws := range d.Workstreams {
					check(ws.Workflow.Steps)
				}
				if !seen {
					t.Fatal("review block missing from display")
				}
			}

			// Both the scoped and whole-workstream diagram must enforce the rule.
			for _, scope := range []string{"B", "M"} {
				got := ""
				for _, f := range livedata.CheckDiagram(snap.Workstreams[0], scope) {
					if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
						got = f.Check
						if !strings.Contains(f.Why, step.Fix) || !strings.Contains(f.Fix, step.Fix) {
							t.Fatal(f)
						}
						if step.NotFix != "" && (strings.Contains(f.Why, step.NotFix) || strings.Contains(f.Fix, step.NotFix)) {
							t.Fatalf("finding names %q: %+v", step.NotFix, f)
						}
					}
				}
				if got != step.Expected {
					t.Fatalf("scope %s got %s want %s", scope, got, step.Expected)
				}
			}
		})
	}
}
