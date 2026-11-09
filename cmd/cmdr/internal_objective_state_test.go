package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestRunInternalSetObjective_StateBlockedSetsMarkerAndClearsOther(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "blocked", "--waiting", "until:2026-11-01T09:00:00Z:review window opens"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	joined := strings.Join(mock.Calls, "\n")
	if !strings.Contains(joined, "SetLabel:obj-1|label=needs-user|on=false") || !strings.Contains(joined, "SetLabel:obj-1|label=blocked|on=true") {
		t.Fatalf("unexpected calls:\n%s", joined)
	}
	if strings.Contains(joined, "SetStatus") {
		t.Fatalf("blocked must not change bead status:\n%s", joined)
	}
	if !strings.Contains(out, `"state":"blocked"`) {
		t.Fatalf("output: %s", out)
	}
}

func TestRunInternalSetObjective_StateInProgressSetsStatusAndClearsMarkers(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)

	registerRunningOperator(t, "operator-x")
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "in-progress", "--owner", "operator-x"}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	joined := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"SetStatus:obj-1", "label=blocked|on=false", "label=needs-user|on=false"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestRunInternalSetObjective_OwnerReplacesOldOwner(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Labels: []string{"kind:objective", "owner:user"}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--owner", "operator-x"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	joined := strings.Join(mock.Calls, "\n")
	if !strings.Contains(joined, "label=owner:user|on=false") || !strings.Contains(joined, "label=owner:operator-x|on=true") {
		t.Fatalf("unexpected calls:\n%s", joined)
	}
}

func TestRunInternalSetObjective_OwnerNoneClears(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Labels: []string{"owner:user"}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--owner", "none"}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	joined := strings.Join(mock.Calls, "\n")
	if !strings.Contains(joined, "label=owner:user|on=false") || strings.Contains(joined, "on=true") {
		t.Fatalf("unexpected calls:\n%s", joined)
	}
}

// Re-planning a step its Operator left drops what cmdrd wrote when it ended.
func TestRunInternalSetObjective_ClearsSettleMarkers(t *testing.T) {
	wait := beads.Wait{Kind: beads.WaitUntil, Since: time.Unix(100, 0), Until: time.Unix(200, 0), Ref: beads.ManagerCloseWait}.Label()
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Labels: []string{"kind:objective", beads.OperatorDiedLabel, wait}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "not-started", "--owner", "none"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	joined := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"label=" + beads.OperatorDiedLabel + "|on=false", "label=" + wait + "|on=false"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestRunInternalSetObjective_RejectsBadState(t *testing.T) {
	setupObjectiveProject(t, &beads.MockClient{})
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--state", "done"}, stdout, stderr); code == 0 {
		t.Fatal("done must go through internal-close-objective")
	}
}

func stubMail(t *testing.T) *fakeMailer {
	t.Helper()
	st := newFakeAmStore()
	st.m[amKey+"\x00manager-ms-1"] = "GoldTiger" // the Manager of the step's workstream
	st.m[amKey+"\x00commander"] = "CalmCave"
	m := &fakeMailer{}
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return st, m, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })
	return m
}

const testDecision = `{"question":"Merge #4107?","context":"CI is green.","options":[{"label":"Merge","implications":"ships to staging","recommended":true},{"label":"Wait"}]}`

func TestSetObjective_NeedsUserMailsCommanderOnce(t *testing.T) {
	mailer := stubMail(t)
	mock := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "ms-1", Title: "Workstream one", Status: "open"},
		{ID: "ms-1.2", Title: "Verify the panel", Status: "in_progress", Labels: []string{"kind:objective"}},
	}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1.2", "--state", "needs-user", "--note", "Check the scroll", "--ask", "Check the scroll", "--decision", testDecision}
	if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("expected one mail to Commander, got %+v", mailer.sent)
	}
	m := mailer.sent[0]
	if m.sender != "GoldTiger" || len(m.to) != 1 || m.to[0] != "CalmCave" {
		t.Fatalf("sender/recipient = %q -> %v", m.sender, m.to)
	}
	for _, want := range []string{"⚑ needs you: Check the scroll — Verify the panel (manifest)"} {
		if m.subject != want {
			t.Fatalf("subject = %q, want %q", m.subject, want)
		}
	}
	for _, want := range []string{"ms-1.2", "Workstream one", "Check the scroll", "Do not close the step", "Ask (what the user sees in his sidebar): Check the scroll"} {
		if !strings.Contains(m.body, want) {
			t.Fatalf("body missing %q:\n%s", want, m.body)
		}
	}
}

