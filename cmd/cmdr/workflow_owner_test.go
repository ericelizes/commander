package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestPlanningReviewOwnerScenario(t *testing.T) {
	var sc struct {
		Cases []struct {
			Role, Owner, Run, Session, Manager string
			OK                                 bool
		}
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/planning-review-owner.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, c := range sc.Cases {
		for _, verb := range []string{"bind", "settle"} {
			t.Run(verb+"/"+c.Role+"/"+c.Owner+"/"+c.Run+c.Session+c.Manager, func(t *testing.T) {
				ctx := context.Background()
				var mem *beads.MemClient
				var block string
				if verb == "bind" {
					mem = &beads.MemClient{}
					res := seedReviewWorkflow(t, mem)
					block = res.Container
					head := strings.Repeat("a", 40)
					pinWorkflowGH(t, func(ctx context.Context, args ...string) ([]byte, error) {
						out, e := workflowFakeGH(ctx, args...)
						return []byte(strings.ReplaceAll(string(out), "head1", head)), e
					})
				} else {
					m, res := settleProject(t)
					mem = m
					block = res.Container
					pinWorkflowGH(t, mergedGH("MERGED", false))
					old := settleCloseBlock
					settleCloseBlock = func(string, string, string) error { return nil }
					t.Cleanup(func() { settleCloseBlock = old })
				}
				for _, b := range mem.Beads {
					if b.ID == block {
						b.PlanningCommander = c.Owner
					}
				}
				t.Setenv("COMMANDER_ROLE", c.Role)
				t.Setenv("COMMANDER_RUN_ID", c.Run)
				t.Setenv("COMMANDER_RUN_SESSION", c.Session)
				t.Setenv("COMMANDER_MANAGER_ROLE", c.Manager)
				before, _ := mem.Comments(ctx, "/d", block)
				if verb == "bind" {
					_, err = bindWorkflowWriters(ctx, mem, "/d", "p", block, strings.Repeat("a", 40), []string{"writer-task"}, nil)
				} else {
					_, err = settleWorkflowBlock(ctx, mem, "/d", "manifest", block, settleReason)
				}
				if (err == nil) != c.OK {
					t.Fatalf("ok=%v error=%v", c.OK, err)
				}
				if !c.OK {
					after, _ := mem.Comments(ctx, "/d", block)
					if len(after) != len(before) {
						t.Fatal("refusal wrote block")
					}
				}
			})
		}
	}
}
