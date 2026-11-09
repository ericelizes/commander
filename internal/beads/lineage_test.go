package beads

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// showCounter counts Details calls (each is one `br show`).
type showCounter struct {
	*MemClient
	details int
	fail    bool
}

func (c *showCounter) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	c.details++
	if c.fail {
		return nil, errors.New("br down")
	}
	return c.MemClient.Details(ctx, dir, ids)
}

// promotedTree is the live shape: t02.8 was unhooked from t02 (no parent edge)
// and is a workstream of its own; its children still hang under it.
func promotedTree() *MemClient {
	m := &MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"example-app-t02", "example-app-t02.7", "example-app-t02.8", "example-app-t02.8.4", "example-app-t02.8.4.5", "example-app-t02.2", "example-app-t02.2.1"} {
		m.Seed(Bead{ID: id})
	}
	m.Edges = []Dep{
		{IssueID: "example-app-t02.8.4", DependsOnID: "example-app-t02.8", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4.5", DependsOnID: "example-app-t02.8.4", Type: "parent-child"},
		{IssueID: "example-app-t02.2", DependsOnID: "example-app-t02", Type: "parent-child"},
		{IssueID: "example-app-t02.2.1", DependsOnID: "example-app-t02.2", Type: "parent-child"},
	}
	return m
}

func TestLineage_PromotedWorkstreamOwnsItsSubtree(t *testing.T) {
	c := &showCounter{MemClient: promotedTree()}
	got := Lineage(context.Background(), c, "/p", nil, "example-app-t02.8.4.5")
	want := []string{"example-app-t02.8.4.5", "example-app-t02.8.4", "example-app-t02.8"}
	if !slices.Equal(got, want) {
		t.Fatalf("Lineage = %v, want %v", got, want)
	}
	if c.details != 1 {
		t.Errorf("Details calls = %d, want 1 (id and its dotted prefixes in one batch)", c.details)
	}
	if r := Root(context.Background(), c, "/p", nil, "example-app-t02.8"); r != "example-app-t02.8" {
		t.Errorf("Root(t02.8) = %q, want itself", r)
	}
}

func TestLineage_OrdinaryNestedBeadUnchanged(t *testing.T) {
	c := &showCounter{MemClient: promotedTree()}
	if r := Root(context.Background(), c, "/p", nil, "example-app-t02.2.1"); r != "example-app-t02" {
		t.Errorf("Root(t02.2.1) = %q, want example-app-t02", r)
	}
	if c.details != 1 {
		t.Errorf("Details calls = %d, want 1", c.details)
	}
}

func TestLineage_MovedUnderAnotherPrefix(t *testing.T) {
	m := &MemClient{EdgeParentsOnly: true}
	for _, id := range []string{"x-1", "x-1.3", "y-2"} {
		m.Seed(Bead{ID: id})
	}
	m.Edges = []Dep{{IssueID: "x-1.3", DependsOnID: "y-2", Type: "parent-child"}}
	c := &showCounter{MemClient: m}
	got := Lineage(context.Background(), c, "/p", nil, "x-1.3")
	if want := []string{"x-1.3", "y-2"}; !slices.Equal(got, want) {
		t.Fatalf("Lineage = %v, want %v", got, want)
	}
	if c.details != 2 {
		t.Errorf("Details calls = %d, want 2 (one more for the parent outside the dotted set)", c.details)
	}
}

func TestLineage_KnownParentsNeedNoRead(t *testing.T) {
	known := Parents{"example-app-t02.8.4.5": "example-app-t02.8.4", "example-app-t02.8.4": "example-app-t02.8", "example-app-t02.8": ""}
	if r := Root(context.Background(), nil, "", known, "example-app-t02.8.4.5"); r != "example-app-t02.8" {
		t.Errorf("Root = %q, want example-app-t02.8", r)
	}
	// A bead not loaded (a new task) walks its dotted id only until it reaches
	// a loaded one, whose real parent then decides.
	if r := Root(context.Background(), nil, "", known, "example-app-t02.8.4.5.9"); r != "example-app-t02.8" {
		t.Errorf("Root(unloaded child) = %q, want example-app-t02.8", r)
	}
}

func TestLineage_UnreadableFallsBackToDottedID(t *testing.T) {
	c := &showCounter{MemClient: promotedTree(), fail: true}
	if r := Root(context.Background(), c, "/p", nil, "example-app-t02.8.4.5"); r != "example-app-t02" {
		t.Errorf("Root with br down = %q, want the dotted root example-app-t02", r)
	}
	if c.details != 1 {
		t.Errorf("Details calls = %d, want 1 (a failed batch is not retried per bead)", c.details)
	}
}

func TestLineage_ParentCycleTerminates(t *testing.T) {
	m := &MemClient{EdgeParentsOnly: true}
	m.Seed(Bead{ID: "a-1"})
	m.Seed(Bead{ID: "b-1"})
	m.Edges = []Dep{{IssueID: "a-1", DependsOnID: "b-1", Type: "parent-child"}, {IssueID: "b-1", DependsOnID: "a-1", Type: "parent-child"}}
	if got := Lineage(context.Background(), m, "/p", nil, "a-1"); !slices.Equal(got, []string{"a-1", "b-1"}) {
		t.Errorf("Lineage = %v", got)
	}
}
