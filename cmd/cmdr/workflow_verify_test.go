package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// pinOwnerRuns supplies exact-task provenance independently of mutable owners.
func pinOwnerRuns(t *testing.T, found, readable bool) {
	t.Helper()
	old := workflowTaskRoles
	workflowTaskRoles = func(_ context.Context, project, task string) ([]string, error) {
		if !readable {
			return nil, errors.New("run table unavailable")
		}
		if !found {
			return nil, fmt.Errorf("task %s matches no Operator run", task)
		}
		return []string{"operator-" + task}, nil
	}
	t.Cleanup(func() { workflowTaskRoles = old })
}
func seedWriterBinding(mem *beads.MemClient, block, head string) {
	mem.Seed(beads.Bead{ID: "writer-task", Status: "closed", Labels: []string{"owner:manager-B"}})
	mem.SetParent(context.Background(), "/d", "writer-task", "B")
	mem.AddComment(context.Background(), "/d", block, workflowtemplate.WriterBinding{Revision: head, PR: testPR, Head: head, Tasks: []string{"writer-task"}}.Comment())
}

func seedReviewWorkflow(t *testing.T, mem *beads.MemClient) *workflowPlugResult {
	t.Helper()
	pinOwnerRuns(t, true, true)
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user"}})
	res, err := plugWorkflow(context.Background(), mem, "/d", legacyInstance(t, testPR))
	if err != nil {
		t.Fatal(err)
	}
	seedWriterBinding(mem, res.Container, "head1")
	return res
}
func workflowFakeGH(ctx context.Context, args ...string) ([]byte, error) {
	if args[0] == "pr" {
		return []byte(`{"headRefOid":"head1","state":"OPEN","commits":[{"oid":"head1"}],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`), nil
	}
	return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`), nil
}
func pinWorkflowGH(t *testing.T, run workflowtemplate.GHRunner) {
	t.Helper()
	old := workflowGH
	workflowGH = run
	t.Cleanup(func() { workflowGH = old })
}
func TestWorkflowCloseChecksAndEvidence(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	first := res.Remaining[0].BeadID
	if err := checkWorkflowClose(ctx, mem, "/d", "p", first); err != nil {
		t.Fatal(err)
	}
	cs, _ := mem.Comments(ctx, "/d", first)
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	e, ok := workflowtemplate.ParseEvidence(cs[0].Text)
	if !ok || e.Head != "head1" || e.Step != "devin-comments" {
		t.Fatal(cs)
	}
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[1].BeadID); err == nil || !strings.Contains(err.Error(), "predecessor") {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", first)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[1].BeadID); err == nil || !strings.Contains(err.Error(), "proof required") {
		t.Fatal(err)
	}
	mem.Seed(beads.Bead{ID: "review-task", Status: "closed", Labels: []string{"owner:operator-independent"}})
	mem.SetParent(ctx, "/d", "review-task", "B")
	mem.AddComment(ctx, "/d", "review-task", "Head: head1\nno findings\nCLEAN")
	raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: "head1", NoFindings: true})
	mem.AddComment(ctx, "/d", res.Remaining[1].BeadID, workflowtemplate.ProofPrefix+string(raw))
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Remaining[1].BeadID); err != nil {
		t.Fatal(err)
	}
	workflowGH = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") }
	if err := checkWorkflowClose(ctx, mem, "/d", "p", first); err == nil {
		t.Fatal("offline close accepted")
	}
}
func TestWorkflowCloseDoorRefusesMissingBlock(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "step", Labels: []string{"workflow-template:pr-review", "workflow-step:review"}}, ListResult: []beads.Bead{{ID: "step", Labels: []string{"workflow-template:pr-review", "workflow-step:review"}}}}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "step"}, stdout, stderr); code == 0 {
		t.Fatal("closed")
	}
	_, errOut := read()
	if !strings.Contains(errOut, "no block") {
		t.Fatal(errOut)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "Close:") {
			t.Fatal(c)
		}
	}
}
func TestWorkflowApproveDoorRefusesMissingBlock(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "step", Labels: []string{"workflow-template:pr-review", "workflow-step:review"}}, ListResult: []beads.Bead{{ID: "step", Labels: []string{"workflow-template:pr-review", "workflow-step:review"}}}}
	setupObjectiveProject(t, mock)
	ctx := context.Background()
	st, done, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProjectByName(ctx, "manifest")
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "step", Ask: "Approve", Note: "review"}); err != nil {
		t.Fatal(err)
	}
	done()
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "step", "--action", "approve"}, stdout, stderr); code == 0 {
		t.Fatal("approved")
	}
	_, errOut := read()
	if !strings.Contains(errOut, "no block") {
		t.Fatal(errOut)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "Close:") {
			t.Fatal(c)
		}
	}
}
func TestWorkflowMergedGatePassWithoutReview(t *testing.T) {
	mem := setupGateProject(t)
	code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--check", "PR merged", "--verifier", "user")
	if code != 0 {
		t.Fatal(errOut)
	}
	code, _, errOut = gateCmd(t, "pass", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--evidence", "done")
	if code != 0 {
		t.Fatal(code, errOut)
	}
	for _, b := range mem.Beads {
		if hasLabel(b.Labels, "gate-name:merged") && b.Status != "closed" {
			t.Fatal("gate did not close")
		}
	}
}
func TestWorkflowDependencyReadFailsClosed(t *testing.T) {
	if !dependsOn(context.Background(), &beads.MockClient{DepsErr: errors.New("busy")}, "/d", "a", "b") {
		t.Fatal("failed open")
	}
}

// A complete version 1 block stays complete after a push: nothing reruns.
func TestWorkflowCompleteBlockStaysCompleteOnANewHead(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	for _, r := range []string{"reviewer-one", "reviewer-two"} {
		mem.Seed(beads.Bead{ID: r, Status: "closed", Labels: []string{"owner:operator-" + r}})
		mem.SetParent(ctx, "/d", r, "B")
		mem.AddComment(ctx, "/d", r, "Head: head1\nno findings\nCLEAN")
	}
	for i, s := range res.Remaining {
		if i > 0 && i < 5 {
			review := "reviewer-one"
			if i >= 3 {
				review = "reviewer-two"
			}
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: review, Head: "head1", NoFindings: true})
			mem.AddComment(ctx, "/d", s.BeadID, workflowtemplate.ProofPrefix+string(raw))
		}
		if err := checkWorkflowClose(ctx, mem, "/d", "p", s.BeadID); err != nil {
			t.Fatalf("step %s: %v", s.Step, err)
		}
		mem.Close(ctx, "/d", s.BeadID)
	}
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Container); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", res.Container)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
		t.Fatal(err)
	}
	workflowGH = func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := workflowFakeGH(ctx, args...)
		return []byte(strings.ReplaceAll(string(out), "head1", "head2")), err
	}
	if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
		t.Fatalf("a passed block went stale on a push: %v", err)
	}
}

// After a new push nothing is stale. Closing a closed step again still runs the
// same fresh check (a refused one records nothing) and records it for the new head.
func TestWorkflowClosedStepsRenewThroughTheCloseDoor(t *testing.T) {
	ctx := context.Background()
	mem := setupGateProject(t)
	allowUnheld(t, "the fixture's later steps hold nothing; the renewal is under test") // the user's closes leave the next step to the Manager
	pinOwnerRuns(t, true, true)
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user"}})
	tpl, err := legacyTemplateFile()
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
	closeStep := func(id string) (int, string) {
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", id}, stdout, stderr)
		_, errOut := read()
		return code, errOut
	}
	review := func(head string) {
		for i, s := range res.Remaining {
			if i == 0 || i == 5 {
				continue
			}
			r := "reviewer-one-" + head
			if i >= 3 {
				r = "reviewer-two-" + head
			}
			mem.Seed(beads.Bead{ID: r, Status: "closed", Labels: []string{"owner:operator-" + r}})
			mem.SetParent(ctx, "/d", r, "B")
			mem.AddComment(ctx, "/d", r, "Head: "+head+"\nno findings\nCLEAN")
			raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: r, Head: head, NoFindings: true})
			mem.AddComment(ctx, "/d", s.BeadID, workflowtemplate.ProofPrefix+string(raw))
		}
	}
	review("head1")
	for _, s := range res.Remaining {
		if code, errOut := closeStep(s.BeadID); code != 0 {
			t.Fatalf("first close of %s: %s", s.Step, errOut)
		}
	}
	if code, errOut := closeStep(res.Container); code != 0 {
		t.Fatal(errOut)
	}

	workflowGH = func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := workflowFakeGH(ctx, args...)
		return []byte(strings.ReplaceAll(string(out), "head1", "head2")), err
	}
	if err := checkWorkflowClose(ctx, mem, "/d", "manifest", "M"); err != nil {
		t.Fatalf("a new head made passed steps stale: %v", err)
	}

	// Still fresh: a re-close whose check fails is refused and records nothing.
	green := workflowGH
	workflowGH = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "pr" {
			return []byte(`{"headRefOid":"head2","state":"OPEN","statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"}]}`), nil
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`), nil
	}
	seedWriterBinding(mem, res.Container, "head2")
	review("head2")
	for _, s := range res.Remaining[:5] {
		if code, errOut := closeStep(s.BeadID); code != 0 {
			t.Fatalf("renew %s: %s", s.Step, errOut)
		}
	}
	final := res.Remaining[5].BeadID
	if code, errOut := closeStep(final); code == 0 || !strings.Contains(errOut, "CI is not green") {
		t.Fatalf("red CI renewed the final check: %d %s", code, errOut)
	}
	workflowGH = green
	if code, errOut := closeStep(final); code != 0 {
		t.Fatal(errOut)
	}
	if err := checkWorkflowClose(ctx, mem, "/d", "manifest", "M"); err != nil {
		t.Fatalf("every step renewed: %v", err)
	}
	for _, s := range res.Remaining {
		if b, _ := mem.Show(ctx, "/d", s.BeadID); b.Status != "closed" {
			t.Fatalf("%s reads %s", s.Step, b.Status)
		}
	}
}

