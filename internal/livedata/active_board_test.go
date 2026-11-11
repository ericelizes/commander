package livedata

import (
	"strings"
	"testing"
	"time"

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
		if row.Kind != "working" || row.Text != "Commander to plan the next step" || row.Working != 0 || row.PlanningCommander != "commander-focused" || row.Actor == nil || row.Actor.Lead.Who != "commander-focused" {
			t.Fatal("a retired Manager's activity leaked into the row", row)
		}
		if violations := SnapshotViolations(s); len(violations) != 0 {
			t.Fatal("recorded planning responsibility became broken state", violations)
		}
	}
	// The responsible Commander idle well past the grace: the row says it is stuck.
	s.Agents["commander-focused"] = SnapAgent{State: store.AgentIdle, Since: 1}
	if row := Board(s, 86400).Workstreams[0]; row.Kind != "stuck" || !strings.HasPrefix(row.Text, "Stuck: Commander has not started to plan") {
		t.Fatal("an idle Commander owing a move was concealed", row)
	}
	s.Agents["commander-focused"] = SnapAgent{State: store.AgentWorking, Since: 86000}
	if row := Board(s, 86400).Workstreams[0]; row.Kind != "working" || row.Text != "Commander: plan the next step" {
		t.Fatal("a working Commander is not shown working", row)
	}
}

