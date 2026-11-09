package evals

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only fixture: the invariant (graph check), the audit and the board's
// Broken flag all read "held" from one predicate, so they name the same steps.
// A child blocked only through its parent's open blocker is held.
func TestHeldParityScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/held-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range livedata.SnapshotViolations(snap) {
		got = append(got, v.BeadID)
	}
	// demo-hp.3's slug wait is a bare label; the snapshot only carries text, so it
	// reads held here. Only the step with nothing on it is a violation, and the
	// workstream bead (no owner, but it has open children) is not.
	want := []string{"demo-hp.4"}
	if !slices.Equal(got, want) {
		t.Fatalf("violations %v, want %v", got, want)
	}
	for _, id := range []string{"demo-hp.2", "demo-hp.2.1"} {
		if slices.Contains(got, id) {
			t.Errorf("%s reads unheld although it waits behind an open blocker", id)
		}
	}
	// The same steps mark Broken on the board.
	s := livedata.WithEdits(snap, nil)
	var broken []string
	for _, st := range s.Workstreams[0].Steps {
		if st.Broken {
			broken = append(broken, st.BeadID)
		}
	}
	if !slices.Equal(broken, want) {
		t.Fatalf("broken steps %v, want %v", broken, want)
	}
}
