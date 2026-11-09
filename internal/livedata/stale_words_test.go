package livedata

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestStaleWordsReachBoardAndDiagram(t *testing.T) {
	raw, err := os.ReadFile("testdata/board/stale-words.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64
		Snapshot Snapshot
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	before := mustJSON(fx.Snapshot)
	d := Board(fx.Snapshot, fx.Now)
	lines := strings.Join(BoardLines(d, fx.Now, "commander"), "\n")
	diagram := strings.Join(DiagramLines(d.Workstreams[0].Diagram), "\n")
	if strings.Contains(lines, "The spec changed") || strings.Contains(diagram, "The spec changed") {
		t.Errorf("a spec edit is for the Manager, not shown:\n%s\n%s", lines, diagram)
	}
	for _, phrase := range []string{"Asked 9 days ago", "Pushed back 4 times since"} {
		if !strings.Contains(lines, phrase) || !strings.Contains(diagram, phrase) {
			t.Errorf("missing %q from board or diagram:\n%s\n%s", phrase, lines, diagram)
		}
	}
	if !strings.Contains(diagram, "Pushed back 3 times since") {
		t.Errorf("wait push missing: %s", diagram)
	}
	if d.Asks[3].StaleWhy != "" {
		t.Errorf("fresh card flagged: %+v", d.Asks[3])
	}
	if mustJSON(fx.Snapshot) != before {
		t.Fatal("Board mutated the source snapshot")
	}
}
