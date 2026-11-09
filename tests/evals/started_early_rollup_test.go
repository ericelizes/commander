package evals

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only board fixture: an open stage whose state only rolled up from a
// closed child is not started early and draws Not started; a step that really
// is in progress ahead of what it waits on still is.
func TestStartedEarlyRollupScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/started-early-rollup.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	early := func(fs []livedata.DiagramFinding) []string {
		var out []string
		for _, f := range fs {
			if f.Check == "started-early" {
				out = append(out, f.Bead)
			}
		}
		return out
	}
	want := []string{"demo-flow.3"}
	ws := snap.Workstreams[0]
	if got := early(livedata.CheckDiagram(ws, ws.BeadID)); !slices.Equal(got, want) {
		t.Fatalf("started-early findings %v, want %v", got, want)
	}
	// The board (what cmdr board --snapshot --diagram prints) says the same, and
	// draws the rolled-up stage as the finding now reads it.
	d := livedata.Board(snap, 1)
	if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil {
		t.Fatalf("board rows: %+v", d.Workstreams)
	}
	diagram := d.Workstreams[0].Diagram
	if got := early(diagram.Findings); !slices.Equal(got, want) {
		t.Fatalf("board diagram started-early findings %v, want %v", got, want)
	}
	// Its advice clears a claim as well as the status: --state alone leaves the owner on.
	for _, f := range diagram.Findings {
		if f.Check == "started-early" && !strings.Contains(f.Fix, "--state not-started --owner none") {
			t.Errorf("started-early fix: %q", f.Fix)
		}
	}
	i := slices.IndexFunc(diagram.Steps, func(s livedata.DiagramStep) bool { return s.ID == "demo-flow.2" })
	if i < 0 || diagram.Steps[i].Text != "Not started" {
		t.Fatalf("demo-flow.2 in diagram: %+v", diagram.Steps)
	}
}
