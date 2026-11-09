package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func addTab(t *testing.T, s *Store, pid int64, ws, target string) int64 {
	t.Helper()
	id, _, err := s.AddTab(context.Background(), InspectorTab{ProjectID: pid, WorkstreamBeadID: ws, Title: target, Kind: "url", Target: target})
	if err != nil {
		t.Fatalf("AddTab: %v", err)
	}
	return id
}

func tabTargets(t *testing.T, s *Store, pid int64, ws string) []string {
	t.Helper()
	tabs, err := s.ListTabs(context.Background(), pid, ws)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tab := range tabs {
		out = append(out, tab.Target)
	}
	return out
}

func TestAddTab_IdempotentAndScoped(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a := addTab(t, s, 1, "ws-1", "https://a")
	id, created, err := s.AddTab(ctx, InspectorTab{ProjectID: 1, WorkstreamBeadID: "ws-1", Title: "dup", Target: "https://a"})
	if err != nil || created || id != a {
		t.Fatalf("dup add: id=%d created=%v err=%v, want id=%d created=false", id, created, err, a)
	}
	addTab(t, s, 1, "ws-1", "https://b")
	addTab(t, s, 0, "", "https://a") // same target in another window is fine
	if got := tabTargets(t, s, 1, "ws-1"); !reflect.DeepEqual(got, []string{"https://a", "https://b"}) {
		t.Errorf("ws-1 tabs = %v", got)
	}
	if got := tabTargets(t, s, 0, ""); !reflect.DeepEqual(got, []string{"https://a"}) {
		t.Errorf("commander tabs = %v", got)
	}
	tab, _ := s.GetTab(ctx, a)
	if tab == nil || tab.RefreshSecs != 60 || tab.AddedBy != "user" || tab.Kind != "url" {
		t.Errorf("defaults not applied: %+v", tab)
	}
	if _, _, err := s.AddTab(ctx, InspectorTab{Target: "x"}); err == nil {
		t.Error("AddTab without title should fail")
	}
}

func TestMoveAndRemoveTab(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a := addTab(t, s, 1, "w", "a")
	b := addTab(t, s, 1, "w", "b")
	c := addTab(t, s, 1, "w", "c")
	if err := s.MoveTab(ctx, c, 1); err != nil {
		t.Fatal(err)
	}
	if got := tabTargets(t, s, 1, "w"); !reflect.DeepEqual(got, []string{"c", "a", "b"}) {
		t.Errorf("after move to 1: %v", got)
	}
	if err := s.MoveTab(ctx, c, 99); err != nil {
		t.Fatal(err)
	}
	if got := tabTargets(t, s, 1, "w"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("after move to end: %v", got)
	}
	if err := s.RemoveTab(ctx, b); err != nil {
		t.Fatal(err)
	}
	tabs, _ := s.ListTabs(ctx, 1, "w")
	if len(tabs) != 2 || tabs[0].ID != a || tabs[0].Position != 1 || tabs[1].ID != c || tabs[1].Position != 2 {
		t.Errorf("after remove: %+v", tabs)
	}
	if err := s.RemoveTab(ctx, 9999); err != nil {
		t.Errorf("removing a missing tab should be a no-op: %v", err)
	}
}

func TestActiveTabAndCache(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a := addTab(t, s, 1, "w", "a")
	b := addTab(t, s, 1, "w", "b")
	if id, _ := s.GetActiveTab(ctx, 1, "w"); id != 0 {
		t.Errorf("no active tab yet, got %d", id)
	}
	if err := s.SetActiveTab(ctx, 1, "w", a); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := s.SaveTabCache(ctx, 1, "w", `{"Title":"x"}`, now); err != nil {
		t.Fatal(err)
	}
	js, at, _ := s.GetTabCache(ctx, 1, "w")
	if js != `{"Title":"x"}` || !at.Equal(now) {
		t.Errorf("cache = %q %v", js, at)
	}
	// Re-selecting the same tab keeps the cache; switching drops it.
	_ = s.SetActiveTab(ctx, 1, "w", a)
	if js, _, _ := s.GetTabCache(ctx, 1, "w"); js == "" {
		t.Error("cache dropped on same-tab activation")
	}
	_ = s.SetActiveTab(ctx, 1, "w", b)
	if js, _, _ := s.GetTabCache(ctx, 1, "w"); js != "" {
		t.Error("cache kept after switching tab")
	}
	if id, _ := s.GetActiveTab(ctx, 1, "w"); id != b {
		t.Errorf("active = %d, want %d", id, b)
	}
	// Removing the active tab clears the selection.
	if err := s.RemoveTab(ctx, b); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.GetActiveTab(ctx, 1, "w"); id != 0 {
		t.Errorf("active after remove = %d, want 0", id)
	}
}
