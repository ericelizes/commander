package evals

import (
	"encoding/json"
	"github.com/example-user/commander/internal/livedata"
	"os"
	"strings"
	"testing"
)

func TestAutomationFailureScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/automation-failure.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Now      int64
		Snapshot livedata.Snapshot
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	r := livedata.Board(scenario.Snapshot, scenario.Now).Workstreams[0]
	if r.Kind != "stalled" || r.Flag != 0 || !strings.Contains(r.Why, "Owner: manager-sample-flow") || !strings.Contains(r.Why, "existing report") || !strings.Contains(r.Why, "sample-flow.1") {
		t.Fatalf("failure hidden: %+v", r)
	}
	report := livedata.CheckinReport(scenario.Snapshot, scenario.Now, livedata.AuditOptions{})
	if !strings.Contains(report, "[red] Manager review overdue") || strings.Contains(report, "Closed report |") {
		t.Fatal(report)
	}
	// Completing existing work ends failure without creating reviews, cards or gates.
	scenario.Snapshot.Workstreams[0].Steps[0].Status = "closed"
	if got := livedata.StalledWork(scenario.Snapshot, scenario.Now); len(got) != 0 {
		t.Fatalf("closed report still failing: %+v", got)
	}
}
