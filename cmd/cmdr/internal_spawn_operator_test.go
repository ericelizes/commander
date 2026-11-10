package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type fakeOperatorSpawner struct {
	reqs  []spawn.SpawnRequest
	err   error
	moved string // the result's Moved
}

func (f *fakeOperatorSpawner) Spawn(_ context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	slug := req.Name
	if slug == "" {
		slug = strings.ReplaceAll(req.Branch, "/", "-")
	}
	return &spawn.SpawnResult{AgentName: "operator-" + slug, Runtime: chat.RuntimeClaude, Account: "default", Moved: f.moved}, nil
}

type stubRuns []store.AgentRun

func (r stubRuns) ListRunningRuns(context.Context) ([]store.AgentRun, error) { return r, nil }

// stubSpawnDeps runs the real workstream cap (default max) against `running`
// Operators on tasks under workstream "s" (its own bead set, not the test's;
// "ms-1" is the other workstream these tests spawn under).
func stubSpawnDeps(t *testing.T, sp *fakeOperatorSpawner, running, max int) {
	t.Helper()
	ws := &beads.MemClient{}
	ws.Seed(beads.Bead{ID: "s"})
	ws.Seed(beads.Bead{ID: "ms-1"})
	var runs stubRuns
	for i := 0; i < running; i++ {
		id := fmt.Sprintf("s.%d", i+1)
		ws.Seed(beads.Bead{ID: id})
		runs = append(runs, store.AgentRun{ProjectID: 7, TaskBeadID: id, Status: "running"})
	}
	orig := spawnOperatorDeps
	spawnOperatorDeps = func(string) (*operatorSpawnDeps, error) {
		return &operatorSpawnDeps{
			Spawner:      sp,
			Project:      &store.Project{ID: 7, Name: "manifest", Type: "code", Path: t.TempDir()},
			DefaultModel: "anthropic/test",
			CheckCap: func(ctx context.Context, _ beads.Client, p *store.Project, step string) error {
				return spawn.CheckWorkstreamCap(ctx, runs, ws, p.ID, p.Path, step, max)
			},
		}, nil
	}
	t.Cleanup(func() { spawnOperatorDeps = orig })
}

// approvedWorkflowGate is the closed Workflow approved gate of workstream ms-1.
var approvedWorkflowGate = beads.Bead{ID: "ms-1.99", Title: "Workflow approved", Status: "closed", Labels: []string{beads.GateLabel}}

// useMockBeads serves m with ms-1's Workflow approved gate closed (added to the
// listing unless it has a gate of its own); useMockBeadsRaw serves it as is.
func useMockBeads(t *testing.T, m *beads.MockClient) {
	t.Helper()
	if !slices.ContainsFunc(m.ListResult, func(b beads.Bead) bool { return slices.Contains(b.Labels, beads.GateLabel) }) {
		m.ListResult = append(m.ListResult, approvedWorkflowGate)
	}
	useMockBeadsRaw(t, m)
}

func useMockBeadsRaw(t *testing.T, m *beads.MockClient) {
	t.Helper()
	// These helpers model an existing workflow; missing-target tests use other ids.
	if m.ShowResult == nil {
		if m.ShowResults == nil {
			m.ShowResults = map[string]*beads.Bead{}
		}
		for _, id := range []string{"ms-1", "ms-1.2", "ms-1.2.5", "ms-1.3", "ms-1.4", "s"} {
			if _, ok := m.ShowResults[id]; !ok {
				m.ShowResults[id] = &beads.Bead{ID: id, Status: "open"}
			}
		}
	}

	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return m }
	t.Cleanup(func() { newBeadsClientFn = orig })
}