func TestSetObjective_NeedsUserStaysQuietWhenAlreadyFlaggedOrSilenced(t *testing.T) {
	mailer := stubMail(t)
	already := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "ms-1.2", Title: "x", Status: "open", Labels: []string{"kind:objective", "needs-user"}},
	}}
	setupObjectiveProject(t, already)
	stdout, stderr, _ := withTempOutputFiles(t)
	runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.2", "--state", "needs-user"}, stdout, stderr)
	if len(mailer.sent) != 0 {
		t.Fatalf("already flagged: no new mail, got %+v", mailer.sent)
	}

	fresh := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.3", Title: "y", Status: "open"}}}
	setupObjectiveProject(t, fresh)
	stdout, stderr, _ = withTempOutputFiles(t)
	runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.3", "--state", "needs-user", "--no-notify"}, stdout, stderr)
	if len(mailer.sent) != 0 {
		t.Fatalf("--no-notify: no mail, got %+v", mailer.sent)
	}
}

func TestSetObjective_NeedsUserRequiresADecision(t *testing.T) {
	mailer := stubMail(t)
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.4", Title: "Verify", Status: "open"}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user"}, stdout, stderr); code == 0 {
		t.Fatal("needs-user without a decision card must be refused")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "requires --decision") {
		t.Fatalf("stderr = %s", errOut)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "SetLabel") || strings.HasPrefix(c, "SetStatus") {
			t.Fatalf("a refused request must not change the step: %v", mock.Calls)
		}
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("no mail on refusal: %+v", mailer.sent)
	}

	// A note and ask without the card are still refused.
	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--note", "details", "--ask", "x"}, stdout, stderr) == 0 {
		t.Fatal("note + ask without --decision must be refused")
	}
	// A card with a bad shape is refused too.
	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--decision", `{"question":""}`}, stdout, stderr) == 0 {
		t.Fatal("an invalid card must be refused")
	}

	// An agent cannot skip the card with --no-notify.
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify"}, stdout, stderr) == 0 {
		t.Fatal("an agent's --no-notify without --decision must be refused")
	}

	// the user's own TUI mark key (--no-notify) needs no note.
	t.Setenv("COMMANDER_ROLE", "")
	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.4", "--state", "needs-user", "--no-notify"}, stdout, stderr); code != 0 {
		t.Fatal("--no-notify should not need a note")
	}
}

func TestSetObjective_NeedsUserRequiresAShortAsk(t *testing.T) {
	mailer := stubMail(t)
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.5", Title: "Verify", Status: "open"}}}
	setupObjectiveProject(t, mock)

	// With a card the ask is derived from its question, so only an explicit
	// over-long ask is refused.
	for name, ask := range map[string][]string{
		"too long": {"--ask", "This ask is far too long to fit in the sidebar column"},
	} {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		args := append([]string{"--project", "manifest", "--bead", "ms-1.5", "--state", "needs-user", "--note", "details", "--decision", testDecision}, ask...)
		if code := runInternalSetObjective(args, stdout, stderr); code == 0 {
			t.Fatalf("%s ask must be refused", name)
		}
		if _, errOut := readOutput(); !strings.Contains(errOut, "--ask") {
			t.Fatalf("%s: stderr should point at --ask: %s", name, errOut)
		}
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("a refused request sends no mail: %+v", mailer.sent)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "SetLabel") || strings.HasPrefix(c, "SetStatus") {
			t.Fatalf("a refused request must not change the step: %v", mock.Calls)
		}
	}
}

func TestSetObjective_NeedsUserPersistsTheAskAndLeavingClearsIt(t *testing.T) {
	stubMail(t)
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.6", Title: "Verify", Status: "open"}, approvedWorkflowGate}}
	setupObjectiveProject(t, mock)
	ctx := context.Background()
	open := func() (*store.Store, int64, func()) {
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			t.Fatal(err)
		}
		p, _ := st.GetProjectByName(ctx, "manifest")
		return st, p.ID, cleanup
	}

	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.6", "--state", "needs-user", "--note", "run these 4 commands: ...", "--ask", "Run 4 staging cmds", "--decision", testDecision}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	st, pid, cleanup := open()
	got, _ := st.GetNeedsYou(ctx, pid, "ms-1.6")
	cleanup()
	if got == nil || got.Ask != "Run 4 staging cmds" || !strings.Contains(got.Note, "4 commands") {
		t.Fatalf("the ask and the full note are persisted: %+v", got)
	}

	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.6", "--state", "in-progress", "--owner", "manager-manifest"}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	st, pid, cleanup = open()
	gone, _ := st.GetNeedsYou(ctx, pid, "ms-1.6")
	cleanup()
	if gone != nil {
		t.Fatalf("leaving needs-user clears the ask: %+v", gone)
	}
}

