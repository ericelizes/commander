package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
)

func gateCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalGate(args, stdout, stderr)
	out, errOut := read()
	return code, out, errOut
}

func fakeGateRun(t *testing.T, fn func(command string) (string, error)) {
	t.Helper()
	orig := gateRunFn
	gateRunFn = func(_ context.Context, _, command string) (string, error) { return fn(command) }
	t.Cleanup(func() { gateRunFn = orig })
}

// setupGateProject is setupObjectiveProject with a stateful beads client: gate
// beads created, commented and closed are what later calls read back.
func setupGateProject(t *testing.T) *beads.MemClient {
	t.Helper()
	mem := &beads.MemClient{Prefix: "ms"}
	mem.Seed(beads.Bead{ID: "ms-1.2", Title: "Stage", Labels: []string{"kind:objective"}})
	setupObjectiveProject(t, &beads.MockClient{})
	newBeadsClientFn = func() beads.Client { return mem }
	return mem
}

func gatesNow(t *testing.T, bead string) []store.Gate {
	t.Helper()
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, err := st.GetProjectByName(context.Background(), "manifest")
	if err != nil || p == nil {
		t.Fatal("no project")
	}
	ns, err := gates.ForStage(context.Background(), newBeadsClientFn(), p.Path, p.ID, bead)
	if err != nil {
		t.Fatal(err)
	}
	var gs []store.Gate
	for _, n := range ns {
		gs = append(gs, n.Gate)
	}
	return gs
}

// waitLabels are the waiting:... labels set on a bead.
func waitLabels(mem *beads.MemClient, id string) []string {
	var out []string
	for _, b := range mem.Beads {
		if b.ID == id {
			for _, l := range b.Labels {
				if strings.HasPrefix(l, "waiting:") {
					out = append(out, l)
				}
			}
		}
	}
	return out
}

func TestGateCheckRunsAutoGatesAndWaitsOnPeople(t *testing.T) {
	mem := setupGateProject(t)
	mem.Beads[0].Labels = append(mem.Beads[0].Labels, "waiting:5:user")
	allowManyGates(t)
	var ran []string
	fakeGateRun(t, func(command string) (string, error) {
		ran = append(ran, command)
		if command == "false" {
			return "boom\n", errors.New("exit status 1")
		}
		return "  ok  \n", nil
	})
	for _, a := range [][]string{
		{"--name", "unit", "--check", "go test ./...", "--verifier", "auto"},
		{"--name", "broken", "--check", "false", "--verifier", "auto"},
		{"--name", "review", "--check", "no TODOs", "--verifier", "reviewer:reviewer-a"},
		{"--name", "signoff", "--check", "looks right", "--verifier", "user"},
	} {
		if code, _, errOut := gateCmd(t, append([]string{"add", "--project", "manifest", "--bead", "ms-1.2"}, a...)...); code != 0 {
			t.Fatalf("add %v: %s", a, errOut)
		}
	}
	code, out, errOut := gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2")
	if code != 0 {
		t.Fatalf("check: %s", errOut)
	}
	if strings.Join(ran, ",") != "go test ./...,false" {
		t.Errorf("ran %v: only auto checks run", ran)
	}
	got := map[string]store.Gate{}
	for _, g := range gatesNow(t, "ms-1.2") {
		got[g.Name] = g
	}
	if g := got["unit"]; g.Status != "passed" || g.Evidence != "ok" || g.By != "auto" || g.At == 0 {
		t.Errorf("unit = %+v", g)
	}
	if g := got["broken"]; g.Status != "failed" || !strings.Contains(g.Evidence, "boom") || !strings.Contains(g.Evidence, "exit status 1") {
		t.Errorf("broken = %+v", g)
	}
	if got["review"].Status != "open" || got["signoff"].Status != "open" {
		t.Errorf("people's gates must stay open: %+v", got)
	}
	waits := strings.Join(waitLabels(mem, "ms-1.2"), " ")
	if !strings.Contains(waits, ":review:reviewer-a") {
		t.Errorf("no review wait: %s", waits)
	}
	if strings.Count(waits, ":user") != 1 {
		t.Errorf("the user wait already existed and must stay single: %s", waits)
	}
	// a gate is a bead: closed = passed, a failed check is labelled, evidence is a comment
	for _, b := range mem.Beads {
		if !strings.HasSuffix(b.Title, "gate unit") {
			continue
		}
		cs, _ := mem.Comments(context.Background(), "", b.ID)
		if b.Status != "closed" || len(cs) != 1 || !strings.HasPrefix(cs[0].Text, "gate passed by auto at ") || !strings.HasSuffix(cs[0].Text, ": ok") {
			t.Errorf("unit bead = %+v comments %+v", b, cs)
		}
	}
	if !strings.Contains(out, `"name":"unit"`) {
		t.Errorf("out = %s", out)
	}
}

