package gates

import (
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"slices"
	"testing"
)

func TestLegacyVerifierReadsAndWritesUser(t *testing.T) {
	n := parse(beads.Bead{Labels: []string{"kind:gate", "verifier:user"}}, 1, nil)
	if n.Gate.Verifier != store.VerifierUser {
		t.Fatalf("%+v", n)
	}
	labels := Labels(store.Gate{Verifier: "user"})
	if !slices.Contains(labels, "approver:user") || !slices.Contains(labels, "verifier:user") {
		t.Fatalf("%v", labels)
	}
}
