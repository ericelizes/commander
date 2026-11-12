package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestRunInternalCreateObjective(t *testing.T) {
	commanderHome := t.TempDir()
	t.Setenv("COMMANDER_HOME", commanderHome)

	projectPath := t.TempDir()
	st, err := store.Open(filepath.Join(commanderHome, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if _, err := st.InsertProject(context.Background(), store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	mockBeads := &beads.MockClient{CreateID: "obj-123"}
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return mockBeads }
	t.Cleanup(func() { newBeadsClientFn = orig })

	stdout, stderr, readOutput := withTempOutputFiles(t)
	exitCode := runInternalCreateObjective([]string{"--project", "manifest", "--title", "fix 2 bugs"}, stdout, stderr)
	out, errOut := readOutput()
	if exitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, errOut)
	}

	var payload internalCreateObjectiveResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("unmarshal stdout: %v\nout=%s", err, out)
	}
	if payload.Project != "manifest" || payload.BeadID != "obj-123" || payload.Title != "fix 2 bugs" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if len(mockBeads.Calls) != 1 {
		t.Fatalf("beads calls=%v", mockBeads.Calls)
	}
	if !strings.Contains(mockBeads.Calls[0], "Create:title=fix 2 bugs") || !strings.Contains(mockBeads.Calls[0], "labels=kind:objective") {
		t.Fatalf("unexpected beads call: %s", mockBeads.Calls[0])
	}
}

