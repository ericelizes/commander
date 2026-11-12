package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// The real approval/close handler must not require GitHub or a planned block.
func TestOptionalPRReviewScenario(t *testing.T) {
	var sc struct {
		Cases []struct{ Name, Label string }
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/optional-pr-review.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, c := range sc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			mem := seedNoPRShip(t)
			g := mergedGate(t, mem)
			if c.Label != "" {
				if err := mem.SetLabel(context.Background(), "/d", g.ID, c.Label, true); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) {
				calls++
				return nil, fmt.Errorf("GitHub unavailable")
			})
			code, _, errOut := gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--evidence", "User approved this merge")
			if code != 0 {
				t.Fatalf("approval/close: %d %s", code, errOut)
			}
			if g := mergedGate(t, mem); g.Status != "closed" {
				t.Fatalf("gate remains %s", g.Status)
			}
			if calls != 0 {
				t.Fatalf("unplanned review read GitHub %d times", calls)
			}
			for _, b := range mem.Beads {
				if workflowLabelForTest(*b) {
					t.Fatal("review workflow was manufactured")
				}
			}
		})
	}
}

func workflowLabelForTest(b beads.Bead) bool {
	for _, l := range b.Labels {
		if len(l) >= len("workflow-template:") && l[:len("workflow-template:")] == "workflow-template:" {
			return true
		}
	}
	return false
}

func TestMergedBlockDoesNotClaimReviewForUnplannedPR(t *testing.T) {
	mem := &beads.MemClient{}
	seedReviewWorkflow(t, mem)
	mem.SetLabel(context.Background(), "/d", "M", "pr:o/other#9", true)
	pinWorkflowRepo(t, "o/r")
	var asked []string
	pinWorkflowGH(t, prStateGH("MERGED", &asked))
	if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "M"); err != nil {
		t.Fatal(err)
	}
	comments, _ := mem.Comments(context.Background(), "/d", "M")
	found := false
	for _, c := range comments {
		if strings.Contains(c.Text, "PR merged: the review block was not checked") {
			found = true
		}
		if strings.Contains(c.Text, "topology and current-head evidence checked") {
			t.Fatal("settled block claimed a completed review")
		}
	}
	if !found {
		t.Fatal("missing honest settlement evidence")
	}
}