func TestCloseRefusedUntilEveryGatePassed(t *testing.T) {
	allowManyGates(t)
	mem := setupGateProject(t)
	fakeGateRun(t, func(string) (string, error) { return "", nil })
	gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "unit", "--check", "true", "--verifier", "auto")
	gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "signoff", "--check", "ok?", "--verifier", "user")

	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "ms-1.2"}, stdout, stderr); code == 0 {
		t.Fatal("closed with open gates")
	}
	_, errOut := read()
	if !strings.Contains(errOut, "unit (auto, open)") || !strings.Contains(errOut, "signoff (user, open)") {
		t.Errorf("error names the gates: %s", errOut)
	}
	if b := mem.Beads[0]; b.Status == "closed" {
		t.Errorf("stage closed with open gates: %+v", b)
	}

	gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2")
	if gs := gatesNow(t, "ms-1.2"); gs[0].Status != "passed" || gs[1].Status != "open" {
		t.Errorf("after check: %+v", gs)
	}
	if code, _, errOut := gateCmd(t, "pass", "--bead", "ms-1.2", "--name", "unit", "--evidence", "x"); code == 0 || !strings.Contains(errOut, "auto") {
		t.Errorf("an auto gate was passed by hand: %s", errOut)
	}
	if code, _, _ := gateCmd(t, "pass", "--bead", "ms-1.2", "--name", "signoff"); code == 0 {
		t.Error("passed without evidence")
	}
	if code, _, errOut := gateCmd(t, "pass", "--bead", "ms-1.2", "--name", "signoff", "--evidence", "the user: ok in chat", "--by", "user"); code != 0 {
		t.Fatalf("pass: %s", errOut)
	}
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "ms-1.2"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("close with every gate passed: %s", errOut)
	}
	if b := mem.Beads[0]; b.Status != "closed" {
		t.Errorf("stage not closed: %+v", b)
	}
}

func TestGateListAndRemove(t *testing.T) {
	allowManyGates(t)
	mem := setupGateProject(t)
	if code, _, _ := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "x", "--check", "c", "--verifier", "bob"); code == 0 {
		t.Error("bad verifier accepted")
	}
	gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "x", "--check", "c", "--verifier", "user")
	code, out, _ := gateCmd(t, "list", "--bead", "ms-1.2", "--json")
	if code != 0 || !strings.Contains(out, `"name":"x"`) || !strings.Contains(out, `"verifier":"user"`) {
		t.Fatalf("list = %d %s", code, out)
	}
	if code, _, _ := gateCmd(t, "rm", "--bead", "ms-1.2", "--name", "x"); code != 0 {
		t.Fatal("rm failed")
	}
	if code, out, _ := gateCmd(t, "list", "--bead", "ms-1.2", "--json"); code != 0 || !strings.Contains(out, `"gates":[]`) {
		t.Errorf("list after rm = %s", out)
	}
	// rm retires the bead (closed + retired), it does not tombstone it.
	var gate beads.Bead
	for _, b := range mem.Beads {
		if gates.IsGate(*b) {
			gate = *b
		}
	}
	if gate.Status != "closed" || !gates.IsRetired(gate) {
		t.Errorf("rm left the gate bead %+v", gate)
	}
}

func TestGateAddValidatesAutoChecks(t *testing.T) {
	allowManyGates(t)
	setupGateProject(t)
	add := func(check, verifier string) (int, string, string) {
		return gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "g", "--check", check, "--verifier", verifier)
	}
	if code, _, errOut := add("if then fi (", "auto"); code == 0 || !strings.Contains(errOut, "not valid shell") {
		t.Errorf("invalid shell accepted: %d %s", code, errOut)
	}
	if code, _, errOut := add("cd .worktrees/op-x/app && just test", "auto"); code == 0 || !strings.Contains(errOut, "gh") {
		t.Errorf("worktree path accepted: %d %s", code, errOut)
	}
	code, out, _ := add("ensure the app looks right to everyone", "auto")
	if code != 0 || !strings.Contains(out, "reads as prose") {
		t.Errorf("prose should warn, not reject: %d %s", code, out)
	}
	code, out, _ = add("go test ./...", "auto")
	if code != 0 || strings.Contains(out, "warnings") {
		t.Errorf("a command should pass quietly: %d %s", code, out)
	}
	// non-auto verifiers keep any text, including worktree mentions
	if code, _, errOut := add("see .worktrees/op-x", "user"); code != 0 {
		t.Errorf("user gate: %s", errOut)
	}
}

