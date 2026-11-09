package evals

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestTruthfulCheckinScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/truthful-checkin.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Snapshot livedata.Snapshot
		Expected struct{ Errors, Blockers, Advisories int }
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	s := scenario.Snapshot
	errors, blockers, advisories := livedata.FindingCounts(s, 1000)
	if errors != scenario.Expected.Errors || blockers != scenario.Expected.Blockers || advisories != scenario.Expected.Advisories {
		t.Fatalf("counts = %d/%d/%d", errors, blockers, advisories)
	}
	if errors+blockers+advisories != len(s.Display.Workstreams[0].Diagram.Findings) {
		t.Fatal("counter differs from graph findings")
	}
	report := livedata.CheckinReport(s, 1000, livedata.AuditOptions{})
	if !strings.Contains(report, "Workflow errors: 8\nBlockers: 5\nAdvisories: 2") {
		t.Fatal(report)
	}
}

// The findings the counter must not get wrong: a started-early rollup and a
// reported Operator are no errors; a real early start and a silent stop are.
func TestCheckinCountsExpectedFindingsOnly(t *testing.T) {
	var early livedata.Snapshot
	raw, err := os.ReadFile("scenarios/started-early-rollup.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &early); err != nil {
		t.Fatal(err)
	}
	var beads []string
	for _, f := range livedata.WorkflowFindings(early, 1) {
		beads = append(beads, livedata.FindingKind(f)+" "+f.Bead)
	}
	if strings.Join(beads, ",") != "error demo-flow.3" {
		t.Fatalf("started-early findings = %v", beads)
	}

	var gap struct {
		Now      int64             `json:"now"`
		Snapshot livedata.Snapshot `json:"snapshot"`
	}
	raw, err = os.ReadFile("scenarios/reported-gap.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &gap); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range livedata.WorkflowFindings(gap.Snapshot, gap.Now) {
		found[f.Bead] = true
	}
	if found["app-w2.1"] {
		t.Fatalf("reported step counted as a finding: %v", found)
	}
	if errors, _, _ := livedata.FindingCounts(gap.Snapshot, gap.Now); errors == 0 {
		t.Fatal("the step that stopped with no report is not counted")
	}
}
