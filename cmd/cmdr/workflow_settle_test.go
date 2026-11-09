package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// settleProject is a plugged block before merge gate M, its first step checked
// while the PR was open, on the project the verbs resolve.
func settleProject(t *testing.T) (*beads.MemClient, *workflowPlugResult) {
	t.Helper()
	ctx := context.Background()
	mem := setupGateProject(t)
	pinOwnerRuns(t, true, true)
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user"}})
	tpl, err := workflowtemplate.Load("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "manifest", Parent: "B", Before: "M", PR: testPR})
	if err != nil {
		t.Fatal(err)
	}
	res, err := plugWorkflow(ctx, mem, "/d", inst)
	if err != nil {
		t.Fatal(err)
	}
	seedWriterBinding(mem, res.Container, "head1")
	pinWorkflowGH(t, workflowFakeGH)
	first := res.Remaining[0].BeadID
	if err := checkWorkflowClose(ctx, mem, "/d", "manifest", first); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", first)
	return mem, res
}

func mergedGH(state string, fail bool) workflowtemplate.GHRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("gh: rate limited")
		}
		return json.Marshal(map[string]string{"headRefOid": "head9", "state": state})
	}
}

func settleBlock(t *testing.T, block, reason string) (int, string, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	code := runWorkflow([]string{"settle-block", "--project", "manifest", "--block", block, "--reason", reason}, stdout, stderr)
	out, errOut := read()
	return code, out, errOut
}

func commentsOf(t *testing.T, mem *beads.MemClient, id string) []string {
	t.Helper()
	cs, _ := mem.Comments(context.Background(), "/d", id)
	var out []string
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

const settleReason = "the user merged it before the review ran"

func TestSettleBlockClosesTheRestAndKeepsHistory(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-B")
	ctx := context.Background()
	mem, res := settleProject(t)
	first := res.Remaining[0].BeadID
	before := commentsOf(t, mem, first)
	pinWorkflowGH(t, mergedGH("MERGED", false))

	// The normal close of an open step is still refused, and names the way out.
	err := checkWorkflowClose(ctx, mem, "/d", "manifest", res.Remaining[1].BeadID)
	if err == nil || !strings.Contains(err.Error(), "cmdr workflow settle-block --project manifest --block "+res.Container) {
		t.Fatalf("refusal names no settle: %v", err)
	}

	code, out, errOut := settleBlock(t, res.Container, settleReason)
	if code != 0 {
		t.Fatal(errOut)
	}
	var got workflowSettleResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(out, err)
	}
	if !got.BlockClosed || got.Head != "head9" || len(got.Kept) != 1 || got.Kept[0] != first || len(got.Settled) != len(res.Remaining)-1 {
		t.Fatalf("%+v", got)
	}
	// Earlier evidence stays as it was: nothing appended, nothing rewritten.
	if after := commentsOf(t, mem, first); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("kept step changed: %v -> %v", before, after)
	}
	for _, s := range res.Remaining[1:] {
		b, _ := mem.Show(ctx, "/d", s.BeadID)
		if b.Status != "closed" {
			t.Fatalf("%s reads %s", s.Step, b.Status)
		}
		cs := commentsOf(t, mem, s.BeadID)
		e, ok := workflowtemplate.ParseEvidence(cs[len(cs)-1])
		if !ok || e.Verifier != workflowtemplate.SettledVerifier || e.Head != "head9" || e.Proof != nil || e.Binding != "" ||
			!strings.Contains(e.Finding, "review not completed") || !strings.Contains(e.Finding, settleReason) {
			t.Fatalf("%s evidence %+v", s.Step, e)
		}
	}
	if b, _ := mem.Show(ctx, "/d", res.Container); b.Status != "closed" {
		t.Fatal("block left open")
	}
	// The block's own binding was never rewritten (no writers fabricated).
	for _, c := range commentsOf(t, mem, res.Container) {
		if strings.HasPrefix(c, "workflow-writers@v1") && strings.Contains(c, "head9") {
			t.Fatal("fabricated a writer binding", c)
		}
	}
	// The merge gate closes as before: its PR merged.
	if err := checkWorkflowClose(ctx, mem, "/d", "manifest", "M"); err != nil {
		t.Fatal(err)
	}
	// A rerun records nothing new.
	n := len(commentsOf(t, mem, res.Remaining[2].BeadID))
	if code, _, errOut := settleBlock(t, res.Container, settleReason); code != 0 {
		t.Fatal(errOut)
	}
	if len(commentsOf(t, mem, res.Remaining[2].BeadID)) != n {
		t.Fatal("rerun recorded the settle twice")
	}
}

