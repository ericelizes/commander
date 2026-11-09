package evals

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only board fixture: a pr-review block whose parent closed is drawn nowhere,
// and the merge rule still reads it before its open target, as the door does.
// Regenerate: GEN_FOLDED_BLOCK_FIXTURE=1 go test ./internal/livedata -run GenerateFoldedBlockFixture
func TestFoldedBlockScenario(t *testing.T) {
	load := func(t *testing.T) livedata.Snapshot {
		t.Helper()
		raw, err := os.ReadFile("scenarios/folded-block.json")
		if err != nil {
			t.Fatal(err)
		}
		var snap livedata.Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	findings := func(fs []livedata.DiagramFinding) []string {
		var out []string
		for _, f := range fs {
			if strings.HasPrefix(f.Check, "MERGE_REVIEW_") {
				out = append(out, f.Check+" "+f.Bead)
			}
		}
		return out
	}
	target := func(snap *livedata.Snapshot) *livedata.SnapStep {
		steps := snap.Workstreams[0].Steps
		i := slices.IndexFunc(steps, func(s livedata.SnapStep) bool { return s.BeadID == "w.2" })
		if i < 0 {
			t.Fatalf("no target: %+v", steps)
		}
		return &steps[i]
	}

	t.Run("complete block", func(t *testing.T) {
		snap := load(t)
		if got := findings(livedata.CheckDiagram(snap.Workstreams[0], "w")); got != nil {
			t.Fatalf("findings: %v", got)
		}
		d := livedata.Board(snap, 1)
		if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil {
			t.Fatalf("board rows: %+v", d.Workstreams)
		}
		if got := findings(d.Workstreams[0].Diagram.Findings); got != nil {
			t.Fatalf("board findings: %v", got)
		}
		for _, s := range d.Workstreams[0].Diagram.Steps {
			if strings.HasPrefix(s.ID, "w.3.") {
				t.Errorf("the folded block is drawn: %+v", s)
			}
		}
	})
	t.Run("a new push", func(t *testing.T) {
		snap := load(t)
		target(&snap).WorkflowCheck.Head = "head2"
		want := []string{"MERGE_REVIEW_INCOMPLETE w.2"}
		if got := findings(livedata.CheckDiagram(snap.Workstreams[0], "w")); !slices.Equal(got, want) {
			t.Fatalf("findings: %v, want %v", got, want)
		}
	})
	t.Run("a closed target", func(t *testing.T) {
		snap := load(t)
		s := target(&snap)
		s.Status, s.WorkflowCheck.Head = "closed", "head2"
		if got := findings(livedata.CheckDiagram(snap.Workstreams[0], "w")); got != nil {
			t.Fatalf("findings: %v", got)
		}
	})
}