func TestResolveNeeds_ApproveClosesAndTellsTheManager_DeferParks(t *testing.T) {
	mailer := &fakeMailer{}
	mailSt := newFakeAmStore()
	mailSt.m[amKey+"\x00commander"] = "CalmCave"
	mailSt.m[amKey+"\x00manager-ms-1"] = "GoldTiger"
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return mailSt, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })

	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "ms-1.2", Status: "open"}}
	setupObjectiveProject(t, mock)
	ctx := context.Background()
	flag := func(bead, ask string) {
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		p, _ := st.GetProjectByName(ctx, "manifest")
		if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: bead, Ask: ask, Note: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	has := func(bead string) bool {
		st, cleanup, _ := needsYouStoreFn()
		defer cleanup()
		p, _ := st.GetProjectByName(ctx, "manifest")
		n, _ := st.GetNeedsYou(ctx, p.ID, bead)
		return n != nil
	}

	flag("ms-1.2", "Approve: import rollout")
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.2", "--action", "approve"}, stdout, stderr); code != 0 {
		t.Fatalf("approve exit=%d", code)
	}
	calls := strings.Join(mock.Calls, "\n")
	if !strings.Contains(calls, "Close:ms-1.2") || has("ms-1.2") {
		t.Fatalf("approve closes the step and clears the ask:\n%s", calls)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to[0] != "GoldTiger" || mailer.sent[0].subject != "the user approved: Approve: import rollout" {
		t.Fatalf("the Manager (the step's workstream) is told: %+v", mailer.sent)
	}

	flag("ms-1.3", "Go/hold first spawn")
	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.3", "--action", "defer"}, stdout, stderr); code != 0 {
		t.Fatalf("defer exit=%d", code)
	}
	calls = strings.Join(mock.Calls, "\n")
	if !strings.Contains(calls, "SetStatus:ms-1.3") || strings.Contains(calls, "Close:ms-1.3") || has("ms-1.3") {
		t.Fatalf("defer parks the step (not started) without closing it:\n%s", calls)
	}
	if len(mailer.sent) != 2 || !strings.HasPrefix(mailer.sent[1].subject, "the user deferred:") {
		t.Fatalf("the Manager hears about the deferral: %+v", mailer.sent)
	}

	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "x", "--action", "maybe"}, stdout, stderr) == 0 {
		t.Fatal("unknown action must be refused")
	}
}

func TestSetObjective_DecisionDerivesAskAndStoresTheCard(t *testing.T) {
	stubMail(t)
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.7", Title: "Merge", Status: "open"}}}
	setupObjectiveProject(t, mock)
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.7", "--state", "needs-user", "--decision", testDecision}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, _ := st.GetProjectByName(context.Background(), "manifest")
	n, _ := st.GetNeedsYou(context.Background(), p.ID, "ms-1.7")
	if n == nil || n.Ask != "Merge #4107?" || !strings.Contains(n.Note, "CI is green.") {
		t.Fatalf("ask/note not derived from the card: %+v", n)
	}
	if d, err := store.ParseDecision(n.Decision); err != nil || len(d.Options) != 2 || !d.Options[0].Recommended {
		t.Fatalf("card not stored: %q %v", n.Decision, err)
	}
}

