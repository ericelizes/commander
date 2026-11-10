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
	// A genuine sibling user action stays visible while the hidden failure persists.
	scenario.Snapshot.Workstreams[0].Steps = append(scenario.Snapshot.Workstreams[0].Steps, livedata.SnapStep{BeadID: "sample-flow.card", Status: "open", State: "needs-user", Waiting: "you"})
	for _, reaction := range []struct {
		state  string
		unread int
	}{{"idle", 0}, {"working", 0}, {"idle", 1}, {"idle", 0}} {
		scenario.Snapshot.Agents["manager-sample-flow"] = livedata.SnapAgent{State: reaction.state}
		scenario.Snapshot.Unread = map[string]int{"manager-sample-flow": reaction.unread}
		if row := livedata.Board(scenario.Snapshot, scenario.Now).Workstreams[0]; row.Kind != "needs" {
			t.Fatalf("user action hidden: %+v", row)
		}
		failures := livedata.StalledWork(scenario.Snapshot, scenario.Now)
		if len(failures) != 1 || !strings.Contains(livedata.CheckinReport(scenario.Snapshot, scenario.Now, livedata.AuditOptions{}), "Manager review overdue") {
			t.Fatalf("hidden failure lost: %+v", failures)
		}
	}
	// Completing existing work ends failure without creating reviews, cards or gates.
	scenario.Snapshot.Workstreams[0].Steps[0].Status = "closed"
	if got := livedata.StalledWork(scenario.Snapshot, scenario.Now); len(got) != 0 {
		t.Fatalf("closed report still failing: %+v", got)
	}
}