func TestSettleBlockRefusesBeforeAnyWrite(t *testing.T) {
	for _, c := range []struct {
		name, role, reason, want string
		gh                       workflowtemplate.GHRunner
		edit                     func(*beads.MemClient)
	}{
		{name: "open PR", role: "manager-B", gh: mergedGH("OPEN", false), want: "not merged"},
		{name: "closed unmerged PR", role: "manager-B", gh: mergedGH("CLOSED", false), want: "not merged"},
		{name: "GitHub unreadable", role: "manager-B", gh: mergedGH("MERGED", true), want: "rate limited"},
		{name: "Operator", role: "operator-x", gh: mergedGH("MERGED", false), want: "Operators cannot"},
		{name: "another Manager", role: "manager-other", gh: mergedGH("MERGED", false), want: "belongs to manager-B"},
		{name: "short reason", role: "manager-B", reason: "merged", gh: mergedGH("MERGED", false), want: "15+"},
		{name: "target with another live PR", role: "manager-B", want: "still has a live PR",
			gh: func(_ context.Context, args ...string) ([]byte, error) {
				state := "MERGED"
				if args[2] != testPR {
					state = "OPEN"
				}
				return json.Marshal(map[string]string{"headRefOid": "head9", "state": state})
			},
			edit: func(mem *beads.MemClient) {
				for _, b := range mem.Beads {
					if b.ID == "M" {
						b.Labels = append(b.Labels, "pr:o/other#7")
					}
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem, res := settleProject(t)
			if c.edit != nil {
				c.edit(mem)
			}
			pinWorkflowGH(t, c.gh)
			t.Setenv("COMMANDER_ROLE", c.role)
			reason := c.reason
			if reason == "" {
				reason = settleReason
			}
			before := map[string]int{}
			for _, s := range res.Remaining {
				before[s.BeadID] = len(commentsOf(t, mem, s.BeadID))
			}
			code, _, errOut := settleBlock(t, res.Container, reason)
			if code == 0 || !strings.Contains(errOut, c.want) {
				t.Fatalf("%d %s", code, errOut)
			}
			for _, s := range res.Remaining[1:] {
				b, _ := mem.Show(context.Background(), "/d", s.BeadID)
				if b.Status == "closed" || len(commentsOf(t, mem, s.BeadID)) != before[s.BeadID] {
					t.Fatalf("refusal wrote on %s", s.Step)
				}
			}
		})
	}
}

func TestSettleBlockByCommanderAndTheUser(t *testing.T) {
	for _, role := range []string{"commander", ""} {
		t.Run("role="+role, func(t *testing.T) {
			_, res := settleProject(t)
			pinWorkflowGH(t, mergedGH("MERGED", false))
			t.Setenv("COMMANDER_ROLE", role)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			if code, _, errOut := settleBlock(t, res.Container, settleReason); code != 0 {
				t.Fatal(errOut)
			}
		})
	}
}

// A refused block close leaves the steps settled and says how to finish.
func TestSettleBlockReportsARefusedBlockClose(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-B")
	mem, res := settleProject(t)
	pinWorkflowGH(t, mergedGH("MERGED", false))
	old := settleCloseBlock
	settleCloseBlock = func(string, string, string) error { return errors.New("gate not passed") }
	t.Cleanup(func() { settleCloseBlock = old })
	code, out, errOut := settleBlock(t, res.Container, settleReason)
	if code == 0 || !strings.Contains(errOut, "internal-close-objective --project manifest --bead "+res.Container) || !strings.Contains(out, `"block_closed":false`) {
		t.Fatal(code, out, errOut)
	}
	if b, _ := mem.Show(context.Background(), "/d", res.Remaining[5].BeadID); b.Status != "closed" {
		t.Fatal("steps not settled")
	}
}
