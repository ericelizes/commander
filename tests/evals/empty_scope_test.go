package evals

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only fixture: an open workstream with nothing in it reads "Not started: Manager to plan"
// (planning is the Manager's move) or, while a Manager-to-plan handoff holds it, "Manager planning".
// It never reads "Nothing to do".
func TestEmptyWorkstreamScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/empty-workstream.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	rows := livedata.Board(snap, 1060).Workstreams
	want := map[string]string{"demo-ew": "Not started: Manager to plan", "demo-ep": "Manager planning"}
	for _, r := range rows {
		if r.Text != want[r.BeadID] {
			t.Errorf("%s reads %q, want %q", r.BeadID, r.Text, want[r.BeadID])
		}
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
}
