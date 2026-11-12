package supervision

import (
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"testing"
	"time"
)

func TestResponsibilityPolicyAcrossAgentCapabilities(t *testing.T) {
	now := time.Unix(20000, 0)
	base := store.SupervisionFact{AgentID: "workstream-qa", SupervisorID: "commander", Status: "open", PresenceKnown: true, ProgressAt: now.Unix() - 60}
	cases := []struct {
		name                   string
		change                 func(*store.SupervisionFact)
		action, reason, target string
	}{
		{"idle actionable", func(f *store.SupervisionFact) {}, "wake", "work_pending", "workstream-qa"},
		{"working", func(f *store.SupervisionFact) { f.Alive = true; f.AgentState = "working"; f.ExecutorBound = true }, "observe", "working", "workstream-qa"},
		{"paused", func(f *store.SupervisionFact) { f.Held = true }, "observe", "held", "workstream-qa"},
		{"blocked dependency", func(f *store.SupervisionFact) { f.OpenBlockers = 1 }, "observe", "dependency", "workstream-qa"},
		{"subordinates working", func(f *store.SupervisionFact) { f.ChildCount = 2 }, "observe", "subordinates", "workstream-qa"},
		{"stalled subordinates all legitimately waiting", func(f *store.SupervisionFact) {
			f.ChildCount = 4
			f.ProgressAt = now.Add(-time.Hour).Unix()
			f.Children = []store.SupervisionChild{{Status: "open", Labels: []string{"needs-user"}}, {Status: "open", ExecutionReady: true}, {Status: "open", Labels: []string{"hold"}}, {Status: "open", HasOpenChildren: true}}
		}, "observe", "subordinates_waiting", "workstream-qa"},
		{"stalled subordinate with a next step", func(f *store.SupervisionFact) {
			f.ChildCount = 2
			f.ProgressAt = now.Add(-time.Hour).Unix()
			f.Children = []store.SupervisionChild{{Status: "open", Labels: []string{"needs-user"}}, {Status: "open"}}
		}, "wake", "subordinate_progress", "workstream-qa"},
		{"result delivered", func(f *store.SupervisionFact) { f.ResultReady = true }, "wake", "result_pending", "workstream-qa"},
		{"reviewer busy", func(f *store.SupervisionFact) { f.ResultReady = true; f.AgentState = "working"; f.Alive = true }, "defer", "result_pending", "workstream-qa"},
		{"unknown runtime", func(f *store.SupervisionFact) { f.PresenceKnown = false }, "observe", "runtime_unknown", "workstream-qa"},
		{"missing resumable execution", func(f *store.SupervisionFact) { f.ExecutorBound = true }, "resume", "work_pending", "workstream-qa"},
		{"busy without progress", func(f *store.SupervisionFact) {
			f.Alive = true
			f.ExecutorBound = true
			f.AgentState = "working"
			f.ProgressAt = now.Add(-2 * time.Hour).Unix()
		}, "wake", "progress_check", "commander"},
		{"working agent on a long run", func(f *store.SupervisionFact) {
			f.Alive = true
			f.ExecutorBound = true
			f.AgentState = "working"
			f.ProgressAt = now.Add(-40 * time.Minute).Unix()
		}, "observe", "working", "workstream-qa"},
		{"waiting on CI is never nudged", func(f *store.SupervisionFact) {
			f.Labels = []string{"waiting:1700000000:ci:12"}
		}, "observe", "dependency", "workstream-qa"},
		{"withdrawn request is nobody's move", func(f *store.SupervisionFact) { f.Labels = []string{"withdrawn"} }, "observe", "withdrawn", "workstream-qa"},
		{"wait on the user with no question recorded", func(f *store.SupervisionFact) { f.Labels = []string{"waiting:1700000000:user"} }, "wake", "record_question", "commander"},
		{"review without progress", func(f *store.SupervisionFact) {
			f.Alive = true
			f.ResultReady = true
			f.AgentState = "working"
			f.ProgressAt = now.Add(-time.Hour).Unix()
		}, "wake", "owner_progress", "commander"},
		{"no outcomes despite activity", func(f *store.SupervisionFact) {
			f.Alive = true
			f.AgentState = "working"
			f.CompletionAt = now.Add(-3 * time.Hour).Unix()
		}, "wake", "completion_check", "commander"},
		{"busy owner on other work", func(f *store.SupervisionFact) { f.Alive = true; f.AgentState = "working" }, "defer", "work_pending", "workstream-qa"},
		{"real user decision", func(f *store.SupervisionFact) { f.Labels = []string{"needs-user"} }, "observe", "user_decision", "workstream-qa"},
		{"ready queued", func(f *store.SupervisionFact) { f.ExecutionReady = true; f.ExecutionCapable = true }, "observe", "execution_queued", "workstream-qa"},
		{"retired intent", func(f *store.SupervisionFact) { f.Labels = []string{"retired"} }, "resolve", "", "workstream-qa"},
		{"planning question", func(f *store.SupervisionFact) {
			f.PendingQuestions = 1
			f.PlanningCommander = "commander-urgent"
			f.PresenceKnown = false
		}, "wake", "planning_question", "commander-urgent"},
		{"question on held work", func(f *store.SupervisionFact) {
			f.PendingQuestions = 1
			f.PlanningCommander = "commander-urgent"
			f.Held = true
			f.ExecutorBound = true
		}, "wake", "planning_question", "commander-urgent"},
		{"question alongside human gate", func(f *store.SupervisionFact) {
			f.PendingQuestions = 1
			f.PlanningCommander = "commander-urgent"
			f.Labels = []string{"needs-user"}
		}, "wake", "planning_question", "commander-urgent"},
		{"saved human answer on held work", func(f *store.SupervisionFact) {
			f.PendingHumanAnswer = true
			f.PlanningCommander = "commander-urgent"
			f.Held = true
			f.Labels = []string{"needs-user"}
		}, "wake", "human_answer_pending", "commander-urgent"},
		{"closed work needs answer receipt", func(f *store.SupervisionFact) {
			f.PendingHumanAnswer = true
			f.PlanningCommander = "commander-urgent"
			f.Status = "closed"
		}, "wake", "human_answer_pending", "commander-urgent"},
		{"waiting for another owner", func(f *store.SupervisionFact) { f.QuestionWaiting = true }, "observe", "planning_answer", "workstream-qa"},
		{"startup reserved", func(f *store.SupervisionFact) {
			f.ExecutionReady = true
			f.ExecutionCapable = true
			f.ExecutionReserved = true
		}, "observe", "execution_starting", "workstream-qa"},
		{"missing execution capability", func(f *store.SupervisionFact) { f.ExecutionReady = true }, "wake", "execution_capability", "commander"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := base
			c.change(&f)
			d := Decide(f, now)
			if d.Action != c.action || d.Reason != c.reason || d.Target != c.target {
				t.Fatalf("decision=%#v", d)
			}
		})
	}
}

