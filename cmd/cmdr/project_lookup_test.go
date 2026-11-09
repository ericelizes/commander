package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func openStateStore(t *testing.T) *store.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedProjectWithPath(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}
	projectID, err := st.InsertProject(ctx, store.Project{
		Name:        name,
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert project %s: %v", name, err)
	}
	return projectID
}

func insertRunRow(t *testing.T, st *store.Store, projectID int64, taskID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{
		ProjectID:         projectID,
		TaskBeadID:        taskID,
		AgentName:         "op",
		OpencodeSessionID: "ses_" + taskID,
		ContainerID:       sql.NullString{String: "c_" + taskID, Valid: true},
		SpawnTime:         1,
		Status:            status,
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
}

func TestResolveTaskOpenTarget_RunningTask(t *testing.T) {
	st := openStateStore(t)
	projectID := seedProjectWithPath(t, st, "frontend")
	insertRunRow(t, st, projectID, "fro-001", "running")

	target, err := resolveTaskOpenTarget("fro-001")
	if err != nil {
		t.Fatalf("resolve task: %v", err)
	}
	if target.projectName != "frontend" || target.taskBeadID != "fro-001" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveTaskOpenTarget_FallbackObjectiveLink(t *testing.T) {
	st := openStateStore(t)
	ctx := context.Background()
	projectID := seedProjectWithPath(t, st, "frontend")
	objectiveID, err := st.InsertObjective(ctx, store.Objective{Title: "obj", Status: "open", CreatedAt: 1})
	if err != nil {
		t.Fatalf("insert objective: %v", err)
	}
	if _, err := st.InsertObjectiveTask(ctx, store.ObjectiveTask{
		ObjectiveID: objectiveID,
		ProjectID:   projectID,
		TaskBeadID:  "fro-002",
		CreatedAt:   1,
	}); err != nil {
		t.Fatalf("insert objective task: %v", err)
	}

	target, err := resolveTaskOpenTarget("fro-002")
	if err != nil {
		t.Fatalf("resolve task: %v", err)
	}
	if target.projectName != "frontend" || target.taskBeadID != "fro-002" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveTaskOpenTarget_MissingTask(t *testing.T) {
	st := openStateStore(t)
	_ = seedProjectWithPath(t, st, "frontend")

	_, err := resolveTaskOpenTarget("fro-404")
	if err == nil {
		t.Fatal("expected missing task error")
	}
	if !strings.Contains(err.Error(), `task "fro-404" was not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveTaskOpenTarget_AmbiguousAcrossProjects(t *testing.T) {
	st := openStateStore(t)
	ctx := context.Background()
	projectID1 := seedProjectWithPath(t, st, "alpha")
	projectID2 := seedProjectWithPath(t, st, "beta")
	objectiveID, err := st.InsertObjective(ctx, store.Objective{Title: "obj", Status: "open", CreatedAt: 1})
	if err != nil {
		t.Fatalf("insert objective: %v", err)
	}
	for _, row := range []store.ObjectiveTask{
		{ObjectiveID: objectiveID, ProjectID: projectID1, TaskBeadID: "shared-001", CreatedAt: 1},
		{ObjectiveID: objectiveID, ProjectID: projectID2, TaskBeadID: "shared-001", CreatedAt: 2},
	} {
		if _, err := st.InsertObjectiveTask(ctx, row); err != nil {
			t.Fatalf("insert objective task: %v", err)
		}
	}

	_, err = resolveTaskOpenTarget("shared-001")
	if err == nil {
		t.Fatal("expected ambiguous task error")
	}
	if !strings.Contains(err.Error(), "alpha, beta") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveWorkspaceActivationAtRoot_SingleMatch(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "commander")
	if err := os.MkdirAll(filepath.Join(projectPath, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}

	got, err := resolveWorkspaceActivationAtRoot(root, "the commander project")
	if err != nil {
		t.Fatalf("resolve workspace activation: %v", err)
	}
	if got.Outcome != workspaceActivationSingle {
		t.Fatalf("outcome=%q want %q", got.Outcome, workspaceActivationSingle)
	}
	wantMatches := []workspaceActivationMatch{{
		Name: "commander",
		Path: projectPath,
		Type: "code",
	}}
	if !reflect.DeepEqual(got.Matches, wantMatches) {
		t.Fatalf("matches=%+v want %+v", got.Matches, wantMatches)
	}
}

func TestResolveWorkspaceActivationAtRoot_NoMatch(t *testing.T) {
	root := t.TempDir()
	got, err := resolveWorkspaceActivationAtRoot(root, "missing")
	if err != nil {
		t.Fatalf("resolve workspace activation: %v", err)
	}
	if got.Outcome != workspaceActivationNone {
		t.Fatalf("outcome=%q want %q", got.Outcome, workspaceActivationNone)
	}
	if len(got.Matches) != 0 {
		t.Fatalf("matches=%+v want none", got.Matches)
	}
}

func TestResolveWorkspaceActivationAtRoot_Ambiguous(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		filepath.Join("apps", "commander"),
		filepath.Join("scratch", "commander"),
	} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}

	got, err := resolveWorkspaceActivationAtRoot(root, "commander")
	if err != nil {
		t.Fatalf("resolve workspace activation: %v", err)
	}
	if got.Outcome != workspaceActivationAmbiguous {
		t.Fatalf("outcome=%q want %q", got.Outcome, workspaceActivationAmbiguous)
	}
	if len(got.Matches) != 2 {
		t.Fatalf("matches=%+v want 2", got.Matches)
	}
}