func TestResolveNeeds_DecideMailsTheChoiceAndClearsTheFlag(t *testing.T) {
	mailer := &fakeMailer{}
	mailSt := newFakeAmStore()
	mailSt.m[amKey+"\x00commander"] = "CalmCave"
	mailSt.m[amKey+"\x00manager-ms-1"] = "GoldTiger"
	orig := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) { return mailSt, mailer, amKey, func() {}, nil }
	t.Cleanup(func() { mailDeps = orig })
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)
	ctx := context.Background()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProjectByName(ctx, "manifest")
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: p.ID, BeadID: "ms-1.2", Ask: "Merge #4107?", Decision: testDecision}); err != nil {
		t.Fatal(err)
	}
	cleanup()

	stdout, stderr, _ := withTempOutputFiles(t)
	args := []string{"--project", "manifest", "--bead", "ms-1.2", "--action", "decide", "--choice", "Merge", "--text", "after lunch"}
	if code := runInternalResolveNeeds(args, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].to[0] != "GoldTiger" || mailer.sent[0].subject != "[decision] ms-1.2: Merge" ||
		!strings.Contains(mailer.sent[0].body, "[decision] ms-1.2: Merge + after lunch") {
		t.Fatalf("the Manager gets '[decision] step: choice + text': %+v", mailer.sent)
	}
	st, cleanup, _ = needsYouStoreFn()
	defer cleanup()
	if n, _ := st.GetNeedsYou(ctx, p.ID, "ms-1.2"); n != nil {
		t.Fatalf("the flag clears: %+v", n)
	}
	if !strings.Contains(strings.Join(mock.Calls, "\n"), "in-progress") && !strings.Contains(strings.Join(mock.Calls, "\n"), "SetStatus:ms-1.2") {
		t.Fatalf("the step goes back in progress: %v", mock.Calls)
	}
	if code := runInternalResolveNeeds([]string{"--project", "manifest", "--bead", "ms-1.2", "--action", "decide"}, stdout, stderr); code == 0 {
		t.Fatal("decide needs a choice or text")
	}
}

func TestSetObjective_ReviewLabelsThePR(t *testing.T) {
	mock := &beads.MockClient{ListResult: []beads.Bead{{ID: "ms-1.8", Title: "Stack", Status: "in_progress"}}}
	setupObjectiveProject(t, mock)
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "#4159"}, stdout, stderr); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	calls := strings.Join(mock.Calls, "\n")
	if !strings.Contains(calls, "label=in-review|on=true") || !strings.Contains(calls, "label=pr:4159|on=true") {
		t.Fatalf("calls:\n%s", calls)
	}
	stdout, stderr, _ = withTempOutputFiles(t)
	if runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "soon"}, stdout, stderr) == 0 {
		t.Fatal("a non-numeric PR is refused")
	}
}

// --max-operators sets the workstream's only max-operators:<n> label, and only
// on a workstream bead (the cap is read there).
func TestRunInternalSetObjective_MaxOperatorsOnWorkstreamOnly(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Labels: []string{"kind:objective", "max-operators:4"}}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--max-operators", "12"}, stdout, stderr); code != 0 {
		_, errOut := readOutput()
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	joined := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"SetLabel:obj-1|label=max-operators:4|on=false", "SetLabel:obj-1|label=max-operators:12|on=true"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}

	mock.Calls = nil
	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1.2", "--max-operators", "3"}, stdout, stderr); code == 0 {
		t.Fatal("--max-operators on a step was accepted")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "goes on the workstream bead obj-1") {
		t.Fatalf("stderr = %s", errOut)
	}
	if strings.Contains(strings.Join(mock.Calls, "\n"), "max-operators:3") {
		t.Fatalf("label set on a step: %v", mock.Calls)
	}

	stdout, stderr, _ = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "obj-1", "--max-operators", "0"}, stdout, stderr); code != 1 {
		t.Fatal("--max-operators 0 was accepted")
	}
}

func reviewNoneProject(t *testing.T, labels ...string) *beads.MemClient {
	t.Helper()
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "ms-1.8", Title: "Stack", Status: "open", Labels: labels})
	setupObjectiveProject(t, mem)
	return mem
}

func TestSetObjective_ReviewNoneRemovesEveryPRLabel(t *testing.T) {
	mem := reviewNoneProject(t, "kind:objective", "in-review", "pr:4159", "pr:acme/widgets#12", "pr:https://github.com/acme/widgets/pull/13")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "none"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Equal(b.Labels, []string{"kind:objective"}) {
		t.Fatalf("labels: %v", b.Labels)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "none"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("clearing twice: exit=%d: %s", code, errOut)
	}
}