func TestIncompleteRequiredRetirementIsReconciledByCurrentCommander(t *testing.T) {
	for _, labels := range [][]string{{"kind:gate", "retired"}, {"workflow-template:pr-review", "superseded"}} {
		fact := store.SupervisionFact{Status: "open", Labels: labels, Held: true, PlanningCommander: "commander-urgent", SupervisorID: "commander", AgentID: "executor-generic"}
		got := Decide(fact, time.Now())
		if got.Action != "wake" || got.Reason != "retirement_pending" || got.Target != "commander-urgent" {
			t.Fatalf("incomplete retirement disappeared or resumed an executor: %+v", got)
		}
		fact.Status = beads.StatusDeferred
		got = Decide(fact, time.Now())
		if got.Action == "wake" || got.Action == "resume" {
			t.Fatalf("intentional parking was treated as abandoned execution: %+v", got)
		}
	}
}

func TestRepeatBackoffSpacesOutUnchangedObligations(t *testing.T) {
	want := []time.Duration{time.Minute, time.Minute, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 4 * time.Hour, 4 * time.Hour}
	for attempts, d := range want {
		if got := repeatBackoff(int64(attempts)); got != d {
			t.Fatalf("attempts %d: %s, want %s", attempts, got, d)
		}
	}
}

func TestInactiveGateObligations(t *testing.T) {
	now := time.Unix(20000, 0)
	for _, inactive := range []bool{false, true} {
		for _, completion := range []bool{false, true} {
			f := store.SupervisionFact{Status: "open", Labels: []string{"kind:gate", "gate-of:stage"}, GateInactive: inactive, AgentID: "owner", SupervisorID: "commander", PresenceKnown: true, Alive: true, ExecutorBound: true, AgentState: "working", ProgressAt: now.Add(-2 * time.Hour).Unix()}
			if completion {
				f.CompletionAt = now.Add(-3 * time.Hour).Unix()
			}
			d := Decide(f, now)
			if inactive {
				if d.Action != "observe" || d.Reason != "gate_inactive" {
					t.Fatalf("inactive gate: %+v", d)
				}
			} else {
				want := "progress_check"
				if completion {
					want = "completion_check"
				}
				if d.Action != "wake" || d.Reason != want {
					t.Fatalf("open stage: %+v want %s", d, want)
				}
			}
		}
	}
}
