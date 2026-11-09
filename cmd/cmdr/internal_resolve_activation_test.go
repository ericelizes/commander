package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestRunInternalResolveOpenProject_AlreadyActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	projectPath := filepath.Join(workspaceRoot, "manifest")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if _, err := st.InsertProject(context.Background(), store.Project{
		Name:        "manifest",
		Type:        "code",
		Path:        projectPath,
		ActivatedAt: 1,
		Status:      "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "stdout")
	errPath := filepath.Join(t.TempDir(), "stderr")
	stdout, _ := os.Create(outPath)
	stderr, _ := os.Create(errPath)
	code := runInternalResolveOpenProject([]string{"--name", "manifest"}, stdout, stderr)
	_ = stdout.Close()
	_ = stderr.Close()
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var got internalResolveActivationResult
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.AlreadyActive || got.ActivePath != projectPath || got.Outcome != workspaceActivationSingle {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestRunInternalResolveOpenProject_PathlessWorkspaceSingle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)

	projectPath := filepath.Join(workspaceRoot, "manifest")
	if err := os.MkdirAll(filepath.Join(projectPath, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "stdout")
	errPath := filepath.Join(t.TempDir(), "stderr")
	stdout, _ := os.Create(outPath)
	stderr, _ := os.Create(errPath)
	code := runInternalResolveOpenProject([]string{"--name", "manifest"}, stdout, stderr)
	_ = stdout.Close()
	_ = stderr.Close()
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var got internalResolveActivationResult
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AlreadyActive {
		t.Fatalf("already_active = true, want false")
	}
	if got.Outcome != workspaceActivationSingle || len(got.Matches) != 1 || got.Matches[0].Path != projectPath || got.Matches[0].Type != "code" {
		t.Fatalf("unexpected result: %+v", got)
	}
}
