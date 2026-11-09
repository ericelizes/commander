package store

import (
	"context"
	"strings"
	"testing"
)

func TestProject_InsertAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	id, err := s.InsertProject(ctx, Project{
		Name:        "frontend",
		Type:        "code",
		Path:        "/tmp/example/code/frontend",
		ActivatedAt: 1700000000,
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive rowid, got %d", id)
	}

	got, err := s.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil project")
	}
	if got.Name != "frontend" || got.Type != "code" {
		t.Errorf("unexpected project: %+v", got)
	}
}

func TestProject_GetByName_NotFoundReturnsNil(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	p, err := s.GetProjectByName(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("expected nil error for missing project, got %v", err)
	}
	if p != nil {
		t.Errorf("expected nil project, got %+v", p)
	}
}

func TestProject_TypeCheckConstraint(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	_, err := s.InsertProject(ctx, Project{
		Name: "bogus", Type: "music", Path: "/tmp", ActivatedAt: 1, Status: "active",
	})
	if err == nil {
		t.Fatal("expected CHECK constraint violation for type='music'")
	}
}

func TestProject_UniqueName(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if _, err := s.InsertProject(ctx, Project{
		Name: "dupe", Type: "code", Path: "/a", ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := s.InsertProject(ctx, Project{
		Name: "dupe", Type: "code", Path: "/b", ActivatedAt: 2, Status: "active",
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("expected UNIQUE violation, got %v", err)
	}
}

func TestProject_List(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.InsertProject(ctx, Project{
			Name: n, Type: "code", Path: "/" + n, ActivatedAt: 1, Status: "active",
		}); err != nil {
			t.Fatalf("insert %s: %v", n, err)
		}
	}
	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d: %+v", len(list), list)
	}
}

func TestProject_RepoPath(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, err := s.InsertProject(ctx, Project{Name: "example-app", Type: "knowledge", Path: "/p", ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProject(ctx, id); got.RepoPath != "" {
		t.Errorf("default repo_path: %q", got.RepoPath)
	}
	if err := s.SetProjectRepoPath(ctx, "example-app", "/p/repo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProjectByName(ctx, "example-app"); got.RepoPath != "/p/repo" {
		t.Errorf("repo_path: %q", got.RepoPath)
	}
	if err := s.SetProjectRepoPath(ctx, "nope", "/x"); err == nil {
		t.Error("unknown project should error")
	}
}