// A review wait with no PR could never clear: the command has to deal with it too.
func TestSetObjective_ReviewNoneRefusesARemainingReviewWait(t *testing.T) {
	wait := "waiting:1793900000:review:some-reviewer"
	mem := reviewNoneProject(t, "kind:objective", "in-review", "pr:4159", wait)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "none"}, stdout, stderr); code == 0 {
		t.Fatal("refused")
	}
	if _, errOut := read(); !strings.Contains(errOut, "--waiting") {
		t.Fatalf("names the way out: %s", errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Contains(b.Labels, "pr:4159") || !slices.Contains(b.Labels, "in-review") {
		t.Fatalf("a refusal clears nothing: %v", b.Labels)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "none", "--waiting", "none"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	// The user's write dropped the step's only holder: it owes the Manager's "Manager to start"
	// receipt (custody), and nothing else is left.
	b, _ = mem.Show(context.Background(), "/d", "ms-1.8")
	var rest []string
	receipt := false
	for _, l := range b.Labels {
		if ws := beads.WaitsOf([]string{l}); len(ws) == 1 && beads.IsManagerHandoff(ws[0]) {
			receipt = true
			continue
		}
		rest = append(rest, l)
	}
	if !receipt || !slices.Equal(rest, []string{"kind:objective"}) {
		t.Fatalf("labels: %v", b.Labels)
	}
}

// A failed read clears nothing: the label is what the merge rule judges.
func TestSetObjective_ReviewNoneWithAFailedReadClearsNothing(t *testing.T) {
	mock := &beads.MockClient{ShowErr: errors.New("br is down")}
	setupObjectiveProject(t, mock)
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "none"}, stdout, stderr); code == 0 {
		t.Fatal("refused")
	}
	if calls := strings.Join(mock.Calls, "\n"); strings.Contains(calls, "SetLabel") {
		t.Fatalf("calls:\n%s", calls)
	}
}

// reviewClearNotes is every comment on the bead that records a clear.
func reviewClearNotes(t *testing.T, mem *beads.MemClient, id string) []string {
	t.Helper()
	cs, err := mem.Comments(context.Background(), "/d", id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		if strings.HasPrefix(c.Text, "review labels cleared by ") {
			out = append(out, c.Text)
		}
	}
	return out
}

func runReviewNone(t *testing.T, id string, extra ...string) (int, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalSetObjective(append([]string{"--project", "manifest", "--bead", id, "--review", "none"}, extra...), stdout, stderr)
	_, errOut := read()
	return code, errOut
}

// The clear is a recorded act: who, each label it removed, and why. Look-alike
// labels stay, and the record does not name them.
func TestSetObjective_ReviewNoneRecordsEachRemovedLabel(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "manager-ms-1")
	lookAlikes := []string{"pr-ready", "prod-gate:x", "probe:1", "ticket:TEAM-12"}
	mem := reviewNoneProject(t, append([]string{"kind:objective", "in-review", "pr:12", "PR:13", "pr:acme/widgets#34"}, lookAlikes...)...)
	if code, errOut := runReviewNone(t, "ms-1.8", "--reason", "the label went on the wrong step"); code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Equal(b.Labels, append([]string{"kind:objective"}, lookAlikes...)) {
		t.Fatalf("labels: %v", b.Labels)
	}
	notes := reviewClearNotes(t, mem, "ms-1.8")
	if len(notes) != 1 {
		t.Fatalf("one record: %q", notes)
	}
	for _, want := range []string{"manager-ms-1", "in-review", "pr:12", "PR:13", "pr:acme/widgets#34", "the label went on the wrong step"} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("record has no %q: %s", want, notes[0])
		}
	}
	for _, kept := range lookAlikes {
		if strings.Contains(notes[0], kept) {
			t.Errorf("record names %q, which stayed: %s", kept, notes[0])
		}
	}
	// Nothing left to remove: nothing to record.
	if code, errOut := runReviewNone(t, "ms-1.8"); code != 0 {
		t.Fatalf("clearing twice: exit=%d: %s", code, errOut)
	}
	if notes := reviewClearNotes(t, mem, "ms-1.8"); len(notes) != 1 {
		t.Fatalf("an empty clear records nothing: %q", notes)
	}
}

// No reason is needed on ordinary work; the record says there was none.
func TestSetObjective_ReviewNoneWithNoReasonSaysSo(t *testing.T) {
	mem := reviewNoneProject(t, "kind:objective", "in-review")
	if code, errOut := runReviewNone(t, "ms-1.8"); code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	notes := reviewClearNotes(t, mem, "ms-1.8")
	if len(notes) != 1 || !strings.Contains(notes[0], "in-review") || !strings.Contains(notes[0], "no reason given") {
		t.Fatalf("record: %q", notes)
	}
}