func TestOperatorGateIsNeverRunAndBlocksCloseUntilPassed(t *testing.T) {
	allowManyGates(t)
	mem := setupGateProject(t)
	fakeGateRun(t, func(command string) (string, error) {
		t.Errorf("operator gate must not run: %q", command)
		return "", nil
	})
	if code, _, errOut := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "lint", "--check", "wire just lint when the track starts", "--verifier", "operator"); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2"); code != 0 {
		t.Fatal(errOut)
	}
	if w := waitLabels(mem, "ms-1.2"); len(w) != 0 {
		t.Errorf("operator gate must not create a wait: %v", w)
	}
	if g := gatesNow(t, "ms-1.2")[0]; g.Status != "open" || g.Verifier != "operator" {
		t.Errorf("gate = %+v", g)
	}
	if err := requireGatesPassed(context.Background(), "manifest", "ms-1.2"); err == nil || !strings.Contains(err.Error(), "lint") {
		t.Errorf("close should refuse: %v", err)
	}
	if code, _, errOut := gateCmd(t, "pass", "--bead", "ms-1.2", "--name", "lint", "--evidence", "just lint exit 0", "--by", "op"); code != 0 {
		t.Fatal(errOut)
	}
	if err := requireGatesPassed(context.Background(), "manifest", "ms-1.2"); err != nil {
		t.Errorf("close after pass: %v", err)
	}
	code, out, _ := gateCmd(t, "list", "--bead", "ms-1.2")
	if code != 0 || !strings.Contains(out, "operator") {
		t.Errorf("list: %s", out)
	}
}

func TestGateAddRefusesASecondGateAndWarnsOnUserWithoutReason(t *testing.T) {
	setupGateProject(t)
	add := func(bead, name, verifier string, extra ...string) (int, string, string) {
		return gateCmd(t, append([]string{"add", "--project", "manifest", "--bead", bead, "--name", name, "--check", "x", "--verifier", verifier}, extra...)...)
	}
	if code, out, _ := add("ms-1.2", "b", "user"); code != 0 || !strings.Contains(out, "genuine decisions") {
		t.Fatalf("user without --reason should add with a warning: %d %s", code, out)
	}
	if code, _, errOut := add("ms-1.2", "c", "operator"); code == 0 || !strings.Contains(errOut, "the stage is the gate") {
		t.Fatalf("second gate must be refused: %d %s", code, errOut)
	}
	if code, out, _ := add("ms-1.2", "b", "user", "--reason", "prod merge"); code != 0 || strings.Contains(out, "genuine decisions") {
		t.Fatalf("re-adding b with a reason: %d %s", code, out)
	}
}

func TestGateTemplates(t *testing.T) {
	a := templateArgs{Repo: "o/r", PRs: []string{"4107", "4101"}, Reviewers: []string{"reviewer-a", "reviewer-b"}}
	for _, tpl := range gateTemplates {
		g, err := templateGate(tpl, a)
		if err != nil || g.Name != tpl || g.Check == "" || g.Verifier == "" {
			t.Fatalf("%s: %+v %v", tpl, g, err)
		}
		if g.Verifier == "auto" {
			if _, err := validateAutoCheck(g.Check); err != nil {
				t.Errorf("%s auto check invalid: %v\n%s", tpl, err, g.Check)
			}
		}
	}
	m, _ := templateGate("merged", a)
	if strings.Index(m.Check, "4107") > strings.Index(m.Check, "4101") {
		t.Errorf("stacks must be checked in the given order: %s", m.Check)
	}
	if g, _ := templateGate("pr-approved", a); g.Verifier != "reviewer:reviewer-a" || !strings.Contains(g.Check, "reviewer-b") {
		t.Errorf("pr-approved: %+v", g)
	}
	if g, _ := templateGate("reviewed", templateArgs{}); g.Verifier != "operator" || !strings.Contains(g.Check, "CLEAN") {
		t.Errorf("reviewed is an operator gate needing a clean review: %+v", g)
	}
	if g, _ := templateGate("live", templateArgs{}); g.Verifier != "operator" {
		t.Errorf("live without a check is an operator gate: %+v", g)
	}
	if g, _ := templateGate("live", templateArgs{LiveCheck: "curl -sf https://x/health"}); g.Verifier != "auto" {
		t.Errorf("live with a check is auto: %+v", g)
	}
	if _, err := templateGate("pr-ready", templateArgs{}); err == nil {
		t.Error("pr-ready needs --pr")
	}
	if _, err := templateGate("nope", a); err == nil {
		t.Error("unknown template must fail")
	}
}