func pinWorkflowRepo(t *testing.T, repo string) {
	t.Helper()
	old := workflowRepo
	workflowRepo = func(string) string { return repo }
	t.Cleanup(func() { workflowRepo = old })
}

// prStateGH answers every PR with one state and records the PR it was asked about.
func prStateGH(state string, asked *[]string) workflowtemplate.GHRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		*asked = append(*asked, args[2])
		return json.Marshal(map[string]string{"headRefOid": "head1", "state": state})
	}
}

// The limit CLAUDE.md states: clear a live PR's label, label a merged PR, close. The
// merge rule reads what is left, so the gate closes. The swap is not detected; what
// this pins is that it cannot happen without a reason on the record.
func TestWorkflowCloseLabelSwapIsRecorded(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	ctx := context.Background()
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "ms-1.9", Title: "PR merged", Status: "open", Labels: []string{"gate", "gate-name:merged", "pr:7"}})
	setupObjectiveProject(t, mem)
	pinWorkflowRepo(t, "acme/widgets")
	pinWorkflowGH(t, func(_ context.Context, args ...string) ([]byte, error) {
		state := "OPEN"
		if strings.Contains(strings.Join(args, " "), "3366") {
			state = "MERGED"
		}
		return json.Marshal(map[string]string{"headRefOid": "head1", "state": state})
	})
	closeGate := func() int {
		stdout, stderr, _ := withTempOutputFiles(t)
		return runInternalCloseObjective([]string{"--project", "manifest", "--bead", "ms-1.9", "--reason", "merged"}, stdout, stderr)
	}
	if code, _ := runReviewNone(t, "ms-1.9"); code == 0 {
		t.Fatal("the swap's first step needed no reason")
	}
	const why = "wrong PR on this gate"
	if code, errOut := runReviewNone(t, "ms-1.9", "--reason", why); code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.9", "--review", "3366"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("--review 3366: exit=%d: %s", code, errOut)
	}
	if code := closeGate(); code != 0 {
		t.Fatalf("the documented limit changed: exit=%d", code)
	}
	if b, _ := mem.Show(ctx, "/d", "ms-1.9"); b.Status != "closed" {
		t.Fatalf("status: %s", b.Status)
	}
	notes := reviewClearNotes(t, mem, "ms-1.9")
	if len(notes) != 1 || !strings.Contains(notes[0], "pr:7") || !strings.Contains(notes[0], why) || !strings.Contains(notes[0], "manager-ms-1") {
		t.Fatalf("the swap left no record: %q", notes)
	}
}

