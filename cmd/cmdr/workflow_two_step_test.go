package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// The two-step review through the real close door: the Devin step passes once
// no Devin thread is open, the code-review step on one CLEAN report, neither
// needs a writer binding, and a push afterwards reopens nothing: the block and
// the merge target close on the new head with no rerun.
func TestTwoStepReviewThroughTheCloseDoor(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	pinOwnerRuns(t, true, true)
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user"}})
	res, err := plugWorkflow(ctx, mem, "/d", plugInstance(t, testPR))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Remaining) != 2 || res.Remaining[0].Step != "devin" || res.Remaining[1].Step != "code-review" {
		t.Fatalf("plugged steps: %+v", res.Remaining)
	}
	reviewed, head := strings.Repeat("a", 40), strings.Repeat("c", 40)
	devinOpen := true
	pinWorkflowGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" {
			return []byte(`{"headRefOid":"` + head + `","baseRefOid":"` + strings.Repeat("b", 40) + `","state":"OPEN","commits":[{"oid":"` + reviewed + `"},{"oid":"` + head + `"}],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
		}
		resolved := "true"
		if devinOpen {
			resolved = "false"
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"isResolved":` + resolved + `,"comments":{"totalCount":1,"nodes":[{"author":{"login":"devin-ai-integration"}}]}},{"isResolved":false,"comments":{"totalCount":1,"nodes":[{"author":{"login":"alice"}}]}}],"pageInfo":{"hasNextPage":false}}}}}}`), nil
	})
	devin, review := res.Remaining[0].BeadID, res.Remaining[1].BeadID

	if err := checkWorkflowClose(ctx, mem, "/d", "p", devin); err == nil || !strings.Contains(err.Error(), "Devin thread") {
		t.Fatalf("an open Devin thread passed: %v", err)
	}
	devinOpen = false // fixed and resolved, no reply; a person's open thread is not Devin's
	if err := checkWorkflowClose(ctx, mem, "/d", "p", devin); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", devin)

	if err := checkWorkflowClose(ctx, mem, "/d", "p", review); err == nil || !strings.Contains(err.Error(), "no report") {
		t.Fatalf("closed with no report: %v", err)
	}
	mem.AddComment(ctx, "/d", review, workflowtemplate.ReportMarker+"\nHead: "+reviewed+"\nexecution=blocked coverage=partial: provider timed out")
	if err := checkWorkflowClose(ctx, mem, "/d", "p", review); err == nil || !strings.Contains(err.Error(), "not CLEAN") {
		t.Fatalf("a blocked review passed: %v", err)
	}
	mem.AddComment(ctx, "/d", review, workflowtemplate.ReportMarker+"\nHead: "+reviewed+"\nmajor: a.go:3 nil map, fixed in "+head+"\nCLEAN")
	if err := checkWorkflowClose(ctx, mem, "/d", "p", review); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", review)

	// A fix pushed after the report: nothing reruns.
	head = strings.Repeat("d", 40)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Container); err != nil {
		t.Fatalf("the block went stale on a push: %v", err)
	}
	mem.Close(ctx, "/d", res.Container)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
		t.Fatalf("the merge target went stale on a push: %v", err)
	}
	if b, _ := mem.Show(ctx, "/d", res.Container); workflowtemplate.Label(*b, workflowtemplate.LabelVersion) != "2" {
		t.Fatalf("block version: %v", b.Labels)
	}
}

// workflow list prints each catalog entry with when to use and skip it;
// workflow show prints one whole template.
func TestWorkflowCatalogCommands(t *testing.T) {
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runWorkflow([]string{"list"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatal(errOut)
	}
	out, _ := read()
	var entries []struct {
		ID          string   `json:"id"`
		AppliesTo   string   `json:"applies_to"`
		Summary     string   `json:"summary"`
		UseWhen     []string `json:"use_when"`
		SkipWhen    []string `json:"skip_when"`
		CatalogOnly bool     `json:"catalog_only"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = e.CatalogOnly
		if e.AppliesTo == "" || e.Summary == "" || len(e.UseWhen) == 0 || len(e.SkipWhen) == 0 {
			t.Fatalf("entry without catalog words: %+v", e)
		}
	}
	if only, ok := ids["pr-review"]; !ok || only {
		t.Fatalf("pr-review: %v", ids)
	}
	if !ids["forge-app-release"] || strings.Contains(out, `"steps"`) {
		t.Fatalf("list: %s", out)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runWorkflow([]string{"show", "forge-app-release"}, stdout, stderr); code != 0 {
		t.Fatal("show refused")
	}
	if out, _ = read(); !strings.Contains(out, `"owner-go"`) || !strings.Contains(out, `"optional": true`) {
		t.Fatalf("show: %s", out)
	}
}