func TestGateApplyReplacesTheStagesGate(t *testing.T) {
	setupGateProject(t)
	if code, _, e := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "old", "--check", "x", "--verifier", "operator"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := gateCmd(t, "apply", "--project", "manifest", "--bead", "ms-1.2", "--template", "merged", "--repo", "o/r", "--pr", "7")
	if code != 0 || !strings.Contains(out, `"dropped":["old"]`) {
		t.Fatalf("apply: %d %s %s", code, out, e)
	}
	if gs := gatesNow(t, "ms-1.2"); len(gs) != 1 || gs[0].Name != "merged" {
		t.Fatalf("stage should hold exactly the merged gate: %+v", gs)
	}
}

// allowManyGates lifts the one-gate-per-stage cap for tests that exercise
// several gates on one stage.
func allowManyGates(t *testing.T) {
	old := maxGatesPerStage
	maxGatesPerStage = 10
	t.Cleanup(func() { maxGatesPerStage = old })
}

func TestGateRedefineResetsAndFailedThenPassedReopensNothing(t *testing.T) {
	mem := setupGateProject(t)
	runs := 0
	fakeGateRun(t, func(string) (string, error) {
		runs++
		if runs == 1 {
			return "red", errors.New("exit status 1")
		}
		return "green", nil
	})
	add := func() {
		if code, _, e := gateCmd(t, "add", "--project", "manifest", "--bead", "ms-1.2", "--name", "unit", "--check", "go test ./...", "--verifier", "auto"); code != 0 {
			t.Fatal(e)
		}
	}
	add()
	gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2")
	if g := gatesNow(t, "ms-1.2")[0]; g.Status != "failed" || !strings.Contains(g.Evidence, "red") {
		t.Fatalf("failed gate = %+v", g)
	}
	gateCmd(t, "check", "--project", "manifest", "--bead", "ms-1.2")
	if g := gatesNow(t, "ms-1.2")[0]; g.Status != "passed" || g.Evidence != "green" {
		t.Fatalf("a later green check passes it and drops the failed label: %+v", g)
	}
	add() // redefining resets to open, on a fresh bead
	if gs := gatesNow(t, "ms-1.2"); len(gs) != 1 || gs[0].Status != "open" || gs[0].Evidence != "" {
		t.Fatalf("redefined gate = %+v", gs)
	}
	live := 0
	for _, b := range mem.Beads {
		if b.Status != "tombstone" && gates.IsGate(*b) {
			live++
		}
	}
	if live != 1 {
		t.Errorf("want one live gate bead, got %d", live)
	}
}

func TestGateMigrateMovesTableRowsIntoBeadsAndIsIdempotent(t *testing.T) {
	allowManyGates(t)
	mem := setupGateProject(t)
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	p, _ := st.GetProjectByName(ctx, "manifest")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertGate(ctx, store.Gate{ProjectID: p.ID, BeadID: "ms-1.2", Name: "unit", Check: "go test ./...", Verifier: "auto"}))
	must(st.SetGateResult(ctx, p.ID, "ms-1.2", "unit", store.GatePassed, "ok", "auto", time.Unix(70, 0)))
	must(st.UpsertGate(ctx, store.Gate{ProjectID: p.ID, BeadID: "ms-1.2", Name: "signoff", Check: "ship?", Verifier: "user"}))

	for i := 0; i < 2; i++ { // the second run changes nothing
		code, out, e := gateCmd(t, "migrate")
		if code != 0 {
			t.Fatal(e)
		}
		want := `{"migrated":0}`
		if i == 0 {
			want = `{"migrated":2}`
		}
		if strings.TrimSpace(out) != want {
			t.Errorf("run %d: %s", i, out)
		}
	}
	gs := gatesNow(t, "ms-1.2")
	if len(gs) != 2 || gs[0].Name != "unit" || gs[0].Status != "passed" || gs[0].Evidence != "ok" || gs[0].By != "auto" || gs[0].At != 70 || gs[0].Check != "go test ./..." {
		t.Fatalf("migrated gates = %+v", gs)
	}
	if gs[1].Name != "signoff" || gs[1].Status != "open" || gs[1].Verifier != "user" {
		t.Errorf("signoff = %+v", gs[1])
	}
	if left, _ := st.ListAllGates(ctx); len(left) != 0 {
		t.Errorf("table rows left behind: %+v", left)
	}
	var user bool
	for _, b := range mem.Beads {
		if strings.HasSuffix(b.Title, "gate signoff") {
			user = strings.Contains(strings.Join(b.Labels, " "), "approver:user")
		}
	}
	if !user {
		t.Error("an user gate carries approver:user")
	}
	// a row whose bead already exists (a run that died before dropping the row) is only dropped
	must(st.UpsertGate(ctx, store.Gate{ProjectID: p.ID, BeadID: "ms-1.2", Name: "signoff", Check: "ship?", Verifier: "user"}))
	if code, out, _ := gateCmd(t, "migrate"); code != 0 || strings.TrimSpace(out) != `{"migrated":0}` {
		t.Errorf("rerun with an orphan row: %d %s", code, out)
	}
	if n := len(gatesNow(t, "ms-1.2")); n != 2 {
		t.Errorf("duplicate bead made: %d gates", n)
	}
}