// A merge target's or merged gate's stage's label needs a reason. The live-PR read
// is deliberately absent: GitHub is never asked, so a failed read cannot open this.
func TestSetObjective_ReviewNoneOnAMergeTargetNeedsAReason(t *testing.T) {
	const enough, short = "wrong step, TEAM-12", "wrong step"
	merged := beads.Bead{ID: "ms-1.9", Title: "PR merged", Status: "open", Labels: []string{"gate", "gate-name:merged", "gate-of:" + beads.EscapeLabel("ms-1.8")}}
	cases := []struct {
		name   string
		bead   beads.Bead
		others []beads.Bead
		needs  bool
		label  string
	}{
		{name: "merged gate", bead: beads.Bead{Title: "PR merged", Labels: []string{"gate", "gate-name:merged", "pr:7"}}, needs: true, label: "pr:7"},
		{name: "gate titled merge", bead: beads.Bead{Title: "Merge to main", Labels: []string{"gate", "pr:acme/widgets#7"}}, needs: true, label: "pr:acme/widgets#7"},
		{name: "merge step", bead: beads.Bead{Title: "Merge the fix", Labels: []string{"kind:objective", "pr:7"}}, needs: true, label: "pr:7"},
		{name: "merge step, other spelling", bead: beads.Bead{Title: "Merge the fix", Labels: []string{"kind:objective", "PR:7"}}, needs: true, label: "PR:7"},
		{name: "merged gate's stage", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}, others: []beads.Bead{merged}, needs: true, label: "pr:7"},
		{name: "parked merged gate's stage", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}, others: []beads.Bead{{ID: merged.ID, Title: merged.Title, Status: "deferred", Labels: merged.Labels}}, needs: true, label: "pr:7"},
		{name: "plain step", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}},
		{name: "merge-worded work that is not the merge", bead: beads.Bead{Title: "Fix merge conflicts", Labels: []string{"kind:objective", "pr:7"}}},
		{name: "merge step with no pr label", bead: beads.Bead{Title: "Merge the fix", Labels: []string{"kind:objective", "in-review"}}},
		{name: "merged gate with no pr label", bead: beads.Bead{Title: "PR merged", Labels: []string{"gate", "gate-name:merged", "in-review"}}},
		{name: "closed merge step", bead: beads.Bead{Title: "Merge the fix", Status: "closed", Labels: []string{"kind:objective", "pr:7"}}},
		{name: "stage of another kind of gate", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}, others: []beads.Bead{{ID: "ms-1.9", Title: "Spec approved", Status: "open", Labels: []string{"gate", "gate-name:spec", "gate-of:" + beads.EscapeLabel("ms-1.8")}}}},
		{name: "stage of a closed merged gate", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}, others: []beads.Bead{{ID: merged.ID, Title: merged.Title, Status: "closed", Labels: merged.Labels}}},
		{name: "another stage's merged gate", bead: beads.Bead{Title: "Build it", Labels: []string{"kind:objective", "pr:7"}}, others: []beads.Bead{{ID: merged.ID, Title: merged.Title, Status: "open", Labels: []string{"gate", "gate-name:merged", "gate-of:" + beads.EscapeLabel("ms-1.7")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := func() *beads.MemClient {
				mem := &beads.MemClient{}
				b := tc.bead
				b.ID = "ms-1.8"
				if b.Status == "" {
					b.Status = "open"
				}
				mem.Seed(b)
				for _, o := range tc.others {
					mem.Seed(o)
				}
				setupObjectiveProject(t, mem)
				return mem
			}
			pinWorkflowGH(t, func(context.Context, ...string) ([]byte, error) {
				t.Error("--review none asked GitHub")
				return nil, errors.New("offline")
			})
			mem := seed()
			code, errOut := runReviewNone(t, "ms-1.8")
			b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
			if !tc.needs {
				if code != 0 || slices.ContainsFunc(b.Labels, func(l string) bool { return strings.HasPrefix(strings.ToLower(l), "pr:") }) {
					t.Fatalf("no reason needed: exit=%d labels=%v: %s", code, b.Labels, errOut)
				}
				return
			}
			if code == 0 || !strings.Contains(errOut, tc.label) || !strings.Contains(errOut, "--reason") {
				t.Fatalf("refused naming %s and --reason: exit=%d: %s", tc.label, code, errOut)
			}
			if !slices.Equal(b.Labels, tc.bead.Labels) || len(reviewClearNotes(t, mem, "ms-1.8")) != 0 {
				t.Fatalf("a refusal clears and records nothing: %v", b.Labels)
			}
			if code, _ := runReviewNone(t, "ms-1.8", "--reason", short); code == 0 {
				t.Fatal("a short reason passed")
			}
			if code, _ := runReviewNone(t, "ms-1.8", "--reason", "      padded       "); code == 0 {
				t.Fatal("padding passed")
			}
			if code, errOut := runReviewNone(t, "ms-1.8", "--reason", enough); code != 0 {
				t.Fatalf("with a reason: exit=%d: %s", code, errOut)
			}
			b, _ = mem.Show(context.Background(), "/d", "ms-1.8")
			if slices.Contains(b.Labels, tc.label) {
				t.Fatalf("labels: %v", b.Labels)
			}
			notes := reviewClearNotes(t, mem, "ms-1.8")
			if len(notes) != 1 || !strings.Contains(notes[0], tc.label) || !strings.Contains(notes[0], enough) {
				t.Fatalf("record: %q", notes)
			}
		})
	}
}