// #2: pr: on an ordinary step says which PR it tracks, nothing more.
func TestWorkflowClosePlainPRStepClosesWithNoGitHub(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w.1", Title: "Worker retry with backoff", Labels: []string{"kind:objective", "in-review", "pr:1234"}})
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") })
	if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.1"); err != nil {
		t.Fatal(err)
	}
}

// Work about merging is not the merge: an ordinary pr: step closes with its PR open.

// A merge without a tracked PR or review block closes normally.
func TestWorkflowClosePlannedMergeWithoutReview(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w.1", Title: "PR merged", Labels: []string{"gate", "gate-name:merged"}})
	pinWorkflowRepo(t, "o/r")
	var asked []string
	pinWorkflowGH(t, prStateGH("OPEN", &asked))
	err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCloseMergeWorkStepIsNoTarget(t *testing.T) {
	for _, title := range []string{"Fix merge conflicts", "Merge main into feature", "merge queue setup", "pre-merge checks"} {
		mem := &beads.MemClient{}
		mem.Seed(beads.Bead{ID: "w.1", Title: title, Labels: []string{"kind:objective", "pr:12"}})
		pinWorkflowRepo(t, "o/r")
		var asked []string
		pinWorkflowGH(t, prStateGH("OPEN", &asked))
		if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.1"); err != nil {
			t.Fatalf("%q: %v", title, err)
		}
	}
	for _, title := range []string{"Merge PR", "Merged", "merge to main"} {
		mem := &beads.MemClient{}
		mem.Seed(beads.Bead{ID: "w.1", Title: title, Labels: []string{"kind:objective", "pr:12"}})
		pinWorkflowRepo(t, "o/r")
		var asked []string
		pinWorkflowGH(t, prStateGH("OPEN", &asked))
		if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.1"); err != nil {
			t.Fatalf("%q: %v", title, err)
		}
	}
}