// failingCreate fails Create for titles containing "bad", like br rejecting one gate.
type failingCreate struct{ beads.Client }

func (f failingCreate) Create(ctx context.Context, dir, title string, opts beads.CreateOptions) (string, error) {
	if strings.Contains(title, "bad") {
		return "", errors.New("br create: exit status 4")
	}
	return f.Client.Create(ctx, dir, title, opts)
}

func TestGateMigrateEveryGateShape(t *testing.T) {
	allowManyGates(t)
	mem := &beads.MemClient{Prefix: "ms"}
	setupObjectiveProject(t, &beads.MockClient{})
	newBeadsClientFn = func() beads.Client { return mem }
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	p, _ := st.GetProjectByName(ctx, "manifest")
	shapes := []store.Gate{
		{BeadID: "ms-t08.2", Name: "investigation", Check: "look", Verifier: "operator"},
		{BeadID: "ms-1.2.3", Name: "unit_tests", Check: "go test ./... && echo @#/ ok", Verifier: "auto"},
		{BeadID: "ms-1", Name: "signoff", Check: "ship?", Verifier: "user"},
		{BeadID: "ms_x-9.1", Name: "review-2", Check: "reviewed", Verifier: "reviewer:some_login-1"},
	}
	for _, g := range shapes {
		mem.Seed(beads.Bead{ID: g.BeadID})
		g.ProjectID = p.ID
		if err := st.UpsertGate(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, e := gateCmd(t, "migrate"); code != 0 || strings.TrimSpace(out) != `{"migrated":4}` {
		t.Fatalf("migrate: %d %s %s", code, out, e)
	}
	for _, g := range shapes {
		gs := gatesNow(t, g.BeadID)
		if len(gs) != 1 || gs[0].Name != g.Name || gs[0].Verifier != g.Verifier || gs[0].BeadID != g.BeadID || gs[0].Check != g.Check {
			t.Errorf("%s: gates = %+v", g.BeadID, gs)
		}
	}
}

func TestGateMigrateContinuesPastFailuresAndExitsNonzero(t *testing.T) {
	allowManyGates(t)
	mem := &beads.MemClient{Prefix: "ms"}
	mem.Seed(beads.Bead{ID: "ms-1"})
	setupObjectiveProject(t, &beads.MockClient{})
	newBeadsClientFn = func() beads.Client { return failingCreate{mem} }
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	p, _ := st.GetProjectByName(ctx, "manifest")
	for _, name := range []string{"first", "bad", "last"} {
		if err := st.UpsertGate(ctx, store.Gate{ProjectID: p.ID, BeadID: "ms-1", Name: name, Check: "c", Verifier: "auto"}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := gateCmd(t, "migrate")
	if code == 0 || !strings.Contains(out, `"migrated":2`) || !strings.Contains(out, "ms-1/bad") {
		t.Fatalf("want exit!=0 with 2 migrated and the failure listed: %d %s", code, out)
	}
	if left, _ := st.ListAllGates(ctx); len(left) != 1 || left[0].Name != "bad" {
		t.Errorf("only the failed row stays: %+v", left)
	}
	newBeadsClientFn = func() beads.Client { return mem } // the cause is fixed: resume
	if code, out, e := gateCmd(t, "migrate"); code != 0 || strings.TrimSpace(out) != `{"migrated":1}` {
		t.Errorf("resume: %d %s %s", code, out, e)
	}
}