func TestSpawnOperator_CreatesTaskSpawnsAndMarksTheTask(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.2.1"}
	useMockBeads(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "Fix the Import bug!", "--prompt", "do it"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}

	calls := strings.Join(mock.Calls, "\n")
	if !strings.Contains(calls, "Create:title=Fix the Import bug!|parent=ms-1.2|labels=kind:task,spawn-attempt:") {
		t.Fatalf("task must be a kind:task child of the step:\n%s", calls)
	}
	if len(sp.reqs) != 1 {
		t.Fatalf("spawn calls = %d", len(sp.reqs))
	}
	req := sp.reqs[0]
	if req.TaskBeadID != "ms-1.2.1" || req.Branch != "op/fix-the-import-bug-ms-1.2.1" || req.Model != "anthropic/test" || req.ProjectID != 7 {
		t.Fatalf("unexpected spawn request: %+v", req)
	}
	// The task, not the step: in progress, owned by the operator.
	for _, want := range []string{"SetStatus:ms-1.2.1|status=in_progress", "SetLabel:ms-1.2.1|label=owner:operator-op-fix-the-import-bug-ms-1-2-1|on=true"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "SetLabel:ms-1.2|") || strings.Contains(calls, "SetStatus:ms-1.2|") {
		t.Fatalf("the step's state follows from its children; it must not be set:\n%s", calls)
	}
	if !strings.Contains(out, `"agent":"operator-op-fix-the-import-bug-ms-1.2.1"`) {
		t.Fatalf("output: %s", out)
	}
}

func TestSpawnOperator_RefusesAtCapacityWithoutCreatingAnything(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 3, 3)
	mock := &beads.MockClient{CreateID: "x"}
	useMockBeads(t, mock)
	mailer := stubMail(t)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "s", "--title", "t"}, stdout, stderr); code == 0 {
		t.Fatal("expected failure at capacity")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "workstream s already has 3 running operators (max 3)") {
		t.Fatalf("stderr = %s", errOut)
	}
	// Validate the target, then read the step's Manager for the mail.
	if len(mock.Calls) != 2 || !strings.HasPrefix(mock.Calls[0], "Show:s|") || !strings.HasPrefix(mock.Calls[1], "Details:") || len(sp.reqs) != 0 {
		t.Fatalf("nothing should be created at capacity: %v %v", mock.Calls, sp.reqs)
	}
	if len(mailer.sent) != 1 || !strings.Contains(mailer.sent[0].body, "already has 3 running operators") {
		t.Fatalf("a capacity refusal must also mail Commander: %+v", mailer.sent)
	}
}

func TestSpawnOperator_SpawnFailureDeletesOrphanTaskAndLeavesStepAlone(t *testing.T) {
	sp := &fakeOperatorSpawner{err: errors.New("docker down")}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.1"}
	useMockBeads(t, mock)
	mailer := stubMail(t)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1", "--title", "t"}, stdout, stderr); code == 0 {
		t.Fatal("expected failure")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "docker down") {
		t.Fatalf("stderr = %s", errOut)
	}
	calls := strings.Join(mock.Calls, "\n")
	// Closed would read as finished work; the task never ran, so it is deleted.
	if !strings.Contains(calls, "Delete:ms-1.1") || strings.Contains(calls, "Close:ms-1.1") {
		t.Fatalf("orphan task should be deleted, not closed:\n%s", calls)
	}
	if strings.Contains(calls, "SetLabel") || strings.Contains(calls, "SetStatus") {
		t.Fatalf("step must not be touched on failure:\n%s", calls)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("expected one mail to Commander, got %+v", mailer.sent)
	}
	m := mailer.sent[0]
	if m.sender != "GoldTiger" || len(m.to) != 1 || m.to[0] != "CalmCave" {
		t.Fatalf("sender/recipient = %q -> %v (want the step's Manager -> commander)", m.sender, m.to)
	}
	if m.subject != "spawn failed: t (manifest)" {
		t.Fatalf("subject = %q", m.subject)
	}
	for _, want := range []string{"docker down", "Step: ms-1", "cmdr internal-spawn-operator --project manifest --step ms-1 --title t"} {
		if !strings.Contains(m.body, want) {
			t.Fatalf("body missing %q:\n%s", want, m.body)
		}
	}
}

func TestSpawnOperator_FailureMailIsHighImportanceAndItsFailureOnlyWarns(t *testing.T) {
	sp := &fakeOperatorSpawner{err: errors.New("no docker")}
	stubSpawnDeps(t, sp, 0, 3)
	useMockBeads(t, &beads.MockClient{CreateID: "ms-1.1"})

	var gotImportance string
	recorder := &importanceMailer{fakeMailer: stubMail(t), importance: &gotImportance}
	origDeps := mailDeps
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		st := newFakeAmStore()
		st.m[amKey+"\x00manager-ms-1"] = "GoldTiger"
		st.m[amKey+"\x00commander"] = "CalmCave"
		return st, recorder, amKey, func() {}, nil
	}
	t.Cleanup(func() { mailDeps = origDeps })

	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1", "--title", "t"}, stdout, stderr); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if gotImportance != "high" {
		t.Fatalf("importance = %q, want high", gotImportance)
	}

	// Mail server down: the exit code is unchanged and stderr carries a warning.
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		return nil, nil, "", nil, errors.New("am offline")
	}
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1", "--title", "t"}, stdout, stderr); code != 1 {
		t.Fatalf("exit = %d, a mail failure must not change it", code)
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "no docker") || !strings.Contains(errOut, "mailing Commander failed too: am offline") {
		t.Fatalf("stderr = %s", errOut)
	}
}