// A merge title does not create a mandatory review workflow.
func TestWorkflowCloseMergeTitlesDoNotRequireReview(t *testing.T) {
	for _, title := range []string{"Squash and merge PR 12", "Review and merge PR 12", "Merge the API PR", "Merge this PR"} {
		mem := &beads.MemClient{}
		mem.Seed(beads.Bead{ID: "w.1", Title: title, Labels: []string{"kind:objective", "pr:12"}})
		pinWorkflowRepo(t, "o/r")
		var asked []string
		pinWorkflowGH(t, prStateGH("OPEN", &asked))
		err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.1")
		if err != nil {
			t.Fatalf("%q: %v", title, err)
		}
	}
}

// A merge step can close with its PR open and no review block.
func TestWorkflowCloseMergeTitledPRStep(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w.2", Title: "Widget service PR 1234 merged", Labels: []string{"kind:objective", "pr:1234"}})
	pinWorkflowRepo(t, "o/r")
	var asked []string
	pinWorkflowGH(t, prStateGH("OPEN", &asked))
	err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.2")
	if err != nil {
		t.Fatalf("unmerged: %v", err)
	}
	pinWorkflowGH(t, prStateGH("MERGED", &asked))
	if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "w.2"); err != nil {
		t.Fatalf("merged: %v", err)
	}
}

// F6: with the block's PR open, nothing but that block lets the target close.
func TestWorkflowCloseMergedExemptionJudgesTheBlocksPR(t *testing.T) {
	const merged = "https://github.com/o/r/pull/1"
	statesGH := func(_ context.Context, args ...string) ([]byte, error) {
		state := "OPEN"
		if args[2] == merged {
			state = "MERGED"
		}
		return json.Marshal(map[string]string{"headRefOid": "head1", "state": state})
	}
	for _, c := range []struct {
		name string
		edit func(m *beads.Bead)
	}{
		{"URL in the description", func(m *beads.Bead) { m.Description = "follows " + merged }},
		{"merged pr: label", func(m *beads.Bead) { m.Labels = append(m.Labels, "pr:o/r#1") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			mem := &beads.MemClient{}
			seedReviewWorkflow(t, mem)
			pinWorkflowRepo(t, "o/r")
			pinWorkflowGH(t, statesGH)
			for _, b := range mem.Beads {
				if b.ID == "M" {
					c.edit(b)
				}
			}
			err := checkWorkflowClose(context.Background(), mem, "/d", "p", "M")
			if err == nil || !strings.Contains(err.Error(), "MERGE_REVIEW_INCOMPLETE") {
				t.Fatalf("%v", err)
			}
		})
	}
	t.Run("a gate check names no PR", func(t *testing.T) {
		mem := &beads.MemClient{}
		mem.Seed(beads.Bead{ID: "G", Title: "PR merged", Description: `gh pr view ` + merged, Labels: []string{"gate"}})
		pinWorkflowRepo(t, "o/r")
		pinWorkflowGH(t, statesGH)
		err := checkWorkflowClose(context.Background(), mem, "/d", "p", "G")
		if err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// F6: a block for an already merged PR would exempt its target.
func TestWorkflowPlugRefusesAMergedPR(t *testing.T) {
	setupGateProject(t)
	var asked []string
	pinWorkflowGH(t, prStateGH("MERGED", &asked))
	stdout, stderr, read := withTempOutputFiles(t)
	code := runWorkflow([]string{"plug", "pr-review", "--project", "manifest", "--parent", "B", "--before", "M", "--pr", testPR}, stdout, stderr)
	_, errOut := read()
	if code == 0 || !strings.Contains(errOut, "already merged") {
		t.Fatal(code, errOut)
	}
}

// F10: the door judges comments and edges read fresh, not the display's lagging export.
func TestWorkflowCloseReadsFresh(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	first, second := res.Remaining[0].BeadID, res.Remaining[1].BeadID
	mem.Close(ctx, "/d", first)
	raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: "head1", NoFindings: true})
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+string(raw))
	mem.Seed(beads.Bead{ID: "review-task", Status: "closed", Labels: []string{"owner:operator-independent"}})
	mem.SetParent(ctx, "/d", "review-task", "B")
	mem.AddComment(ctx, "/d", "review-task", "Head: head1\nCLEAN")
	mem.Seed(beads.Bead{ID: "far", Status: "open"})
	for i := range 1042 {
		mem.Seed(beads.Bead{ID: fmt.Sprintf("unrelated-%d", i), Status: "open"})
	}
	stale := noBulkWorkflowDetails{MemClient: mem}
	old := workflowFreshDetails
	t.Cleanup(func() { workflowFreshDetails = old })
	var read []string
	workflowFreshDetails = func(ctx context.Context, _ beads.Client, dir string, ids []string) (map[string]beads.Detail, error) {
		read = append(read, ids...)
		return mem.Details(ctx, dir, ids)
	}
	if err := checkWorkflowClose(ctx, stale, "/d", "p", second); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(read, second) || !slices.Contains(read, first) || !slices.Contains(read, res.Container) {
		t.Fatalf("read fresh: %v", read)
	}
	if slices.Contains(read, "far") || len(read) > len(res.Remaining)+12 {
		t.Fatalf("read an unrelated bead fresh: %v", read)
	}
}

