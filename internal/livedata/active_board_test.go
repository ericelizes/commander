package livedata

import (
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func activeBoardFixture() Snapshot {
	return Snapshot{Storage: "postgres", Projects: []SnapProject{{ID: 1, Name: "p"}}, OrchestrationMode: "active", Executions: []SnapExecution{}, Agents: map[string]SnapAgent{"manager-old": {State: store.AgentWorking}}, Workstreams: []SnapWorkstream{{N: 1, Project: "p", ProjectID: 1, BeadID: "root", Title: "Goal", Status: "open", PlanningCommander: "commander-focused", PlanningPending: true, ManagerRole: "manager-old", Steps: []SnapStep{{BeadID: "leaf", Title: "Work", Status: "open", State: "open", PlanningCommander: "commander-focused", PlanningPending: true}}}}}
}
func TestActiveBoardPlanningObligationDoesNotInventAgentActivity(t *testing.T) {
	s := activeBoardFixture()
	for _, now := range []int64{1, 86400} {
		d := Board(s, now)
		applyExecutionCounts(&s, &d)
		row := d.Workstreams[0]
		if row.Kind != "waiting" || row.Text != "Commander to plan the next action" || row.Working != 0 || row.PlanningCommander != "commander-focused" {
			t.Fatal("legacy Manager inference escaped", row)
		}
		if violations := SnapshotViolations(s); len(violations) != 0 {
			t.Fatal("recorded planning responsibility became broken state", violations)
		}
	}
	s.Workstreams[0].PlanningPending = false
	s.Workstreams[0].Steps[0].PlanningPending = false
	if row := Board(s, 1).Workstreams[0]; row.Kind != "stalled" || row.Text != "Assignment needs reconciliation" {
		t.Fatal("missing custody was concealed", row)
	}
}
func TestActiveBoardPreservesWaitsHoldsAndRealDecisionPriority(t *testing.T) {
	s := activeBoardFixture()
	ws := &s.Workstreams[0]
	ws.Steps[0].Waiting = beads.ManagerCloseWait
	ws.Steps[0].WaitSince = 1
	if row := Board(s, 86400).Workstreams[0]; row.Text != "Commander to reconcile the recorded handoff" || row.Kind != "waiting" {
		t.Fatal("legacy receipt became overdue or active work", row)
	}
	ws.Steps[0].Waiting = "CI #41"
	if row := Board(s, 86400).Workstreams[0]; !strings.Contains(row.Text, "Waiting for CI") {
		t.Fatal("external wait erased", row)
	}
	ws.Hold = true
	if row := Board(s, 86400).Workstreams[0]; row.Text != "Paused by recorded direction" {
		t.Fatal("hold became new work", row)
	}
	ws.Hold = false
	ws.Steps[0].Waiting = ""
	ws.HumanAnswerPending = true
	if row := Board(s, 86400).Workstreams[0]; row.Text != "Applying your recorded answer" || row.Kind == "needs" {
		t.Fatal("saved answer asked twice", row)
	}
	ws.HumanAnswerPending = false
	s.NeedsYou = []NeedsYou{{Project: "p", BeadID: "root", Workstream: "Goal", Title: "Approve release?"}}
	if row := Board(s, 86400).Workstreams[0]; row.Kind != "needs" || !strings.Contains(row.Text, "Approve release?") {
		t.Fatal("real decision disappeared", row)
	}
}
func TestActiveBoardCountsOnlyCurrentExecutionAndChecksUnknownCustody(t *testing.T) {
	s := activeBoardFixture()
	s.Workstreams[0].Steps[0].PlanningPending = false
	s.ExecutionChecks = []SnapExecution{{ProjectID: 1, BeadID: "leaf", RunID: 7, SessionID: "current", Role: "executor-current"}}
	if row := Board(s, 1).Workstreams[0]; row.Kind != "waiting" || row.Text != "Checking execution status" || row.Working != 0 {
		t.Fatal("unknown custody was treated as planning or execution", row)
	}
	s.ExecutionChecks = nil
	s.Executions = []SnapExecution{{ProjectID: 1, BeadID: "leaf", RunID: 7, SessionID: "current", Role: "executor-current", State: store.AgentWorking}}
	applyObservedWorking(&s)
	d := Board(s, 1)
	applyExecutionCounts(&s, &d)
	if row := d.Workstreams[0]; row.Kind != "working" || row.Working != 1 {
		t.Fatal("actual execution disappeared", row)
	}
}
