package evals

import (
	"encoding/json"
	"github.com/example-user/commander/internal/livedata"
	"os"
	"strings"
	"testing"
)

func TestPausedConfirmDoneScenario(t *testing.T) {
	raw, e := os.ReadFile("scenarios/paused-confirm-done.json")
	if e != nil {
		t.Fatal(e)
	}
	var s livedata.Snapshot
	if e = json.Unmarshal(raw, &s); e != nil {
		t.Fatal(e)
	}
	d := livedata.Board(s, 100)
	if len(d.Workstreams) != 1 || len(d.Workstreams[0].Tracks) != 2 {
		t.Fatalf("paused workstream disappeared beside confirmation: %+v", d.Workstreams)
	}
	if d.Workstreams[0].Tracks[0].Text != "Paused" {
		t.Fatalf("paused row: %+v", d.Workstreams[0].Tracks[0])
	}
	if !s.Workstreams[0].Steps[1].Children[1].ClosingGate {
		t.Fatal("main Confirm done identity lost")
	}
	workflow, _ := json.Marshal(d.Workstreams[0].Workflow)
	if !strings.Contains(string(workflow), "Confirm done") {
		t.Fatalf("main confirmation step hidden: %s", workflow)
	}
}
