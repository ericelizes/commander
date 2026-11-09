package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestCardStaleAgeOffAndBoundary(t *testing.T) {
	defer SetStaleDays(7)
	const now = 1_800_000_000
	old := NeedsYou{Since: now - 30*86400}
	SetStaleDays(0)
	if r, _ := CardStale(old, now); r != "" {
		t.Fatalf("stale_days 0 switches the age rule off, got %q", r)
	}
	SetStaleDays(7)
	if r, _ := CardStale(NeedsYou{Since: now - 7*86400}, now); r != "" {
		t.Fatalf("exactly 7 days is not over 7 days, got %q", r)
	}
	if r, _ := CardStale(NeedsYou{Since: now - 7*86400 - 1}, now); r != StaleAge {
		t.Fatalf("over 7 days: %q", r)
	}
	// Spec-only: a changed spec stales a young card even with the age rule off.
	SetStaleDays(0)
	if r, _ := CardStale(NeedsYou{Since: now, SpecHash: "b", Basis: &store.Basis{Spec: "a", Raised: now}}, now); r != StaleSpec {
		t.Fatalf("spec change: %q", r)
	}
}

func TestSpecHashIgnoresEverythingButTheSpec(t *testing.T) {
	base := beads.Bead{ID: "p-1", Title: "T", Description: "D", AcceptanceCriteria: "A", Status: "open", Labels: []string{"x"}, UpdatedAt: "1", CommentCount: 1}
	noisy := base
	noisy.Labels = []string{"x", "waiting:1:user", "wait-checked:5", "owner:op"}
	noisy.UpdatedAt, noisy.CommentCount, noisy.DependencyCount, noisy.Status, noisy.AssignedAgent = "9", 40, 3, "in_progress", "someone"
	if beads.SpecHash(base) != beads.SpecHash(noisy) {
		t.Fatal("comments, labels, waits, wait-checked, owner and status must not move the spec hash")
	}
	for _, edit := range []func(*beads.Bead){
		func(b *beads.Bead) { b.Title = "T2" },
		func(b *beads.Bead) { b.Description = "D2" },
		func(b *beads.Bead) { b.AcceptanceCriteria = "A2" },
	} {
		e := base
		edit(&e)
		if beads.SpecHash(base) == beads.SpecHash(e) {
			t.Fatal("a spec edit must move the hash")
		}
	}
}
