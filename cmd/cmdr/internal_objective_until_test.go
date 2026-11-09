package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// --until keeps exactly one until:<finish line> label: a new one replaces the
// old, "none" clears it, and anything but one word is refused.
func TestApplyUntil(t *testing.T) {
	bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1.2", Labels: []string{"kind:objective", "until:approved"}}}
	if err := applyUntil(context.Background(), bc, "/p", "p-1.2", "merged"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Show:p-1.2|dir=/p",
		"SetLabel:p-1.2|label=until:approved|on=false|dir=/p",
		"SetLabel:p-1.2|label=until:merged|on=true|dir=/p",
	}
	if len(bc.Calls) != 3 || !reflect.DeepEqual(bc.Calls, want) {
		t.Fatalf("calls = %q", bc.Calls)
	}

	bc.Calls = nil
	if err := applyUntil(context.Background(), bc, "/p", "p-1.2", "none"); err != nil {
		t.Fatal(err)
	}
	if bc.Calls[len(bc.Calls)-1] != "SetLabel:p-1.2|label=until:approved|on=false|dir=/p" {
		t.Fatalf("none should only clear: %q", bc.Calls)
	}

	if err := applyUntil(context.Background(), bc, "/p", "p-1.2", "ready to merge"); err == nil {
		t.Fatal("a multi-word finish line was accepted")
	}
}

// A step goes in progress only with someone on it: an owner already on the
// bead, or one given in the same call.
func TestRequireOwner(t *testing.T) {
	ctx := context.Background()
	bc := &beads.MockClient{ListResult: []beads.Bead{
		{ID: "p-1.1", Labels: []string{"kind:objective", "owner:operator-x"}},
		{ID: "p-1.2", Labels: []string{"kind:objective"}},
	}}
	if err := requireOwner(ctx, bc, "/p", "p-1.1", ""); err != nil {
		t.Fatalf("owned step refused: %v", err)
	}
	if err := requireOwner(ctx, bc, "/p", "p-1.2", ""); err == nil {
		t.Fatal("an unowned step was allowed in progress")
	}
	if err := requireOwner(ctx, bc, "/p", "p-1.2", "operator-y"); err != nil {
		t.Fatalf("owner in the same call refused: %v", err)
	}
	if err := requireOwner(ctx, bc, "/p", "p-1.1", "none"); err == nil {
		t.Fatal("clearing the owner while starting the step was allowed")
	}
}

// --viz keeps exactly one viz:<n> label: a new one replaces the old, "none" clears.
func TestApplyViz(t *testing.T) {
	bc := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1.2", Labels: []string{"kind:task", "viz:3"}}}
	if err := applySoleLabel(context.Background(), bc, "/p", "p-1.2", beads.VizLabelPrefix, "1"); err != nil {
		t.Fatal(err)
	}
	got := bc.Calls[len(bc.Calls)-2:]
	want := []string{"SetLabel:p-1.2|label=viz:3|on=false|dir=/p", "SetLabel:p-1.2|label=viz:1|on=true|dir=/p"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q", bc.Calls)
	}
	bc.Calls = nil
	if err := applySoleLabel(context.Background(), bc, "/p", "p-1.2", beads.VizLabelPrefix, "none"); err != nil {
		t.Fatal(err)
	}
	if last := bc.Calls[len(bc.Calls)-1]; last != "SetLabel:p-1.2|label=viz:3|on=false|dir=/p" {
		t.Fatalf("none should only clear: %q", bc.Calls)
	}
}