// importanceMailer records the importance of the message it relays.
type importanceMailer struct {
	*fakeMailer
	importance *string
}

func (m *importanceMailer) SendMessage(ctx context.Context, project, sender string, to []string, subject, body string, opts agentmail.SendOptions) error {
	*m.importance = opts.Importance
	return m.fakeMailer.SendMessage(ctx, project, sender, to, subject, body, opts)
}

func TestQuoteArgs(t *testing.T) {
	got := quoteArgs([]string{"--title", "Fix the bug", "--prompt", ""})
	if want := `--title "Fix the bug" --prompt ""`; got != want {
		t.Fatalf("quoteArgs = %s, want %s", got, want)
	}
}

func TestSpawnOperator_RequiresFlags(t *testing.T) {
	stdout, stderr, _ := withTempOutputFiles(t)
	if runInternalSpawnOperator([]string{"--project", "manifest", "--title", "t"}, stdout, stderr) == 0 {
		t.Fatal("--step is required")
	}
}

func TestOperatorBranch(t *testing.T) {
	cases := map[string]string{
		"Fix the Import bug!": "op/fix-the-import-bug-t1",
		"!!!":                 "op/t1",
	}
	for title, want := range cases {
		if got := operatorBranch(title, "t1"); got != want {
			t.Errorf("operatorBranch(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestOperatorBranch_OwnerLabelFitsBrsLimit(t *testing.T) {
	for _, id := range []string{"commander-il9.19.2", "example-app-t02.5", "t1", "example-automation-t07.2.1"} {
		b := operatorBranch("spawn smoke test of the very long title", id)
		owner := "owner:operator-" + strings.ReplaceAll(b, "/", "-")
		if len(owner) > 50 && len(id) < 24 {
			t.Errorf("%s: owner label %q is %d chars", id, owner, len(owner))
		}
	}
}

// The role is named from the task bead whatever --branch says; --branch is
// only the git branch.
func TestSpawnOperator_RoleNameComesFromTheBeadNotTheBranch(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.2.1"}
	useMockBeads(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "Fix the Import bug!", "--branch", "feat/whatever"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	req := sp.reqs[0]
	if req.Branch != "feat/whatever" || req.Name != "op-fix-the-import-bug-ms-1.2.1" {
		t.Fatalf("branch=%q name=%q", req.Branch, req.Name)
	}
	if !strings.Contains(out, `"agent":"operator-op-fix-the-import-bug-ms-1.2.1"`) {
		t.Fatalf("output: %s", out)
	}
}

func openGateMock() *beads.MockClient {
	return &beads.MockClient{
		CreateID:   "ms-1.2.1",
		DepsResult: map[string][]beads.Dep{"ms-1": {{IssueID: "ms-1", DependsOnID: "ms-1.9", Type: "blocks"}}},
		ShowResult: &beads.Bead{ID: "ms-1.9", Status: "● P2 · OPEN", Labels: []string{"kind:objective", beads.GateLabel}},
	}
}

func TestSpawnOperator_RefusesUnderAnUnapprovedWorkflowGate(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := openGateMock()
	useMockBeads(t, mock)
	stubMail(t)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	// The gate blocks the stage ms-1; the task goes under its step ms-1.2.
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr); code == 0 {
		t.Fatal("expected a refusal under an open gate")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "workflow not approved: ms-1.9") {
		t.Fatalf("stderr = %s", errOut)
	}
	if len(sp.reqs) != 0 || strings.Contains(strings.Join(mock.Calls, "\n"), "Create:") {
		t.Fatalf("nothing may be created: %v %v", mock.Calls, sp.reqs)
	}
}

func TestSpawnOperator_ApprovedGateOrWritesWorkflowSpawns(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		args   []string
	}{
		"approved gate":   {status: "● P2 · CLOSED"},
		"writes workflow": {status: "● P2 · OPEN", args: []string{"--writes-workflow"}},
	} {
		t.Run(name, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			mock := openGateMock()
			mock.ShowResult.Status = tc.status
			useMockBeads(t, mock)

			stdout, stderr, readOutput := withTempOutputFiles(t)
			args := append([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, tc.args...)
			if code := runInternalSpawnOperator(args, stdout, stderr); code != 0 {
				_, errOut := readOutput()
				t.Fatalf("exit=%d stderr=%s", code, errOut)
			}
			if len(sp.reqs) != 1 {
				t.Fatalf("spawn calls = %d", len(sp.reqs))
			}
		})
	}
}

// gateTree is the live shape: example-app-t02.8 was unhooked from example-app-t02
// (no parent edge) and is its own workstream. gate-a blocks example-app-t02,
// gate-b blocks example-app-t02.8; both are open.
func gateTree(blocked ...string) *beads.MemClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"example-app-t02", "example-app-t02.2", "example-app-t02.2.1", "example-app-t02.8", "example-app-t02.8.4", "example-app-t02.8.4.5"} {
		m.Seed(beads.Bead{ID: id, Labels: []string{"kind:objective"}})
	}
	m.Seed(beads.Bead{ID: "gate-a", Labels: []string{"kind:objective", beads.GateLabel}})
	m.Seed(beads.Bead{ID: "gate-b", Labels: []string{"kind:objective", beads.GateLabel}})
	m.Edges = []beads.Dep{
		{IssueID: "example-app-t02.2", DependsOnID: "example-app-t02", Type: "parent-child"},
		{IssueID: "example-app-t02.2.1", DependsOnID: "example-app-t02.2", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4", DependsOnID: "example-app-t02.8", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4.5", DependsOnID: "example-app-t02.8.4", Type: "parent-child"},
	}
	gateOf := map[string]string{"example-app-t02": "gate-a", "example-app-t02.8": "gate-b"}
	for _, id := range blocked {
		m.Edges = append(m.Edges, beads.Dep{IssueID: id, DependsOnID: gateOf[id], Type: "blocks"})
	}
	return m
}

