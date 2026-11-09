package main

import (
	"context"
	"slices"
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestMigrateWorkstreams(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{Prefix: "b"}
	add := func(title, parent string, labels ...string) string {
		id, err := m.Create(ctx, "", title, beads.CreateOptions{Parent: parent, Labels: append([]string{"kind:objective"}, labels...)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root := add("root", "")
	track := add("track", root, beads.TrackLabel)
	step := add("step beside it", root)
	orphan := add("leftover", "", beads.TrackLabel)
	gate := add("a parentless gate", "", beads.GateLabel)

	dry, err := migrateWorkstreams(ctx, m, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dry.Label, root) || !slices.Contains(dry.Label, track) || slices.Contains(dry.Label, step) || slices.Contains(dry.Label, orphan) || slices.Contains(dry.Label, gate) {
		t.Fatalf("label %v", dry.Label)
	}
	if !slices.Equal(dry.Mixed, []string{root}) {
		t.Fatalf("mixed %v", dry.Mixed)
	}
	if b, _ := m.Show(ctx, "", root); slices.Contains(b.Labels, beads.WorkstreamLabel) {
		t.Fatal("a dry run wrote")
	}

	if _, err := migrateWorkstreams(ctx, m, "", true); err != nil {
		t.Fatal(err)
	}
	b, _ := m.Show(ctx, "", track)
	if !slices.Contains(b.Labels, beads.WorkstreamLabel) || slices.Contains(b.Labels, beads.TrackLabel) {
		t.Fatalf("track labels %v", b.Labels)
	}
	again, err := migrateWorkstreams(ctx, m, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Label) != 0 {
		t.Fatalf("not idempotent: %v", again.Label)
	}
}