// laggingDetails is an export that has not caught up: it holds no comments or waits.
type laggingDetails struct{ *beads.MemClient }

func (l laggingDetails) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	out, err := l.MemClient.Details(ctx, dir, ids)
	for id, d := range out {
		d.Comments = nil
		d.Deps = slices.DeleteFunc(d.Deps, func(e beads.Dep) bool { return e.Type == "blocks" })
		out[id] = d
	}
	return out, err
}

// F9: a malformed proof is ignored; a good one still works, and none says what was ignored.
func TestWorkflowCloseMalformedProofDoesNotBlockForGood(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	mem.Close(ctx, "/d", res.Remaining[0].BeadID)
	second := res.Remaining[1].BeadID
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+"{oops")
	err := checkWorkflowClose(ctx, mem, "/d", "p", second)
	if err == nil || !strings.Contains(err.Error(), "proof required") || !strings.Contains(err.Error(), "malformed review proof") {
		t.Fatal(err)
	}
	mem.Seed(beads.Bead{ID: "review-task", Status: "closed", Labels: []string{"owner:operator-independent"}})
	mem.SetParent(ctx, "/d", "review-task", "B")
	mem.AddComment(ctx, "/d", "review-task", "Head: head1\nCLEAN\n")
	raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: "head1", NoFindings: true})
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+string(raw))
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+"[1]")
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+"{}")
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+"null")
	if err := checkWorkflowClose(ctx, mem, "/d", "p", second); err != nil {
		t.Fatal(err)
	}
}

// #2: waiting on a review block is what guards a step, whatever it is called.
func TestWorkflowCloseStepWaitingOnBlockIsGuarded(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "Ship the thing", Labels: []string{"kind:objective"}})
	if _, err := plugWorkflow(context.Background(), mem, "/d", plugInstance(t, testPR)); err != nil {
		t.Fatal(err)
	}
	pinWorkflowRepo(t, "o/r")
	var asked []string
	pinWorkflowGH(t, prStateGH("OPEN", &asked))
	err := checkWorkflowClose(context.Background(), mem, "/d", "p", "M")
	if err == nil || !strings.Contains(err.Error(), "MERGE_REVIEW_INCOMPLETE") {
		t.Fatalf("guarded step: %v", err)
	}
}

// Downstream of a block plugged before another bead. That block is an ordinary blocker for it, and it closes normally.
func TestWorkflowCloseDownstreamOfAnotherTargetsBlockCloses(t *testing.T) {
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	mem.Seed(beads.Bead{ID: "after", ParentID: "B", Title: "Follow-up check", Labels: []string{"kind:objective"}})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "after", DependsOnID: res.Container, Type: "blocks"})
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("offline") })
	if err := checkWorkflowClose(context.Background(), mem, "/d", "p", "after"); err != nil {
		t.Fatal(err)
	}
}

// #5: a block for another PR is no review of this one.
func TestWorkflowCloseBlockForAnotherPRIsRequired(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "PR merged", Labels: []string{"gate", "kind:objective", "approver:user", "pr:8"}})
	if _, err := plugWorkflow(context.Background(), mem, "/d", plugInstance(t, testPR)); err != nil {
		t.Fatal(err)
	}
	pinWorkflowRepo(t, "o/r")
	var asked []string
	pinWorkflowGH(t, prStateGH("OPEN", &asked))
	err := checkWorkflowClose(context.Background(), mem, "/d", "p", "M")
	if err == nil || !strings.Contains(err.Error(), "MERGE_REVIEW_INCOMPLETE") {
		t.Fatal(err)
	}
}