func TestOpenGateAbove_WalksRealAncestors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, step, want string
		blocked          []string
	}{
		{"old workstream's gate does not hold a promoted one", "example-app-t02.8.4.5", "", []string{"example-app-t02"}},
		{"promoted workstream's own gate holds its steps", "example-app-t02.8.4.5", "gate-b", []string{"example-app-t02", "example-app-t02.8"}},
		{"ordinary nested step is held by its workstream's gate", "example-app-t02.2.1", "gate-a", []string{"example-app-t02"}},
		{"ordinary nested step ignores a promoted sibling's gate", "example-app-t02.2.1", "", []string{"example-app-t02.8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := openGateAbove(ctx, gateTree(tc.blocked...), "/p", tc.step)
			if err != nil || got != tc.want {
				t.Fatalf("openGateAbove(%s) = %q, %v; want %q", tc.step, got, err, tc.want)
			}
		})
	}
}

func TestOpenGateAbove_MovedUnderAnotherPrefix(t *testing.T) {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "x-1"})
	m.Seed(beads.Bead{ID: "x-1.3"})
	m.Seed(beads.Bead{ID: "y-2"})
	m.Seed(beads.Bead{ID: "gate-x", Labels: []string{beads.GateLabel}})
	m.Seed(beads.Bead{ID: "gate-y", Labels: []string{beads.GateLabel}})
	m.Edges = []beads.Dep{
		{IssueID: "x-1.3", DependsOnID: "y-2", Type: "parent-child"},
		{IssueID: "x-1", DependsOnID: "gate-x", Type: "blocks"},
		{IssueID: "y-2", DependsOnID: "gate-y", Type: "blocks"},
	}
	got, err := openGateAbove(context.Background(), m, "/p", "x-1.3")
	if err != nil || got != "gate-y" {
		t.Fatalf("openGateAbove = %q, %v; want gate-y", got, err)
	}
}

func prodGateMock(status string, labels []string, desc string) *beads.MockClient {
	return &beads.MockClient{
		CreateID:   "ms-1.2.1",
		ShowResult: &beads.Bead{ID: "ms-1.7", Status: status, Labels: labels, Description: desc},
	}
}

