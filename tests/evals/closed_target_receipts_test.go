package evals

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only board fixture: a merge target closed after its PR merged gets no PR
// read any more, so its unknown head must not turn the block's receipts stale.
func TestClosedTargetReceiptsScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/closed-target-receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	ws := snap.Workstreams[0]
	for _, f := range livedata.CheckDiagram(ws, ws.BeadID) {
		if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
			t.Errorf("merge rule on a closed target: %+v", f)
		}
	}
	// The board (what cmdr board --snapshot --diagram prints) reads the block as
	// done, with every receipt standing and no move left for the Manager.
	d := livedata.Board(snap, 1)
	if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil {
		t.Fatalf("board rows: %+v", d.Workstreams)
	}
	var block *livedata.DiagramStep
	for i, s := range d.Workstreams[0].Diagram.Steps {
		if s.ID == "w.9" {
			block = &d.Workstreams[0].Diagram.Steps[i]
		}
	}
	if block == nil || block.Review == nil {
		t.Fatalf("block missing: %+v", d.Workstreams[0].Diagram.Steps)
	}
	if block.Kind != "done" || block.Text != "Done" || block.Progress != "6 of 6 checks done" {
		t.Errorf("block: %s %q progress %q", block.Kind, block.Text, block.Progress)
	}
	if block.Review.Done != 6 || block.Review.Stale != 0 {
		t.Errorf("receipts: %+v", block.Review)
	}
	for _, c := range block.Review.Checks {
		if c.Stale || c.Kind != "done" || c.Finding != "passed" {
			t.Errorf("check: %+v", c)
		}
	}
}