// #5: the door and graph check read one PR for each label and give one answer.
func TestWorkflowMergedExemptionDoorAndGraphAgree(t *testing.T) {
	cases := []struct{ name, id string }{
		{"own bare label", "own"},
		{"own repo label", "cross"},
		{"stage label", "gate"},
	}
	for _, state := range []string{"MERGED", "OPEN"} {
		for _, c := range cases {
			t.Run(state+" "+c.name, func(t *testing.T) {
				mem := &beads.MemClient{}
				mem.Seed(beads.Bead{ID: "stage", Title: "Build", Labels: []string{"kind:objective", "pr:1234"}})
				mem.Seed(beads.Bead{ID: "own", Title: "PR merged", Labels: []string{"gate", "kind:objective", "pr:1234"}})
				mem.Seed(beads.Bead{ID: "cross", Title: "PR merged", Labels: []string{"gate", "kind:objective", "pr:o/r#1234"}})
				mem.Seed(beads.Bead{ID: "gate", Title: "stage: gate merged", Labels: []string{"kind:gate", "gate-of:stage", "gate-name:merged"}})
				pinWorkflowRepo(t, "o/r")
				var asked []string
				pinWorkflowGH(t, prStateGH(state, &asked))
				doorErr := checkWorkflowClose(context.Background(), mem, "/d", "p", c.id)
				if len(asked) != 0 {
					t.Fatalf("door asked about %v", asked)
				}
				if state == "MERGED" && doorErr != nil {
					t.Fatalf("merged PR refused: %v", doorErr)
				}
				if doorErr != nil {
					t.Fatalf("open PR: %v", doorErr)
				}
				node := func(id string) *workflowtemplate.Node {
					b, _ := mem.Show(context.Background(), "/d", id)
					return &workflowtemplate.Node{Bead: *b}
				}
				step := livedata.SnapStep{BeadID: "stage", Title: "Build", Status: "open", PR: []string{"1234"}}
				if c.id == "gate" {
					step.GateChecks = []workflowtemplate.Node{*node("gate")}
				}
				steps := []livedata.SnapStep{step}
				if c.id != "gate" {
					steps = append(steps, livedata.SnapStep{BeadID: c.id, Title: "PR merged", Status: "open", Gate: true, WorkflowCheck: node(c.id)})
				}
				snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{Project: "p", Repo: "o/r", BeadID: "w", Steps: steps}}}
				livedata.RefreshWorkflowHeads(&snap, func(pr string) (workflowtemplate.Head, bool) {
					h, err := workflowtemplate.ReadHead(context.Background(), workflowGH, pr)
					return h, err == nil
				})
				found := ""
				for _, f := range livedata.CheckDiagram(snap.Workstreams[0], "w") {
					if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
						found = f.Check
					}
				}
				if (doorErr == nil) != (found == "") {
					t.Fatalf("door says %v, graph says %q", doorErr, found)
				}
			})
		}
	}
}

// plugBeforeM is a plugged block in front of a plain target, on the project the verbs resolve.
func plugBeforeM(t *testing.T, mem *beads.MemClient) *workflowPlugResult {
	t.Helper()
	mem.Seed(beads.Bead{ID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "Ship it", Labels: []string{"kind:objective"}})
	tpl, err := legacyTemplateFile()
	if err != nil {
		t.Fatal(err)
	}
	inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "manifest", Parent: "B", Before: "M", PR: testPR})
	if err != nil {
		t.Fatal(err)
	}
	res, err := plugWorkflow(context.Background(), mem, "/d", inst)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// F1: the edge from a target to its own review block is the guard.
func TestInternalDepRmRefusesTheTargetsReviewBlock(t *testing.T) {
	mem := setupGateProject(t)
	allowUnheld(t, "the fixture's waiters hold nothing else; the review-block refusal is under test")
	res := plugBeforeM(t, mem)
	mem.Seed(beads.Bead{ID: "other", ParentID: "B", Status: "open"})
	mem.Seed(beads.Bead{ID: "after", ParentID: "B", Status: "open"})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "M", DependsOnID: "other", Type: "blocks"}, beads.Dep{IssueID: "after", DependsOnID: res.Container, Type: "blocks"})
	rm := func(a, b string) (int, string) {
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalDep([]string{"--project", "manifest", "rm", a, b}, stdout, stderr)
		_, errOut := read()
		return code, errOut
	}
	if code, errOut := rm("M", res.Container); code == 0 || !strings.Contains(errOut, "plugged in front of M") {
		t.Fatal(code, errOut)
	}
	if deps, _ := mem.Deps(context.Background(), "/d", "M"); !slices.ContainsFunc(deps, func(d beads.Dep) bool { return d.DependsOnID == res.Container }) {
		t.Fatal("the edge went")
	}
	for _, edge := range [][2]string{{"M", "other"}, {"after", res.Container}} {
		if code, errOut := rm(edge[0], edge[1]); code != 0 {
			t.Fatalf("%v: %s", edge, errOut)
		}
	}
}