func TestSpawnOperator_ProdGate(t *testing.T) {
	good := []string{gates.KindLabel, "approver:user", "gate-of:" + beads.EscapeLabel("ms-1.2")}
	for name, tc := range map[string]struct {
		caller  string
		status  string
		labels  []string
		desc    string
		wantErr string
		title   string
	}{
		"ok":                {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: good, desc: "run migration 42"},
		"plain gate label":  {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: []string{beads.GateLabel, "approver:user", "gate-of:" + beads.EscapeLabel("ms-1.2")}, desc: "run migration 42"},
		"legacy approver":   {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: []string{gates.KindLabel, "approver:sample-owner", "gate-of:" + beads.EscapeLabel("ms-1")}, desc: "run migration 42"},
		"open gate":         {caller: "manager-ms-1", status: "● P2 · OPEN", labels: good, desc: "x", wantErr: "not closed"},
		"not a gate":        {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: []string{"kind:task"}, desc: "x", wantErr: "not a gate bead"},
		"wrong approver":    {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: []string{gates.KindLabel, "approver:someone", "gate-of:" + beads.EscapeLabel("ms-1.2")}, desc: "x", wantErr: "not approved by the user"},
		"not a manager":     {caller: "operator-x", status: "● P2 · CLOSED", labels: good, desc: "x", wantErr: "only a Manager"},
		"no caller":         {caller: "", status: "● P2 · CLOSED", labels: good, desc: "x", wantErr: "only a Manager"},
		"not linked":        {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: []string{gates.KindLabel, "approver:user", "gate-of:other"}, desc: "x", wantErr: "not linked"},
		"no scope written":  {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: good, desc: " ", wantErr: "no description"},
		"workflow approval": {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: good, desc: "Approved by the user: the plan", title: "Dev flow: workflow approved", wantErr: "workflow approval"},
		"spec approval":     {caller: "manager-ms-1", status: "● P2 · CLOSED", labels: good, desc: "the spec", title: "Spec approved", wantErr: "workflow approval"},
	} {
		t.Run(name, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			mock := prodGateMock(tc.status, tc.labels, tc.desc)
			mock.ShowResult.Title = tc.title
			useMockBeads(t, mock)
			stubMail(t)
			t.Setenv("COMMANDER_ROLE", tc.caller)

			stdout, stderr, readOutput := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--prod-gate", "ms-1.7"}, stdout, stderr)
			_, errOut := readOutput()
			if tc.wantErr != "" {
				if code == 0 || !strings.Contains(errOut, tc.wantErr) {
					t.Fatalf("exit=%d stderr=%s, want %q", code, errOut, tc.wantErr)
				}
				if len(sp.reqs) != 0 || strings.Contains(strings.Join(mock.Calls, "\n"), "Create:") {
					t.Fatalf("nothing may be created: %v", mock.Calls)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, errOut)
			}
			if got := sp.reqs[0]; got.ProdGate != "ms-1.7" || got.ProdGateScope != tc.desc {
				t.Fatalf("req = %+v", got)
			}
			if !strings.Contains(strings.Join(mock.Calls, "\n"), "prod-gate:"+beads.EscapeLabel("ms-1.7")) {
				t.Fatalf("label not set: %v", mock.Calls)
			}
		})
	}
}

// failFirst fails the first n spawns with err, then starts the Operator.
type failFirst struct {
	fakeOperatorSpawner
	n   int
	err error
}

func (f *failFirst) Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	if len(f.reqs) < f.n {
		f.reqs = append(f.reqs, req)
		return nil, f.err
	}
	return f.fakeOperatorSpawner.Spawn(ctx, req)
}

