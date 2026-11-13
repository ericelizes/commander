package evals

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// An Operator ended with a report and cmdrd has not settled it yet: the board and
// the plan read it as the Manager's move, beside a step that stopped with no report.
func TestReportedGapScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/reported-gap.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64             `json:"now"`
		Snapshot livedata.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	d := livedata.Board(fx.Snapshot, fx.Now)
	if len(d.Workstreams) != 2 {
		t.Fatalf("rows: %+v", d.Workstreams)
	}
	beside, stopped := d.Workstreams[0], d.Workstreams[1]
	if beside.Text == "State missing" || beside.Kind != "working" {
		t.Fatalf("reported step beside live work reads %q (%s), why %q", beside.Text, beside.Kind, beside.Why)
	}
	for _, f := range beside.Diagram.Findings {
		if f.Bead == "app-w2.1" {
			t.Fatalf("reported step flagged: %+v", f)
		}
	}
	if stopped.Kind != "stalled" || !strings.HasPrefix(stopped.Why, "Agent is no longer running and has not reported") || !strings.Contains(stopped.Why, "Owner: manager-app-w3") || !strings.Contains(stopped.Why, "current holder") || !strings.Contains(stopped.Why, "app-w3.1") {
		t.Fatalf("no report reads %q, why %q", stopped.Text, stopped.Why)
	}
}
