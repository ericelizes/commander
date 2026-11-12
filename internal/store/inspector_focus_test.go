package store

import (
	"context"
	"testing"
	"time"
)

func TestInspectorFocusCustody(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testInspectorFocusCustody(t, openMem(t)) })
	t.Run("postgres", func(t *testing.T) { testInspectorFocusCustody(t, postgresTestStore(t)) })
}

func testInspectorFocusCustody(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, Project{Name: "fixture", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	otherPID, err := s.InsertProject(ctx, Project{Name: "other", Type: "code", Path: t.TempDir(), Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	for _, w := range []struct {
		pid int64
		ws  string
	}{{pid, "fixture-1"}, {pid, "fixture-2"}, {otherPID, "fixture-1"}, {0, ""}} {
		a := addTab(t, s, w.pid, w.ws, "fixture-1.1")
		b := addTab(t, s, w.pid, w.ws, "fixture-1.2")
		// Cache-only state has a NULL selection, exercising the conflict path even
		// on the first focus. It must not keep an unrelated cached card.
		if err := s.SaveTabCache(ctx, w.pid, w.ws, "stale", now); err != nil {
			t.Fatal(err)
		}
		if err := s.SetActiveTab(ctx, w.pid, w.ws, a); err != nil {
			t.Fatal(err)
		}
		if js, _, err := s.GetTabCache(ctx, w.pid, w.ws); err != nil || js != "" {
			t.Fatalf("NULL selection cache: %q %v", js, err)
		}
		if err := s.SaveTabCache(ctx, w.pid, w.ws, "card", now); err != nil {
			t.Fatal(err)
		}
		if err := s.SetActiveTab(ctx, w.pid, w.ws, a); err != nil {
			t.Fatal(err)
		}
		if js, at, err := s.GetTabCache(ctx, w.pid, w.ws); err != nil || js != "card" || !at.Equal(now) {
			t.Fatalf("same focus cache: %q %v %v", js, at, err)
		}
		if err := s.SetActiveTab(ctx, w.pid, w.ws, b); err != nil {
			t.Fatal(err)
		}
		if js, _, err := s.GetTabCache(ctx, w.pid, w.ws); err != nil || js != "" {
			t.Fatalf("switched cache: %q %v", js, err)
		}
		if err := s.SaveTabCache(ctx, w.pid, w.ws, "retained", now); err != nil {
			t.Fatal(err)
		}
	}
	// Each window still points to its own decision, with its cache intact.
	for _, w := range []struct {
		pid int64
		ws  string
	}{{pid, "fixture-1"}, {pid, "fixture-2"}, {otherPID, "fixture-1"}, {0, ""}} {
		id, err := s.GetActiveTab(ctx, w.pid, w.ws)
		if err != nil {
			t.Fatal(err)
		}
		tab, err := s.GetTab(ctx, id)
		if err != nil || tab == nil || tab.ProjectID != w.pid || tab.WorkstreamBeadID != w.ws || tab.Target != "fixture-1.2" {
			t.Fatalf("selection custody: %+v %v", tab, err)
		}
		if js, at, err := s.GetTabCache(ctx, w.pid, w.ws); err != nil || js != "retained" || !at.Equal(now) {
			t.Fatalf("foreign focus affected cache: %q %v %v", js, at, err)
		}
	}
}