// A spawn that failed on busy beads is tried once more after a pause; only a
// second failure (or any other failure) is reported and mailed.
func TestSpawnOperator_BusyFailureRetriesOnce(t *testing.T) {
	orig := spawnRetryAfter
	spawnRetryAfter = time.Millisecond
	t.Cleanup(func() { spawnRetryAfter = orig })
	for _, tc := range []struct {
		name   string
		fails  int
		err    error
		code   int
		spawns int
		mails  int
	}{
		{"busy once, then spawned", 1, errors.New("br show: signal: killed"), 0, 2, 0},
		{"busy twice, reported", 2, errors.New("could not read the workflow gate of ms-1 (busy), retry in a minute"), 1, 2, 1},
		{"not a passing failure, reported at once", 2, errors.New("docker down"), 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := &failFirst{n: tc.fails, err: tc.err}
			stubSpawnDepsWith(t, sp)
			useMockBeads(t, &beads.MockClient{CreateID: "ms-1.1"})
			mailer := stubMail(t)
			stdout, stderr, readOutput := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1", "--title", "t"}, stdout, stderr)
			_, errOut := readOutput()
			if code != tc.code || len(sp.reqs) != tc.spawns || len(mailer.sent) != tc.mails {
				t.Fatalf("exit %d spawns %d mails %d, want %d %d %d; stderr:\n%s", code, len(sp.reqs), len(mailer.sent), tc.code, tc.spawns, tc.mails, errOut)
			}
		})
	}
}

// stubSpawnDepsWith serves sp with no workstream cap.
func stubSpawnDepsWith(t *testing.T, sp interface {
	Spawn(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error)
}) {
	t.Helper()
	orig := spawnOperatorDeps
	dir := t.TempDir()
	spawnOperatorDeps = func(string) (*operatorSpawnDeps, error) {
		return &operatorSpawnDeps{Spawner: sp, Project: &store.Project{ID: 7, Name: "manifest", Type: "code", Path: dir}, DefaultModel: "anthropic/test"}, nil
	}
	t.Cleanup(func() { spawnOperatorDeps = orig })
}

func TestSpawnOperator_ParentRefusedUnderNested(t *testing.T) {
	owners := []string{beads.NestedA1, beads.NestedA, beads.NestedRoot}
	for _, tc := range []struct {
		role     string
		asParent bool
		record   bool
		refused  bool
	}{
		{"manager-" + beads.NestedA1, false, false, false},  // the owner runs its Operators
		{"manager-" + beads.NestedRoot, false, false, true}, // a parent dispatches by mail
		{"manager-" + beads.NestedA, false, false, true},
		{"manager-" + beads.NestedB, false, false, true}, // not in the chain: mails the owner
		{"manager-" + beads.NestedB, true, false, true},  // --as-parent is no way round it
	} {
		record, err := parentDispatch(tc.role, owners, beads.NestedStep, tc.asParent)
		if (err != nil) != tc.refused || record != tc.record {
			t.Errorf("%s: record=%v err=%v", tc.role, record, err)
		}
		if err != nil && !strings.Contains(err.Error(), "cmdr internal-mail --to manager-"+beads.NestedA1) {
			t.Errorf("refusal does not name the nested Manager: %v", err)
		}
	}
	if record, err := parentDispatch("manager-"+beads.NestedRoot, []string{beads.NestedRoot}, beads.NestedRoot, false); err != nil || record {
		t.Fatalf("a root's own Manager: %v %v", record, err)
	}
	// After --undo (owners = the root alone) a nested Manager is refused, in
	// either spelling of its role.
	for _, role := range []string{"manager-demo-r.1", "manager-demo-r-1"} {
		if _, err := parentDispatch(role, []string{"demo-r"}, "demo-r.1.1", true); err == nil || !strings.Contains(err.Error(), "cmdr internal-mail --to manager-demo-r") {
			t.Fatalf("%s after undo: %v", role, err)
		}
	}
	if _, err := parentDispatch("manager-demo-r-1", []string{"demo-r.1", "demo-r"}, "demo-r.1.1", false); err != nil {
		t.Fatalf("the owner in its dashed spelling: %v", err)
	}
}

func TestSpawnOperator_AsParentRecorded(t *testing.T) {
	record, err := parentDispatch("manager-"+beads.NestedRoot, []string{beads.NestedA1, beads.NestedA, beads.NestedRoot}, beads.NestedStep, true)
	if err != nil || !record {
		t.Fatalf("--as-parent: record=%v err=%v", record, err)
	}
}