func TestActiveBoardPreservesWaitsHoldsAndRealDecisionPriority(t *testing.T) {
	s := activeBoardFixture()
	ws := &s.Workstreams[0]
	s.Agents["commander-focused"] = SnapAgent{State: store.AgentWorking}
	ws.Steps[0].TypedWaits = []beads.Wait{{Kind: beads.WaitUntil, Ref: beads.ManagerCloseWait, Until: time.Unix(86000, 0), Since: time.Unix(1, 0)}}
	if row := Board(s, 86400).Workstreams[0]; !strings.HasPrefix(row.Text, "Commander: act on") || row.Kind != "working" {
		t.Fatal("a legacy Manager receipt is not Commander-owed work", row)
	}
	ws.Steps[0].TypedWaits = []beads.Wait{{Kind: beads.WaitCI, PR: 41, Since: time.Unix(80000, 0)}}
	if row := Board(s, 86400).Workstreams[0]; row.Kind != "waiting" || !strings.Contains(row.Text, "Waiting on CI #41") {
		t.Fatal("external wait erased", row)
	}
	ws.Hold, ws.HoldReason = true, "until the batch lands"
	if row := Board(s, 86400).Workstreams[0]; row.Kind != "quiet" || row.Text != "Paused: until the batch lands" {
		t.Fatal("hold became new work", row)
	}
	ws.Hold = false
	ws.Steps[0].TypedWaits = nil
	ws.HumanAnswerPending = true
	if row := Board(s, 86400).Workstreams[0]; row.Kind == "needs" || !strings.Contains(row.Text, "apply your recorded answer") {
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
	s.Workstreams[0].Steps[0].ProgressAt = 1
	s.ExecutionChecks = []SnapExecution{{ProjectID: 1, BeadID: "leaf", RunID: 7, SessionID: "current", Role: "executor-current"}}
	if row := Board(s, 60).Workstreams[0]; row.Kind != "working" || !strings.HasPrefix(row.Text, "Starting an agent") || row.Working != 0 {
		t.Fatal("a fresh claim was counted as execution or hidden", row)
	}
	if row := Board(s, 3600).Workstreams[0]; row.Kind != "stuck" || !strings.HasPrefix(row.Text, "Stuck: claimed") {
		t.Fatal("a claim with no process for an hour is not stuck", row)
	}
	s.ExecutionChecks = nil
	s.Executions = []SnapExecution{{ProjectID: 1, BeadID: "leaf", RunID: 7, SessionID: "current", Role: "executor-current", State: store.AgentWorking}}
	s.Workstreams[0].Steps[0].ProgressAt = 3000
	applyObservedWorking(&s)
	d := Board(s, 3600)
	applyExecutionCounts(&s, &d)
	if row := d.Workstreams[0]; row.Kind != "working" || row.Working != 1 || !strings.HasPrefix(row.Text, "Working on") {
		t.Fatal("actual execution disappeared", row)
	}
}

func TestActiveBoardExhaustedLaunchShowsRepairWithoutInventingActivity(t *testing.T) {
	s := activeBoardFixture()
	facts := Data{Postgres: true, ObservedExecutions: []store.ObservedExecution{{ProjectID: 1, BeadID: "hidden", Path: []string{"hidden", "leaf", "root"}, RunID: 7, SessionID: "current", Role: "executor-current", StartupAttempts: 5, StartupError: "adapter refused startup"}}}
	s.ExecutionChecks = unverifiedExecutionOf(s, facts)
	s.Executions = observedExecutionOf(s, facts)
	s.Workstreams[0].Steps[0].Children = []SnapStep{{BeadID: "hidden", Title: "Hidden work", Status: "open"}}
	d := Board(s, 1)
	applyExecutionCounts(&s, &d)
	if row := d.Workstreams[0]; row.Kind != "stuck" || !strings.Contains(row.Text, "could not start (adapter refused startup)") || row.Working != 0 || row.PlanningCommander != "commander-focused" {
		t.Fatal("exhausted startup hidden or counted active", row)
	}
	if len(s.ExecutionChecks) != 1 || s.ExecutionChecks[0].SessionID != "current" || s.ExecutionChecks[0].RunID != 7 || s.Fleet.Active != 0 {
		t.Fatal("repair lost exact execution identity", s.ExecutionChecks, s.Fleet)
	}
	s.Workstreams[0].Hold = true
	if row := Board(s, 1).Workstreams[0]; !strings.HasPrefix(row.Text, "Paused") {
		t.Fatal("repair overrode hold", row)
	}
}

func TestActiveBoardFollowsDependencyChainsToTheUser(t *testing.T) {
	s := activeBoardFixture()
	s.Agents["commander-focused"] = SnapAgent{State: store.AgentWorking}
	s.Workstreams = append(s.Workstreams, SnapWorkstream{N: 2, Project: "p", ProjectID: 1, BeadID: "other", Title: "Mac", Status: "open", Steps: []SnapStep{
		{BeadID: "gate", Title: "Greenlight the repair batch", Status: "open", Gate: true, Approvers: []string{"user"}},
		{BeadID: "mac", Title: "Mac execution", Status: "open", BlockedBy: []string{"gate"}},
	}})
	s.NeedsYou = []NeedsYou{{Project: "p", BeadID: "gate", Workstream: "Mac", Title: "Greenlight the repair batch"}}
	s.Workstreams[0].Steps[0].PlanningPending = false
	s.Workstreams[0].Steps[0].BlockedBy = []string{"mac"}
	row := Board(s, 100).Workstreams[0]
	if row.Kind != "needs" || !strings.Contains(row.Text, "Greenlight the repair batch") || row.Actor == nil || row.Actor.Lead.Bead != "gate" || len(row.Actor.Lead.Via) == 0 {
		t.Fatal("a chain ending at the user does not say so", row, row.Actor)
	}
}

func TestActiveBoardHidesWithdrawnRequestsAndRollsUpCounts(t *testing.T) {
	s := activeBoardFixture()
	s.Workstreams = append(s.Workstreams, SnapWorkstream{N: 2, Project: "p", ProjectID: 1, BeadID: "gone", Title: "Old gate", Status: beads.StatusDeferred, Withdrawn: true})
	if d := Board(s, 1); len(d.Workstreams) != 1 || d.Workstreams[0].BeadID != "root" {
		t.Fatal("a withdrawn request is still a row", d.Workstreams)
	}
	s.Workstreams[0].Steps = append(s.Workstreams[0].Steps,
		SnapStep{BeadID: "a", Title: "Build", Status: "open", ProgressAt: 90},
		SnapStep{BeadID: "b", Title: "Test", Status: "open", ProgressAt: 90},
		SnapStep{BeadID: "c", Title: "Merge", Status: "open", TypedWaits: []beads.Wait{{Kind: beads.WaitCI, PR: 9}}})
	s.Executions = []SnapExecution{{ProjectID: 1, BeadID: "a", Role: "exec-a", State: store.AgentWorking}, {ProjectID: 1, BeadID: "b", Role: "exec-b", State: store.AgentWorking}}
	s.NeedsYou = []NeedsYou{{Project: "p", BeadID: "leaf", Workstream: "Goal", Title: "Approve the plan"}}
	row := Board(s, 100).Workstreams[0]
	if row.Kind != "needs" || row.Text != "Needs you: Approve the plan · 2 agents working · 1 waiting outside" {
		t.Fatal("the row hides what else is going on", row.Text)
	}
}

func TestActiveBoardIgnoresRetiredManagerCapacity(t *testing.T) {
	for _, pending := range []bool{false, true} {
		s := activeBoardFixture()
		s.Workstreams[0].PlanningPending = pending
		s.Workstreams[0].Steps[0].PlanningPending = pending
		want := Board(s, 86400).Workstreams[0]
		s.ManagerCapacity = map[string]string{"manager-old": "retired Manager has no usage"}
		got := Board(s, 86400).Workstreams[0]
		if got.Kind != want.Kind || got.Text != want.Text || got.Why != want.Why || got.Working != want.Working {
			t.Fatalf("retired Manager capacity replaced canonical planning/custody: got %+v; want %+v", got, want)
		}
	}
}
