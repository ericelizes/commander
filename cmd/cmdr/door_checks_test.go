package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fmt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// registerRunningOperator records a running agent run so in-progress writes
// owned by role pass the door.
func registerRunningOperator(t *testing.T, role string) {
	t.Helper()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	projects, err := st.ListProjects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("no project: %v", err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: projects[0].ID, TaskBeadID: "obj-1", AgentName: role, SpawnTime: 1}); err != nil {
		t.Fatal(err)
	}
}

func refused(t *testing.T, args []string, want string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective(args, stdout, stderr); code == 0 {
		t.Fatalf("%v: write passed the door", args)
	}
	if _, errOut := read(); !strings.Contains(errOut, want) {
		t.Fatalf("stderr %q, want %q", errOut, want)
	}
}

func TestDoor_BlockedWithNothingToWaitOnIsRefused(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open"}}
	setupObjectiveProject(t, mock)
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked"}, "names nothing it waits on")
	if strings.Contains(strings.Join(mock.Calls, "\n"), "SetLabel") {
		t.Fatalf("a refused write changed labels:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

func TestDoor_BlockedOnAnOpenStepPasses(t *testing.T) {
	mock := &beads.MockClient{
		ShowResult: &beads.Bead{ID: "obj-2", Status: "open"},
		DepsResult: map[string][]beads.Dep{"obj-1": {{IssueID: "obj-1", DependsOnID: "obj-2", Type: "blocks"}}},
	}
	setupObjectiveProject(t, mock)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
}

func TestDoor_InProgressOwnedByUserIsRefused(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--state", "in-progress", "--owner", "user"}, "no way to show the user working")
}

func TestDoor_InProgressOwnedByStoppedOperatorIsRefused(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--state", "in-progress", "--owner", "operator-gone"}, "is not running")
}

func TestDoor_ReviewWaitWithoutPRIsRefused(t *testing.T) {
	err := checkReviewWaitsHavePR(context.Background(), &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1"}}, "", "obj-1", "",
		[]beads.Wait{{Kind: beads.WaitReview, Ref: "someone"}})
	if err == nil || !strings.Contains(err.Error(), "names no PR") {
		t.Fatalf("err = %v", err)
	}
	if err := checkReviewWaitsHavePR(context.Background(), &beads.MockClient{}, "", "obj-1", "42",
		[]beads.Wait{{Kind: beads.WaitReview, Ref: "someone"}}); err != nil {
		t.Fatalf("with --review: %v", err)
	}
	if err := checkReviewWaitsHavePR(context.Background(), &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Labels: []string{"pr:42"}}}, "", "obj-1", "",
		[]beads.Wait{{Kind: beads.WaitReview, Ref: "someone"}}); err != nil {
		t.Fatalf("with pr label: %v", err)
	}
}

func TestDoor_UntilLabelMustBePlainWords(t *testing.T) {
	for _, bad := range []string{"", "quiet-window", strings.Repeat("word ", 20)} {
		if checkUntilLabelsReadable([]beads.Wait{{Kind: beads.WaitUntil, Ref: bad}}) == nil {
			t.Errorf("label %q passed", bad)
		}
	}
	if err := checkUntilLabelsReadable([]beads.Wait{{Kind: beads.WaitUntil, Ref: "the 6 AM quiet window opens"}}); err != nil {
		t.Error(err)
	}
}

func TestDoor_AskWhileAStepItNeedsIsOpenIsRefused(t *testing.T) {
	mock := &beads.MockClient{
		ShowResult: &beads.Bead{ID: "obj-2", Status: "in_progress"},
		DepsResult: map[string][]beads.Dep{"obj-1": {{IssueID: "obj-1", DependsOnID: "obj-2", Type: "blocks"}}},
	}
	if err := checkAskReady(context.Background(), mock, "", "obj-1"); err == nil || !strings.Contains(err.Error(), "obj-2") {
		t.Fatalf("err = %v", err)
	}
	mock.ShowResult.Status = "closed"
	if err := checkAskReady(context.Background(), mock, "", "obj-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDoor_SlugUntilLabelNamesTheTypedWaits(t *testing.T) {
	for _, slug := range []string{"user-spec-review-first", "agent-runtime", "after-storm-fixes-and-install", "dispatcher-next-release"} {
		err := checkUntilLabelsReadable([]beads.Wait{{Kind: beads.WaitUntil, Ref: slug}})
		if err == nil || !strings.Contains(err.Error(), "review:<login>") || !strings.Contains(err.Error(), "prod-tag:<sha>") || !strings.Contains(err.Error(), "needs-user") {
			t.Errorf("%q: err = %v", slug, err)
		}
	}
	setupObjectiveProject(t, &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open"}})
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--waiting", "until:2026-10-27T14:00:00Z:user-spec-review-first"}, "plain words")
}

func TestDoor_TimerOnTheUsersGateIsRefused(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open", Labels: []string{"kind:objective", "gate", "approver:user"}}}
	setupObjectiveProject(t, mock)
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--waiting", "until:2026-10-27T14:00:00Z:the spec review is done"}, "a timer cannot make the user's decision")
	if strings.Contains(strings.Join(mock.Calls, "\n"), "SetLabel") {
		t.Fatalf("a refused write changed labels:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

func TestDoor_TimerOnAStepTheUserOwnsIsRefused(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open", Labels: []string{"kind:objective", "needs-user", "owner:user"}}})
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--waiting", "until:2026-10-27T14:00:00Z:the spec review is done"}, "owned by the user")
}

func TestDoor_TimerOnAnotherApproversGatePasses(t *testing.T) {
	b := &beads.Bead{ID: "obj-1", Labels: []string{"gate", "approver:reviewer-a"}}
	if err := checkUntilNotOnUserDecision(b, "", []beads.Wait{{Kind: beads.WaitUntil, Ref: "the release window opens"}}); err != nil {
		t.Fatal(err)
	}
	if err := checkUntilNotOnUserDecision(&beads.Bead{ID: "obj-1", Labels: []string{"gate", "approver:user"}}, "", []beads.Wait{{Kind: beads.WaitCI, PR: 3}}); err != nil {
		t.Fatalf("only timers are refused on the user's gate: %v", err)
	}
}

func TestDoor_UserOwnerOnAPlainStepOutsideNeedsUserIsRefused(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open", Labels: []string{"kind:objective"}}})
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--owner", "user"}, "only while it asks them something")
	refused(t, []string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked", "--owner", "user", "--waiting", "ci:#4"}, "only while it asks them something")

	b := &beads.Bead{ID: "obj-1", Labels: []string{"kind:objective"}}
	if err := checkUserOwnsOnlyAsks(b, "user", beads.StateNeedsUser); err != nil {
		t.Fatalf("needs-user is the user's: %v", err)
	}
	if err := checkUserOwnsOnlyAsks(&beads.Bead{ID: "obj-1", Labels: []string{"gate"}}, "user", beads.StateNotStarted); err != nil {
		t.Fatalf("a gate may be the user's: %v", err)
	}
	if err := checkUserOwnsOnlyAsks(b, "operator-x", beads.StateNotStarted); err != nil {
		t.Fatal(err)
	}
}

func TestDoor_GateBothBeforeAndAfterTheSameWorkIsRefused(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "ws.1", Status: "open"})
	m.Seed(beads.Bead{ID: "ws.2", Status: "open"})
	m.Seed(beads.Bead{ID: "ws.3", Status: "open"})
	if err := m.DependOn(ctx, "", "ws.1", "ws.2"); err != nil {
		t.Fatal(err)
	}
	if err := m.DependOn(ctx, "", "ws.2", "ws.3"); err != nil {
		t.Fatal(err)
	}
	// ws.1 waits on ws.3 (through ws.2): a gate after ws.1 and before ws.3 loops.
	if err := checkGateLinksAcyclic(ctx, m, "", []string{"ws.1"}, []string{"ws.3"}); err == nil || !strings.Contains(err.Error(), "a cycle") {
		t.Fatalf("err = %v", err)
	}
	if err := checkGateLinksAcyclic(ctx, m, "", []string{"ws.2"}, []string{"ws.2"}); err == nil {
		t.Fatal("the same step on both sides passed")
	}
	if err := checkGateLinksAcyclic(ctx, m, "", []string{"ws.3"}, []string{"ws.1"}); err != nil {
		t.Fatalf("approval after the work it approves: %v", err)
	}
}

func TestManagerGuard_HandWrittenWaitIsRefusedWithTheWaitReason(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	var out strings.Builder
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"br label add p-1 waiting:1:until:2:user-spec-review-first"}}`)
	runInternalManagerGuard([]string{"--role", "manager-p-1"}, in, &out)
	if !strings.Contains(out.String(), "internal-set-objective --waiting") {
		t.Fatalf("hook output = %q", out.String())
	}
}

func TestOpenBlocker_ReadErrorIsNotAnAnswer(t *testing.T) {
	deps := map[string][]beads.Dep{"obj-1": {{IssueID: "obj-1", DependsOnID: "obj-2", Type: "blocks"}}}
	for name, mock := range map[string]*beads.MockClient{
		"deps": {DepsErr: errors.New("timeout")},
		"show": {DepsResult: deps, ShowErr: errors.New("timeout")},
	} {
		if b, err := openBlocker(context.Background(), mock, "", "obj-1"); err == nil || b != "" {
			t.Errorf("%s: blocker %q err %v, want an error", name, b, err)
		}
		if err := checkAskReady(context.Background(), mock, "", "obj-1"); err == nil || !strings.Contains(err.Error(), "busy") {
			t.Errorf("%s: needs-user err = %v, want retryable busy", name, err)
		}
		if err := checkBlockedNamesWait(context.Background(), mock, "", "obj-1", nil); err == nil || !strings.Contains(err.Error(), "busy") {
			t.Errorf("%s: blocked err = %v, want retryable busy", name, err)
		}
	}
}

func TestOpenBlocker_FreshReadBeatsTheCachedView(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	mock := &beads.MockClient{
		ShowResult: &beads.Bead{ID: "obj-2", Status: "in_progress"},
		DepsResult: map[string][]beads.Dep{"obj-1": {{IssueID: "obj-1", DependsOnID: "obj-2", Type: "blocks"}}},
	}
	cc := beads.NewCached(mock)
	if _, err := cc.Show(context.Background(), dir, "obj-2"); err != nil { // warm the cache with "open"
		t.Fatal(err)
	}
	mock.ShowResult = &beads.Bead{ID: "obj-2", Status: "closed"} // closed without touching the beads files
	if b, err := openBlocker(context.Background(), cc, dir, "obj-1"); err != nil || b != "" {
		t.Fatalf("blocker %q err %v, want none: obj-2 is closed", b, err)
	}
}

func TestDoor_TimerStandingInForAReviewIsRefused(t *testing.T) {
	for _, label := range []string{"infra PR 4003 review", "check whether the PR is approved", "reviewer-a reviews the DNS PR"} {
		err := checkUntilLabelsReadable([]beads.Wait{{Kind: beads.WaitUntil, Ref: label}})
		if err == nil || !strings.Contains(err.Error(), "--review <owner>/<name>#N") {
			t.Errorf("%q: err = %v", label, err)
		}
	}
	for _, label := range []string{"the 6 AM quiet window opens", "the dry run's 24 hour check"} {
		if err := checkUntilLabelsReadable([]beads.Wait{{Kind: beads.WaitUntil, Ref: label}}); err != nil {
			t.Errorf("%q: %v", label, err)
		}
	}
}

func TestReviewLabel_AnyRepo(t *testing.T) {
	for in, want := range map[string]string{"4003": "pr:4003", "#4003": "pr:4003", "example-org/infrastructure#4003": "pr:example-org/infrastructure#4003"} {
		if got, err := reviewLabel(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "infra#x", "a b/c#1", "#"} {
		if _, err := reviewLabel(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestDoor_ReviewCardWaitsForGreenCI(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "commander")
	heads := map[string]workflowtemplate.Head{}
	oldRepo, oldRead := prRepoOf, readPRHead
	t.Cleanup(func() { prRepoOf, readPRHead = oldRepo, oldRead })
	prRepoOf = func(string) string { return "o/r" }
	readPRHead = func(_ context.Context, pr string) (workflowtemplate.Head, error) {
		h, ok := heads[pr[strings.LastIndex(pr, "/")+1:]]
		if !ok {
			return h, fmt.Errorf("GraphQL: Could not resolve to a PullRequest with the number of 9")
		}
		return h, nil
	}
	green := workflowtemplate.Check{Kind: "CheckRun", Name: "tests", Status: "COMPLETED", Conclusion: "SUCCESS"}
	red := workflowtemplate.Check{Kind: "CheckRun", Name: "Sidecar package checks (forge-notify)", Status: "COMPLETED", Conclusion: "FAILURE"}
	heads["3651"] = workflowtemplate.Head{SHA: "73c3d05d2eca", State: "OPEN", Checks: []workflowtemplate.Check{green, red}}
	heads["3657"] = workflowtemplate.Head{SHA: "100fd27ef825", State: "OPEN"}
	heads["3622"] = workflowtemplate.Head{SHA: "aaaa", State: "MERGED", Checks: []workflowtemplate.Check{red}}
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "g", Status: "open", Title: "Review and merge PR 3651"}}
	card := &store.Decision{Question: "Review and merge the two Notify PRs?", Context: "Merge https://github.com/o/r/pull/3657 into PR 3651, then 3651 into main."}

	err := checkReviewAskGreen(context.Background(), mock, "", "g", card)
	if err == nil || !strings.Contains(err.Error(), "PR 3651 at 73c3d05d is not green: Sidecar package checks (forge-notify) (failure)") || !strings.Contains(err.Error(), "PR 3657 at 100fd27e has no CI checks") || !strings.Contains(err.Error(), "--checks-not-green") {
		t.Fatalf("a review card over red CI must be refused, naming the checks: %v", err)
	}
	heads["3651"] = workflowtemplate.Head{SHA: "73c3d05d2eca", State: "OPEN", Checks: []workflowtemplate.Check{green}}
	heads["3657"] = workflowtemplate.Head{SHA: "100fd27ef825", State: "MERGED"}
	if err := checkReviewAskGreen(context.Background(), mock, "", "g", card); err != nil {
		t.Fatalf("green, or merged, PRs pass: %v", err)
	}
	// A merged PR named in passing, a number that is not a PR, a card that
	// asks something else, and the user's own write are all left alone.
	if err := checkReviewAskGreen(context.Background(), mock, "", "g", &store.Decision{Question: "Merge the follow-up now that PR 3622 and PR 9 landed?"}); err != nil {
		t.Fatal(err)
	}
	heads["3651"] = workflowtemplate.Head{SHA: "73c3d05d2eca", State: "OPEN", Checks: []workflowtemplate.Check{red}}
	other := &beads.MockClient{ShowResult: &beads.Bead{ID: "s", Status: "open", Title: "Pick the Slack channel"}}
	if err := checkReviewAskGreen(context.Background(), other, "", "s", &store.Decision{Question: "Which channel should PR 3651 post to?"}); err != nil {
		t.Fatalf("a card that asks no review or merge is not held for CI: %v", err)
	}
	t.Setenv("COMMANDER_ROLE", "")
	if err := checkReviewAskGreen(context.Background(), mock, "", "g", card); err != nil {
		t.Fatalf("the user's own write is not refused: %v", err)
	}
}