// F5: only a door writes evidence; a proof is still the review's to record.
func TestInternalCommentRefusesEvidence(t *testing.T) {
	mem := setupGateProject(t)
	mem.Seed(beads.Bead{ID: "S", Status: "open"})
	comment := func(text string) (int, string) {
		stdout, stderr, read := withTempOutputFiles(t)
		code := runInternalComment([]string{"--project", "manifest", "--bead", "S", "--text", text}, strings.NewReader(""), stdout, stderr)
		_, errOut := read()
		return code, errOut
	}
	forged := workflowtemplate.Evidence{Instance: "i", Step: "final-check", Verifier: "head-current", PR: testPR, Head: "h", Finding: "passed"}.Comment()
	for _, text := range []string{forged, "\n  " + forged} {
		if code, errOut := comment(text); code == 0 || !strings.Contains(errOut, "only the completion door") {
			t.Fatal(code, errOut)
		}
	}
	binding := workflowtemplate.WriterBinding{Revision: "forged", PR: testPR, Head: "h", Tasks: []string{"writer"}}.Comment()
	for _, text := range []string{binding, "\n  " + binding} {
		if code, errOut := comment(text); code == 0 || !strings.Contains(errOut, "bind-writers") {
			t.Fatal(code, errOut)
		}
	}

	if cs, _ := mem.Comments(context.Background(), "/d", "S"); len(cs) != 0 {
		t.Fatal(cs)
	}
	for _, text := range []string{workflowtemplate.ProofPrefix + `{"review_bead":"r"}`, "progress: quoting " + forged} {
		if code, errOut := comment(text); code != 0 {
			t.Fatal(errOut)
		}
	}
}

// F2: internal-gate rm and apply retire a merge gate only through the same check.
func TestGateRmAndApplyOnAMergeGateGoThroughTheDoor(t *testing.T) {
	mem := setupGateProject(t)
	mem.Seed(beads.Bead{ID: "ms-1", Labels: []string{"kind:objective"}})
	pinWorkflowGH(t, workflowFakeGH)
	if code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged", "--check", "PR merged", "--verifier", "user"); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := gateCmd(t, "rm", "--project", "manifest", "--bead", "ms-1.2", "--name", "merged"); code != 0 {
		t.Fatal(code, errOut)
	}
	if code, _, errOut := gateCmd(t, "apply", "--project", "manifest", "--bead", "ms-1.2", "--template", "spec"); code != 0 {
		t.Fatal(code, errOut)
	}
	for _, b := range mem.Beads {
		if hasLabel(b.Labels, "gate-name:merged") && (b.Status != "closed" && !hasLabel(b.Labels, "retired")) {
			t.Fatal("the merge gate was not retired")
		}
	}
	// Any other gate comes off as before.
	if code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1", "--name", "spec", "--check", "ok", "--verifier", "operator"); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := gateCmd(t, "rm", "--project", "manifest", "--bead", "ms-1", "--name", "spec"); code != 0 {
		t.Fatal(errOut)
	}
}

// The template's own check is what cmdrd lets through.
func TestMergedTemplateIsTheStandardCheck(t *testing.T) {
	for _, repo := range []string{"", "acme/web"} {
		g, err := templateGate("merged", templateArgs{Repo: repo, PRs: []string{"7", "8"}})
		if err != nil || !gates.IsMergedCheck(g.Check) {
			t.Fatal(g.Check, err)
		}
	}
}

// F3: a review owner must be an Operator that ran, when the run table can say.
func TestWorkflowReviewOwnerMustHaveARun(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowGH(t, workflowFakeGH)
	mem.Close(ctx, "/d", res.Remaining[0].BeadID)
	second := res.Remaining[1].BeadID
	mem.Seed(beads.Bead{ID: "review-task", Status: "closed", Labels: []string{"owner:operator-ghost"}})
	mem.SetParent(ctx, "/d", "review-task", "B")
	mem.AddComment(ctx, "/d", "review-task", "Head: head1\nCLEAN")
	raw, _ := json.Marshal(workflowtemplate.Proof{ReviewBead: "review-task", Head: "head1", NoFindings: true})
	mem.AddComment(ctx, "/d", second, workflowtemplate.ProofPrefix+string(raw))
	pinOwnerRuns(t, false, true)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", second); err == nil || !strings.Contains(err.Error(), "matches no Operator run") {
		t.Fatal(err)
	}
	pinOwnerRuns(t, false, false)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", second); err == nil {
		t.Fatal("unreadable run table accepted")
	}
}

func TestWorkflowOwnerRunReadsAgentRuns(t *testing.T) {
	setupGateProject(t)
	ctx := context.Background()
	st, done, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetProjectByName(ctx, "manifest")
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	// Stored dotted; the owner label spells it with dashes.
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: p.ID, TaskBeadID: "r.1", AgentName: "op-review-r.1", OpencodeSessionID: "s", SpawnTime: 1, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	done()
	if roles, err := workflowTaskRoles(ctx, "manifest", "r.1"); err != nil || !slices.Contains(roles, "operator-op-review-r-1") {
		t.Fatal(roles, err)
	}
	if _, err := workflowTaskRoles(ctx, "manifest", "ghost"); err == nil {
		t.Fatal("missing task run accepted")
	}
}

// Generated-step closure must never fall back to a project-wide detail read.
type noBulkWorkflowDetails struct{ *beads.MemClient }

