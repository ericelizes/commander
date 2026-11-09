package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestSetProjectRepo(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.InsertProject(ctx, store.Project{Name: "example-app", Type: "knowledge", Path: "/p", Status: "active"}); err != nil {
		t.Fatal(err)
	}

	plain := t.TempDir()
	if err := setProjectRepo(ctx, st, "example-app", plain); err == nil || !strings.Contains(err.Error(), "not a git repo") {
		t.Errorf("non-repo should be refused: %v", err)
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setProjectRepo(ctx, st, "example-app", repo); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetProjectByName(ctx, "example-app"); p.RepoPath != repo {
		t.Errorf("repo_path %q", p.RepoPath)
	}
	if err := setProjectRepo(ctx, st, "example-app", "--clear"); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetProjectByName(ctx, "example-app"); p.RepoPath != "" {
		t.Errorf("not cleared: %q", p.RepoPath)
	}
}
