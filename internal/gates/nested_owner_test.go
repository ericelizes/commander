package gates

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestPublicationRepair_NestedOwnerMayRepair(t *testing.T) {
	tree := beads.NestedTree()
	dir := t.TempDir()
	upward := []string{beads.NestedA1, beads.NestedA, beads.NestedRoot}
	if got := nestedOwnerOf(context.Background(), tree, dir, upward, "manager-"+beads.NestedA1); got != "" {
		t.Fatal("switch off: a nested Manager repaired")
	}
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	for actor, want := range map[string]string{
		"manager-" + beads.NestedA1: beads.NestedA1, "manager-" + beads.NestedA: beads.NestedA,
		"manager-" + beads.NestedB: "", "operator-x": "",
	} {
		if got := nestedOwnerOf(context.Background(), tree, dir, upward, actor); got != want {
			t.Errorf("%s: %q, want %q", actor, got, want)
		}
	}
}

// A nested Manager repairs only waiters inside its own subtree, never a wait on
// an ancestor above its workstream.
func TestPublicationRepair_NestedOwnerStaysInItsSubtree(t *testing.T) {
	ctx := context.Background()
	ws := []string{"kind:objective", beads.WorkstreamLabel}
	dir := t.TempDir()
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	graph := func(waits ...string) *beads.MemClient {
		m := publicationGraph()
		m.Seed(beads.Bead{ID: "outer", Labels: ws})
		m.Seed(beads.Bead{ID: "inner", Labels: ws})
		m.SetParent(ctx, dir, "outer", "sample")
		m.SetParent(ctx, dir, "inner", "outer")
		m.SetParent(ctx, dir, "stage", "inner")
		for _, w := range waits {
			m.DependOn(ctx, dir, w, "card")
		}
		return m
	}
	both := []string{"inner", "outer"}
	for actor, refused := range map[string]bool{"manager-inner": true, "manager-outer": false, "manager-sample": false} {
		err := RepairPublication(ctx, graph("outer", "inner", "stage"), dir, "card", "inner", both, "Redundant ancestor wait", actor, true)
		if got := err != nil && strings.Contains(err.Error(), "above"); got != refused || (!refused && err != nil) {
			t.Errorf("%s: err %v, want refused=%v", actor, err, refused)
		}
	}
	// Its own workstream bead is in its subtree.
	if err := RepairPublication(ctx, graph("inner", "stage"), dir, "card", "inner", []string{"inner"}, "Redundant ancestor wait", "manager-inner", true); err != nil {
		t.Fatalf("its own workstream bead: %v", err)
	}
}