// failingListClient cannot read which gates guard a stage.
type failingListClient struct{ *beads.MemClient }

func (failingListClient) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	return nil, errors.New("br is down")
}

// A failed read of the stage's gates opens nothing; a reason needs no such read.
func TestSetObjective_ReviewNoneWithAnUnreadableGateListClearsNothing(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "ms-1.8", Title: "Build it", Status: "open", Labels: []string{"kind:objective", "pr:7"}})
	setupObjectiveProject(t, failingListClient{mem})
	if code, errOut := runReviewNone(t, "ms-1.8"); code == 0 || !strings.Contains(errOut, "nothing cleared") {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Contains(b.Labels, "pr:7") {
		t.Fatalf("labels: %v", b.Labels)
	}
	if code, errOut := runReviewNone(t, "ms-1.8", "--reason", "wrong step, TEAM-12"); code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
}

// failingCommentClient cannot write the record.
type failingCommentClient struct{ *beads.MemClient }

func (failingCommentClient) AddComment(context.Context, string, string, string) error {
	return errors.New("br is down")
}

// No record, no clear.
func TestSetObjective_ReviewNoneWithAFailedRecordClearsNothing(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "ms-1.8", Title: "Build it", Status: "open", Labels: []string{"kind:objective", "in-review", "pr:7"}})
	setupObjectiveProject(t, failingCommentClient{mem})
	if code, _ := runReviewNone(t, "ms-1.8"); code == 0 {
		t.Fatal("the failed record was not reported")
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Contains(b.Labels, "pr:7") || !slices.Contains(b.Labels, "in-review") {
		t.Fatalf("labels: %v", b.Labels)
	}
}

// As hold, park and push-reset: an Operator asks its Manager. Setting a PR stays its own.
func TestSetObjective_ReviewNoneRefusesAnOperator(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-op-x")
	mem := reviewNoneProject(t, "kind:objective", "in-review", "pr:7")
	if code, errOut := runReviewNone(t, "ms-1.8", "--reason", "wrong step, TEAM-12"); code == 0 || !strings.Contains(errOut, "Manager") {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Contains(b.Labels, "pr:7") || !slices.Contains(b.Labels, "in-review") || len(reviewClearNotes(t, mem, "ms-1.8")) != 0 {
		t.Fatalf("labels: %v", b.Labels)
	}
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", "ms-1.8", "--review", "8"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("--review 8: exit=%d: %s", code, errOut)
	}
}