func TestRunInternalCreateObjective_MissingProjectFails(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	stdout, stderr, readOutput := withTempOutputFiles(t)
	exitCode := runInternalCreateObjective([]string{"--project", "manifest", "--title", "fix 2 bugs"}, stdout, stderr)
	_, errOut := readOutput()
	if exitCode == 0 {
		t.Fatal("expected failure for missing project")
	}
	if !strings.Contains(errOut, "state db") {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
}

func setupObjectiveProject(t *testing.T, mock beads.Client) {
	t.Helper()
	commanderHome := t.TempDir()
	t.Setenv("COMMANDER_HOME", commanderHome)
	st, err := store.Open(filepath.Join(commanderHome, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertProject(context.Background(), store.Project{
		Name: "manifest", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return mock }
	t.Cleanup(func() { newBeadsClientFn = orig })
}

func TestRunInternalCreateObjective_WithParent(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.1"}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "child", "--parent", "obj-1", "--waiting", "review:rev"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(createCall(mock), "parent=obj-1") {
		t.Fatalf("unexpected beads call: %s", createCall(mock))
	}
	var payload internalCreateObjectiveResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Parent != "obj-1" || payload.BeadID != "obj-1.1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRunInternalRenameObjective(t *testing.T) {
	mock := &beads.MockClient{}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalRenameObjective([]string{"--project", "manifest", "--bead", "obj-1", "--title", "better name"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	if len(mock.Calls) != 1 || !strings.HasPrefix(mock.Calls[0], "Rename:obj-1|title=better name") {
		t.Fatalf("unexpected beads calls: %v", mock.Calls)
	}
	if !strings.Contains(out, `"title":"better name"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRunInternalCloseObjective(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "obj-1", Status: "open"}}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "obj-1"}, stdout, stderr)
	out, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	// The parent-completion guard first reads the bead and actual child edges.
	// The workflow door then reads the bead and its own edges (a plugged review block), the stage's
	// gate beads are looked up, the holder check reads the closure fresh (batched List by id,
	// Show, Details, Dependents), then the close, then a read for push labels to clear; the close
	// is the only write
	n := len(mock.Calls)
	if n < 7 || !strings.HasPrefix(mock.Calls[0], "Show:") || !strings.HasPrefix(mock.Calls[1], "Dependents:obj-1") || !strings.HasPrefix(mock.Calls[2], "Show:") || !strings.HasPrefix(mock.Calls[3], "Deps:obj-1") || !strings.HasPrefix(mock.Calls[4], "List:") || !strings.HasPrefix(mock.Calls[n-2], "Close:obj-1") || !strings.HasPrefix(mock.Calls[n-1], "Show:obj-1") {
		t.Fatalf("unexpected beads calls: %v", mock.Calls)
	}
	for _, c := range mock.Calls[5 : n-2] {
		if !strings.HasPrefix(c, "Show:") && !strings.HasPrefix(c, "Details:") && !strings.HasPrefix(c, "Dependents:") && !strings.HasPrefix(c, "List:") {
			t.Fatalf("the holder check wrote: %v", mock.Calls)
		}
	}
	if !strings.Contains(out, `"closed":true`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRunInternalEditObjective_MissingFlagsFail(t *testing.T) {
	stdout, stderr, _ := withTempOutputFiles(t)
	if runInternalRenameObjective([]string{"--project", "manifest"}, stdout, stderr) == 0 {
		t.Fatal("rename: expected failure without --bead/--title")
	}
	if runInternalCloseObjective([]string{"--project", "manifest"}, stdout, stderr) == 0 {
		t.Fatal("close: expected failure without --bead")
	}
}

func TestInternalSetObjective_RejectsBadStatus(t *testing.T) {
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	if code := runInternalSetObjective([]string{"--project", "x", "--bead", "b", "--status", "closed"}, devnull, devnull); code != 1 {
		t.Fatalf("expected exit 1 for --status closed, got %d", code)
	}
}

// A gate is a bead labelled gate with its approvers, sitting between work:
// it waits on the work before it, and the work after it waits on it.
func TestRunInternalCreateObjective_Gate(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)

	stdout, stderr, readOutput := withTempOutputFiles(t)
	code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "PRs approved", "--parent", "obj-1",
		"--gate", "--approver", "reviewer-a,reviewer-b", "--after", "obj-1.2,obj-1.3", "--before", "obj-1.5", "--criteria", "Both reviewers approve the PRs"}, stdout, stderr)
	_, errOut := readOutput()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	calls := strings.Join(mock.Calls, "\n")
	for _, want := range []string{"labels=kind:objective,gate,approver:reviewer-a,approver:reviewer-b", "DependOn:obj-1.4->obj-1.2", "DependOn:obj-1.4->obj-1.3", "DependOn:obj-1.5->obj-1.4", "acceptance=Both reviewers approve the PRs"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
}

func TestRunInternalCreateObjective_GateNeedsAnApprover(t *testing.T) {
	mock := &beads.MockClient{CreateID: "obj-1.4"}
	setupObjectiveProject(t, mock)
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "g", "--gate"}, stdout, stderr); code == 0 {
		t.Fatal("a gate without an approver was accepted")
	}
	if len(mock.Calls) != 0 {
		t.Fatalf("nothing should be created: %v", mock.Calls)
	}
}

func TestLooksLikeApproval(t *testing.T) {
	for title, want := range map[string]bool{
		"Greenlight: run the reconcile": true,
		"  approve the spec":            true,
		"Workflow approved":             true,
		"Sign-off on release":           true,
		"Go/No-Go":                      true,
		"fix 2 bugs":                    false,
		"Add approval screen":           false,
	} {
		if got := looksLikeApproval(title); got != want {
			t.Errorf("looksLikeApproval(%q)=%v want %v", title, got, want)
		}
	}
}

func TestCreateObjectiveRefusesApprovalWithoutGate(t *testing.T) {
	commanderHome := t.TempDir()
	t.Setenv("COMMANDER_HOME", commanderHome)
	st, err := store.Open(filepath.Join(commanderHome, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "manifest", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mockBeads := &beads.MockClient{CreateID: "obj-1"}
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return mockBeads }
	t.Cleanup(func() { newBeadsClientFn = orig })

	stdout, stderr, readOutput := withTempOutputFiles(t)
	if code := runInternalCreateObjective([]string{"--project", "manifest", "--title", "Greenlight: run it"}, stdout, stderr); code == 0 {
		t.Fatal("expected refusal")
	}
	if _, errOut := readOutput(); !strings.Contains(errOut, "must be a gate bead") {
		t.Fatalf("stderr=%s", errOut)
	}
	if len(mockBeads.Calls) != 0 {
		t.Fatalf("created a bead: %v", mockBeads.Calls)
	}

	for _, args := range [][]string{
		{"--project", "manifest", "--title", "Greenlight: run it", "--gate", "--approver", "user", "--criteria", "The user says run it"},
		{"--project", "manifest", "--title", "Approve button", "--not-a-gate"},
	} {
		stdout, stderr, readOutput := withTempOutputFiles(t)
		if code := runInternalCreateObjective(args, stdout, stderr); code != 0 {
			_, errOut := readOutput()
			t.Fatalf("%v refused: %s", args, errOut)
		}
	}
}