// A named runtime or account with no usage left is refused before the task bead
// is made; a run the spawner moved says so in the result JSON.
func TestSpawnOperator_UsageRefusalAndMove(t *testing.T) {
	home := accountHome(t, []string{"work"})
	now := time.Now()
	chat.OpenUsage(home).MarkOut(chat.RuntimeCodex, "", "manager-x", now.Add(time.Hour).Unix(), now)
	sp := &fakeOperatorSpawner{moved: "was headed for Codex; Codex is out of usage, so it runs on Claude account default instead."}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.2.1"}
	useMockBeads(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t", "--runtime", "codex"}, stdout, stderr); code == 0 {
		t.Fatal("spawned on Codex while it is out")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "Codex is out of usage until") || !strings.Contains(errOut, "--runtime claude") {
		t.Fatalf("stderr = %s", errOut)
	}
	if len(mock.Calls) != 0 || len(sp.reqs) != 0 {
		t.Fatalf("a refused spawn made something: %v %v", mock.Calls, sp.reqs)
	}

	stdout, stderr, readOutput = withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr); code != 0 {
		_, e := readOutput()
		t.Fatalf("exit %d: %s", code, e)
	}
	out, _ := readOutput()
	for _, want := range []string{`"runtime":"claude"`, `"account":"default"`, `"moved":"was headed for Codex`} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %s: %s", want, out)
		}
	}
}

// With nothing named and every Claude account out (Codex too), the spawn is
// refused before the task bead is made: nothing to create and remove again.
func TestSpawnOperator_AllOutRefusedBeforeTheTask(t *testing.T) {
	home := accountHome(t, []string{"work"})
	now := time.Now()
	u := chat.OpenUsage(home)
	u.MarkOut(chat.RuntimeCodex, "", "manager-x", now.Add(time.Hour).Unix(), now)
	u.MarkOut(chat.RuntimeClaude, "work", "manager-x", now.Add(time.Hour).Unix(), now)
	u.MarkOut(chat.RuntimeClaude, "default", "manager-x", now.Add(time.Hour).Unix(), now)
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	mock := &beads.MockClient{CreateID: "ms-1.2.1"}
	useMockBeads(t, mock)
	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "ms-1.2", "--title", "t"}, stdout, stderr); code == 0 {
		t.Fatal("spawned with no account that has usage")
	}
	_, errOut := readOutput()
	if !strings.Contains(errOut, "no other logged-in Claude account has usage left") || !strings.Contains(errOut, "no task was created") {
		t.Fatalf("stderr = %s", errOut)
	}
	for _, c := range mock.Calls {
		if strings.HasPrefix(c, "Create") || strings.HasPrefix(c, "Delete") {
			t.Fatalf("a refused spawn made a task: %v", mock.Calls)
		}
	}
	if len(sp.reqs) != 0 {
		t.Fatalf("spawned: %v", sp.reqs)
	}
}

func TestSpawnOperator_MissingStepNeverMailsOrCreates(t *testing.T) {
	sp := &fakeOperatorSpawner{}
	stubSpawnDeps(t, sp, 0, 3)
	m := &beads.MockClient{CreateID: "unexpected"}
	useMockBeadsRaw(t, m)
	original := mailDeps
	mails := 0
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		mails++
		return nil, nil, "", nil, errors.New("unexpected mail")
	}
	t.Cleanup(func() { mailDeps = original })
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "null", "--title", "Build", "--writes-workflow"}, stdout, stderr); code == 0 {
		t.Fatal("missing step accepted")
	}
	if mails != 0 || len(sp.reqs) != 0 {
		t.Fatalf("mail=%d spawn=%v", mails, sp.reqs)
	}
	for _, call := range m.Calls {
		if strings.HasPrefix(call, "Create:") {
			t.Fatalf("created task: %v", m.Calls)
		}
	}
}

func TestSpawnOperator_BusyStepReadRetriesAndMails(t *testing.T) {
	old := spawnRetryAfter
	spawnRetryAfter = time.Millisecond
	t.Cleanup(func() { spawnRetryAfter = old })
	stubSpawnDeps(t, &fakeOperatorSpawner{}, 0, 3)
	useMockBeadsRaw(t, &beads.MockClient{ShowErr: errors.New("br is busy (3 waiting); retry in a minute")})
	original := mailDeps
	mails := 0
	mailDeps = func() (amAgentStore, amMailer, string, func(), error) {
		mails++
		return nil, nil, "", nil, errors.New("no mail in test")
	}
	t.Cleanup(func() { mailDeps = original })
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", "p-1", "--title", "Build", "--writes-workflow"}, stdout, stderr); code == 0 {
		t.Fatal("busy read accepted")
	}
	if mails != 1 {
		t.Fatalf("failure mail attempts = %d, want 1", mails)
	}
}
