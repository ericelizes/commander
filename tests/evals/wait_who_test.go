package evals

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Read-only board fixture: every wait kind's step is drawn with the who the
// board's line speaks for (cmdr board --snapshot scenarios/wait-who.json --diagram 1).
// Only a time wait reads as a timer; a PR approval names the PR.
func TestWaitWhoScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/wait-who.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap livedata.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	d := livedata.Board(snap, 1793000000)
	if len(d.Workstreams) != 1 || d.Workstreams[0].Diagram == nil {
		t.Fatalf("board rows: %+v", d.Workstreams)
	}
	who := map[string]string{}
	for _, s := range d.Workstreams[0].Diagram.Steps {
		who[s.ID] = s.Actor + "|" + s.Who
	}
	for id, want := range map[string]string{
		"lab-wk.1":  "automatic|CI, runs on each push to the PR",
		"lab-wk.3":  "automatic|CI on main, runs on the merge",
		"lab-wk.8":  "automatic|a timer",
		"lab-wk.9":  "automatic|a timer",
		"lab-wk.11": "automatic|a prod deploy with 2ac6897",
		"lab-wk.12": "reviewers|PR approval (infrastructure#2163)",
		"lab-wk.13": "reviewers|PR merge (#3960)",
		"lab-wk.14": "automatic|CI on the PR (#3996)",
		"lab-wk.15": "reviewers|a Slack message in C0123",
	} {
		if who[id] != want {
			t.Errorf("%s drawn as %q, want %q", id, who[id], want)
		}
	}
	timers := 0
	for _, w := range who {
		if w == "automatic|a timer" {
			timers++
		}
	}
	if timers != 2 {
		t.Errorf("%d steps read as a timer, want only the 2 time waits: %v", timers, who)
	}
}
