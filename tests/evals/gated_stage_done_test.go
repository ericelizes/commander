package evals

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only board fixture: completed review children do not pass a stage's
// merge or CI condition, nor satisfy its predecessor's user decision.
func TestGatedStageDoneScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/gated-stage-done.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	ws := snap.Workstreams[0]
	var got []string
	for _, f := range livedata.CheckDiagram(ws, ws.BeadID) {
		if f.Check == "stage-done" {
			got = append(got, f.Bead)
		}
	}
	if !slices.Equal(got, []string{"demo-flow.4"}) {
		t.Fatalf("stage-done findings %v, want only completed ungated build", got)
	}
	for _, id := range []string{"demo-flow.2", "demo-flow.3", "demo-flow.5", "demo-flow.6.1"} {
		for _, f := range livedata.CheckDiagram(ws, id) {
			if f.Check == "stage-done" {
				t.Errorf("held stage %s reported complete: %+v", id, f)
			}
		}
	}
	// The board (what cmdr board --snapshot --diagram prints) carries the same findings.
	d := livedata.Board(snap, 1)
	if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil {
		t.Fatalf("board rows: %+v", d.Workstreams)
	}
	var drawn []string
	for _, f := range d.Workstreams[0].Diagram.Findings {
		if f.Check == "stage-done" {
			drawn = append(drawn, f.Bead)
		}
	}
	if !slices.Equal(drawn, []string{"demo-flow.4"}) {
		t.Fatalf("board diagram stage-done findings %v, want only demo-flow.4", drawn)
	}
}
