package beads

import (
	"context"
	"strings"
	"testing"
)

func placementTree(t *testing.T) (*MemClient, func(title, parent string, labels ...string) string) {
	t.Helper()
	m := &MemClient{Prefix: "b"}
	return m, func(title, parent string, labels ...string) string {
		id, err := m.Create(context.Background(), "", title, CreateOptions{Parent: parent, Labels: append([]string{"kind:objective"}, labels...)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func TestCheckPlacement(t *testing.T) {
	ctx := context.Background()
	m, add := placementTree(t)
	leaf := add("leaf", "", WorkstreamLabel)
	step := add("a step", leaf)
	parent := add("parent", "", WorkstreamLabel)
	child := add("child", parent, WorkstreamLabel)
	legacy := add("old top-level, no label", "")
	oldTrack := add("old track", legacy, TrackLabel)

	for _, c := range []struct {
		name, parent string
		p            Placement
		want         string
	}{
		{"top level", "", Placement{Workstream: true}, ""},
		{"a workstream under a step", step, Placement{Workstream: true}, "is a step"},
		{"a workstream beside open steps", leaf, Placement{Workstream: true}, "--take-open-steps"},
		{"a workstream taking those steps", leaf, Placement{Workstream: true, TakingSteps: true}, ""},
		{"a step beside open workstreams", parent, Placement{}, "put this under one of them"},
		{"a gate beside an old track", legacy, Placement{}, "put this under one of them"},
		{"a second workstream", parent, Placement{Workstream: true}, ""},
		{"a workstream under an unlabelled top-level bead", legacy, Placement{Workstream: true}, ""},
		{"a step under a leaf", leaf, Placement{}, ""},
		{"a step under a step", step, Placement{}, ""},
		{"moving the only workstream back to a step", parent, Placement{Self: child}, ""},
	} {
		err := CheckPlacement(ctx, m, "", c.parent, c.p)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	_ = oldTrack
}

// Only open children count: finished steps never block a split.
func TestCheckPlacement_ClosedChildrenDoNotCount(t *testing.T) {
	ctx := context.Background()
	m, add := placementTree(t)
	w := add("w", "", WorkstreamLabel)
	done := add("done step", w)
	if err := m.Close(ctx, "", done); err != nil {
		t.Fatal(err)
	}
	if err := CheckPlacement(ctx, m, "", w, Placement{Workstream: true}); err != nil {
		t.Fatal(err)
	}
	task := add("an operator's task", w, "kind:task")
	_ = task
	if err := CheckPlacement(ctx, m, "", w, Placement{Workstream: true}); err != nil {
		t.Fatalf("a task bead is not a plan step: %v", err)
	}
}

// Parked children do not count until they come back.
func TestCheckPlacement_ParkedChildrenDoNotCount(t *testing.T) {
	ctx := context.Background()
	m, add := placementTree(t)
	w := add("w", "", WorkstreamLabel)
	parked := add("parked workstream", w, WorkstreamLabel)
	if err := m.SetStatus(ctx, "", parked, StatusDeferred); err != nil {
		t.Fatal(err)
	}
	if err := CheckPlacement(ctx, m, "", w, Placement{}); err != nil {
		t.Fatalf("a new step beside a parked workstream: %v", err)
	}
	add("an open step", w)
	if err := CheckPlacement(ctx, m, "", w, Placement{Workstream: true, Self: parked}); err == nil {
		t.Fatal("the parked workstream came back beside an open step")
	}
}

func TestIsNested(t *testing.T) {
	for _, c := range []struct {
		labels []string
		parent string
		want   bool
	}{
		{[]string{WorkstreamLabel}, "", false},
		{[]string{WorkstreamLabel}, "p", true},
		{[]string{TrackLabel}, "p", true},
		{[]string{TrackLabel}, "", true}, // a leftover: stays out of the sidebar
		{nil, "p", false},
	} {
		if got := IsNested(c.labels, c.parent); got != c.want {
			t.Errorf("IsNested(%v, %q) = %v", c.labels, c.parent, got)
		}
	}
}
