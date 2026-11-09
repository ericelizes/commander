package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Compile-time interface satisfaction is asserted in mock.go via
// `var _ Client = (*MockClient)(nil)`. This test exercises recording
// + scripted return values to prove the mock is usable.
func TestMockClient_RecordsAndReplays(t *testing.T) {
	want := []Bead{
		{ID: "bd-1", Title: "first", Status: "open"},
	}
	wantShow := &Bead{ID: "bd-1", Title: "first"}

	m := &MockClient{
		ListResult: want,
		CreateID:   "bd-99",
		ShowResult: wantShow,
	}

	ctx := context.Background()

	if err := m.SetState(ctx, "/p", "bd-1", "cmdr", "brainstorming", "spawn"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	got, err := m.List(ctx, "/p", ListOptions{Labels: []string{"kind:task"}, Limit: 5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "bd-1" {
		t.Errorf("List result mismatch: %+v", got)
	}

	id, err := m.Create(ctx, "/p", "title", CreateOptions{Parent: "bd-1", Silent: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "bd-99" {
		t.Errorf("Create id = %q, want bd-99", id)
	}

	if err := m.Link(ctx, "/p", "bd-1", "bd-2", "parent-child"); err != nil {
		t.Fatalf("Link: %v", err)
	}

	shown, err := m.Show(ctx, "/p", "bd-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown != wantShow {
		t.Errorf("Show pointer mismatch")
	}

	if len(m.Calls) != 5 {
		t.Fatalf("expected 5 recorded calls, got %d: %v", len(m.Calls), m.Calls)
	}

	// Spot-check call shape — these strings are the public contract for
	// downstream tests asserting commander spawn/kill/scheduler flow.
	checks := []struct{ prefix, contains string }{
		{"SetState:bd-1:cmdr=brainstorming", "reason=spawn"},
		{"List:", "labels=kind:task"},
		{"Create:title=title", "parent=bd-1"},
		{"Link:bd-1->bd-2", "type=parent-child"},
		{"Show:bd-1", "dir=/p"},
	}
	for i, c := range checks {
		if !strings.HasPrefix(m.Calls[i], c.prefix) {
			t.Errorf("call[%d] = %q, want prefix %q", i, m.Calls[i], c.prefix)
		}
		if c.contains != "" && !strings.Contains(m.Calls[i], c.contains) {
			t.Errorf("call[%d] = %q, want contains %q", i, m.Calls[i], c.contains)
		}
	}
}

func TestMockClient_PropagatesErrors(t *testing.T) {
	want := errors.New("boom")
	m := &MockClient{
		SetStateErr: want,
		ListErr:     want,
		CreateErr:   want,
		LinkErr:     want,
		ShowErr:     want,
	}
	ctx := context.Background()

	if err := m.SetState(ctx, "/p", "x", "d", "v", ""); !errors.Is(err, want) {
		t.Error("SetState did not propagate error")
	}
	if _, err := m.List(ctx, "/p", ListOptions{}); !errors.Is(err, want) {
		t.Error("List did not propagate error")
	}
	if _, err := m.Create(ctx, "/p", "t", CreateOptions{}); !errors.Is(err, want) {
		t.Error("Create did not propagate error")
	}
	if err := m.Link(ctx, "/p", "a", "b", ""); !errors.Is(err, want) {
		t.Error("Link did not propagate error")
	}
	if _, err := m.Show(ctx, "/p", "x"); !errors.Is(err, want) {
		t.Error("Show did not propagate error")
	}
}

func TestMockClient_NilListReturnsEmptySlice(t *testing.T) {
	m := &MockClient{}
	got, err := m.List(context.Background(), "/p", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("expected len 0, got %d", len(got))
	}
}