// --review none takes the PR away, so a review wait in the same command has none,
// whatever the bead carries now. A refusal clears and records nothing.
func TestSetObjective_ReviewNoneRefusesAReviewWaitInTheSameCommand(t *testing.T) {
	mem := reviewNoneProject(t, "kind:objective", "in-review", "pr:12")
	code, errOut := runReviewNone(t, "ms-1.8", "--waiting", "review:some-reviewer")
	if code == 0 || !strings.Contains(errOut, "names no PR") {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ := mem.Show(context.Background(), "/d", "ms-1.8")
	if !slices.Equal(b.Labels, []string{"kind:objective", "in-review", "pr:12"}) || len(reviewClearNotes(t, mem, "ms-1.8")) != 0 {
		t.Fatalf("labels: %v notes: %q", b.Labels, reviewClearNotes(t, mem, "ms-1.8"))
	}
	// A wait of another kind replaces the review wait, as --waiting always does.
	wait := "waiting:1793900000:review:some-reviewer"
	mem = reviewNoneProject(t, "kind:objective", "pr:12", wait)
	code, errOut = runReviewNone(t, "ms-1.8")
	if code == 0 || !strings.Contains(errOut, "not a review") {
		t.Fatalf("the refusal must not lead to another review wait: exit=%d: %s", code, errOut)
	}
	if code, errOut := runReviewNone(t, "ms-1.8", "--waiting", "ci:#4"); code != 0 {
		t.Fatalf("exit=%d: %s", code, errOut)
	}
	b, _ = mem.Show(context.Background(), "/d", "ms-1.8")
	if slices.Contains(b.Labels, wait) || slices.Contains(b.Labels, "pr:12") {
		t.Fatalf("labels: %v", b.Labels)
	}
}

// Real bd: --review none removes each spelling --review can write, and a repo
// qualified label survives the label grammar on the way in and out.
func TestReviewNone_BDIntegration(t *testing.T) {
	if os.Getenv("COMMANDER_BD_INTEGRATION") == "" {
		t.Skip("set COMMANDER_BD_INTEGRATION=1 to run against a real bd server")
	}
	realBD, err := exec.LookPath("bd")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", "bd")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	ctx := context.Background()
	c := beads.New()
	setupObjectiveProject(t, c)
	st, err := store.Open(filepath.Join(os.Getenv("COMMANDER_HOME"), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project, err := st.GetProjectByName(ctx, "manifest")
	if err != nil {
		t.Fatal(err)
	}
	dir := project.Path
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := c.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(realBD, "-C", dir, "dolt", "stop").Run() })
	id, err := c.Create(ctx, dir, "Stack", beads.CreateOptions{Labels: []string{"kind:objective"}, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	run := func(review string) {
		t.Helper()
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalSetObjective([]string{"--project", "manifest", "--bead", id, "--review", review}, stdout, stderr); code != 0 {
			_, errOut := read()
			t.Fatalf("--review %s: exit=%d: %s", review, code, errOut)
		}
	}
	run("12")
	run("acme/widgets#34")
	b, err := beads.FreshShow(ctx, c, dir, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after --review 12 and acme/widgets#34: %v", b.Labels)
	if !slices.Contains(b.Labels, "pr:12") || !slices.Contains(b.Labels, "pr:acme/widgets#34") {
		t.Fatalf("labels: %v", b.Labels)
	}
	run("none")
	if b, err = beads.FreshShow(ctx, c, dir, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("after --review none: %v", b.Labels)
	if !slices.Equal(b.Labels, []string{"kind:objective"}) {
		t.Fatalf("labels: %v", b.Labels)
	}
	cs, err := c.Comments(ctx, dir, id)
	if err != nil || len(cs) != 1 || !strings.Contains(cs[0].Text, "pr:12, pr:acme/widgets#34, in-review") {
		t.Fatalf("record: %v %+v", err, cs)
	}
	t.Logf("record: %s", cs[0].Text)

	// The stage of a merged gate, found through the real label filter.
	stage, err := c.Create(ctx, dir, "Build it", beads.CreateOptions{Labels: []string{"kind:objective", "pr:7"}, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, dir, "PR merged", beads.CreateOptions{Labels: []string{"gate", "gate-name:merged", "gate-of:" + beads.EscapeLabel(stage)}, Silent: true}); err != nil {
		t.Fatal(err)
	}
	if code, errOut := runReviewNone(t, stage); code == 0 || !strings.Contains(errOut, "pr:7") {
		t.Fatalf("the stage of a merged gate: exit=%d: %s", code, errOut)
	}
	if code, errOut := runReviewNone(t, stage, "--reason", "wrong step, TEAM-12"); code != 0 {
		t.Fatalf("with a reason: exit=%d: %s", code, errOut)
	}
	if b, err = beads.FreshShow(ctx, c, dir, stage); err != nil || slices.Contains(b.Labels, "pr:7") {
		t.Fatalf("labels: %v %v", err, b)
	}
}