func (n noBulkWorkflowDetails) Details(context.Context, string, []string) (map[string]beads.Detail, error) {
	return nil, errors.New("unexpected bulk display detail read")
}

// A --review label on a later step of the merge: the block plugged before the
// greenlight does not cover it, and the greenlight stays judged on its block.
func TestWorkflowCloseMistakenReviewLabelOnALaterStep(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{}
	res := seedReviewWorkflow(t, mem)
	pinWorkflowRepo(t, "o/r")
	pinWorkflowGH(t, workflowFakeGH)
	finishReviewBlock(t, mem, res)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Container); err != nil {
		t.Fatal(err)
	}
	mem.Close(ctx, "/d", res.Container)
	mem.Seed(beads.Bead{ID: "L", ParentID: "B", Title: "Fix merged to main", Labels: []string{"kind:objective", "pr:7"}})
	mem.DependOn(ctx, "/d", "L", "M")
	mem.DependOn(ctx, "/d", "L", res.Container)
	mem.SetLabel(ctx, "/d", "M", "pr:7", true)

	if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
		t.Fatalf("the greenlight's own block covers its label: %v", err)
	}
	err := checkWorkflowClose(ctx, mem, "/d", "p", "L")
	if err != nil {
		t.Fatalf("the later step is not covered by another target's block: %v", err)
	}
	applyReview(ctx, mem, "/d", "L", "none", "the label belongs to M", false)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", "L"); err != nil {
		t.Fatalf("without its label the later step is no target: %v", err)
	}
	// The greenlight without its label: its block still judges it, on the block's PR.
	applyReview(ctx, mem, "/d", "M", "none", "the label belongs to its block", false)
	if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
		t.Fatalf("label cleared: %v", err)
	}
	var asked []string
	pinWorkflowGH(t, prStateGH("OPEN", &asked))
	checkWorkflowClose(ctx, mem, "/d", "p", "M")
	if !slices.Contains(asked, "7") && !slices.Contains(asked, testPR) {
		t.Fatalf("the block's PR was not read: %v", asked)
	}

	// The plug the fix line used to name (another parent, same PR) is refused, so
	// the greenlight's block stays the one block.
	tpl, err := legacyTemplateFile()
	if err != nil {
		t.Fatal(err)
	}
	mem.Seed(beads.Bead{ID: "OTHER", Status: "open"})
	dup, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "OTHER", Before: "M", PR: testPR})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugWorkflow(ctx, mem, "/d", dup); err == nil || !strings.Contains(err.Error(), "refusing a second block") {
		t.Fatalf("a duplicate block: %v", err)
	}
}

// The door on the shape the board folds: a block whose parent closed still judges
// its open target, as TestWorkflowReview_FoldedBlock* has the display read it.
func TestWorkflowCloseFoldedBlockAgreesWithTheBoard(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) *beads.MemClient {
		mem := &beads.MemClient{}
		pinOwnerRuns(t, true, true)
		pinWorkflowRepo(t, "o/r")
		pinWorkflowGH(t, workflowFakeGH)
		mem.Seed(beads.Bead{ID: "B", Status: "open"})
		mem.Seed(beads.Bead{ID: "S", ParentID: "B", Title: "Finished stage", Status: "open", Labels: []string{"kind:objective"}})
		mem.SetParent(ctx, "/d", "S", "B")
		mem.Seed(beads.Bead{ID: "M", ParentID: "B", Title: "Greenlight merging the fix", Labels: []string{"gate", "kind:objective", "approver:user", "pr:7"}})
		tpl, err := legacyTemplateFile()
		if err != nil {
			t.Fatal(err)
		}
		inst, err := workflowtemplate.Instantiate(tpl, workflowtemplate.Params{Project: "p", Parent: "S", Before: "M", PR: testPR})
		if err != nil {
			t.Fatal(err)
		}
		res, err := plugWorkflow(ctx, mem, "/d", inst)
		if err != nil {
			t.Fatal(err)
		}
		seedWriterBinding(mem, res.Container, "head1")
		finishReviewBlock(t, mem, res)
		if err := checkWorkflowClose(ctx, mem, "/d", "p", res.Container); err != nil {
			t.Fatal(err)
		}
		mem.Close(ctx, "/d", res.Container)
		mem.Close(ctx, "/d", "S")
		return mem
	}
	t.Run("complete passes", func(t *testing.T) {
		if err := checkWorkflowClose(ctx, seed(t), "/d", "p", "M"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a new push changes nothing", func(t *testing.T) {
		mem := seed(t)
		pinWorkflowGH(t, func(c context.Context, args ...string) ([]byte, error) {
			out, err := workflowFakeGH(c, args...)
			return bytes.ReplaceAll(out, []byte("head1"), []byte("head2")), err
		})
		if err := checkWorkflowClose(ctx, mem, "/d", "p", "M"); err != nil {
			t.Fatalf("a passed block went stale: %v", err)
		}
	})
}
