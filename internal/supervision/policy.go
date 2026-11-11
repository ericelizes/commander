// Package supervision decides whether responsibility is being carried out.
// It does not approve work, fabricate progress, or execute a task itself.
package supervision

import (
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type Decision struct {
	Reason, Action, Target, Description string
	CheckAfter                          time.Duration
}

// Decide is deterministic. Runtime presence, activity and progress are separate
// facts; a live process alone neither proves work nor warrants an interruption.
func Decide(f store.SupervisionFact, now time.Time) Decision {
	d := Decision{Action: "observe", Target: f.AgentID, CheckAfter: time.Minute}
	if f.PendingHumanAnswer && !f.Deleted {
		d.Reason = "human_answer_pending"
		d.Description = "Apply or reconcile the saved human answer; do not ask again or broaden its authority"
		d.Action = "wake"
		d.Target = f.PlanningCommander
		if d.Target == "" {
			d.Target = f.SupervisorID
		}
		d.CheckAfter = 15 * time.Second
		return d
	}
	if !f.Deleted && f.Status != "closed" && f.Status != beads.StatusDeferred && (has(f.Labels, "retired") || has(f.Labels, "superseded")) {
		required := has(f.Labels, "kind:gate") || has(f.Labels, beads.GateLabel)
		for _, label := range f.Labels {
			if strings.HasPrefix(label, "workflow-template:") || strings.HasPrefix(label, "workflow-step:") {
				required = true
			}
		}
		if required {
			d.Reason = "retirement_pending"
			d.Description = "Reconcile the incomplete retirement against recorded authority; preserve holds and approvals, finish an authorized transition or restore the visible requirement without inventing a passed check"
			d.Action = "wake"
			d.Target = f.PlanningCommander
			if d.Target == "" {
				d.Target = f.SupervisorID
			}
			if d.Target == "" {
				d.Target = "commander"
			}
			d.CheckAfter = 15 * time.Second
			return d
		}
	}
	if f.Status == "closed" || f.Deleted || has(f.Labels, "superseded") || has(f.Labels, "retired") {
		d.Action = "resolve"
		return d
	}
	if f.PendingQuestions > 0 {
		d.Reason = "planning_question"
		d.Description = "Answer the recorded planning question; read cmdr question inbox and preserve all holds and human approvals"
		d.Action = "wake"
		d.Target = f.PlanningCommander
		if d.Target == "" {
			d.Target = f.SupervisorID
		}
		d.CheckAfter = 15 * time.Second
		return d
	}
	if f.Held || f.Status == beads.StatusDeferred {
		d.Reason = "held"
		d.Description = "Paused by recorded direction"
		return d
	}
	state, _ := beads.StepState(f.Status, f.Labels)
	if state == beads.StateNeedsUser || has(f.Labels, beads.GateLabel) {
		d.Reason = "user_decision"
		d.Description = "Waiting for the recorded decision"
		return d
	}
	if f.QuestionWaiting {
		d.Reason = "planning_answer"
		d.Description = "Waiting for the affected Commander decisions"
		return d
	}
	// Typed external waits remain legitimate. Manager custody receipts are work
	// owed by an agent, never a dependency that can excuse an idle owner.
	external := false
	for _, w := range beads.WaitsOf(f.Labels) {
		if !beads.IsManagerHandoff(w) && beads.WaitHolds(w, now) {
			external = true
		}
	}
	if f.OpenBlockers > 0 || external {
		d.Reason = "dependency"
		d.Description = "Waiting on a recorded dependency"
		return d
	}
	if f.ExecutionReady {
		d.CheckAfter = 5 * time.Second
		owner := f.PlanningCommander
		if owner == "" {
			owner = f.SupervisorID
		}
		if f.ExecutionError != "" && f.ExecutionAttempts >= 5 {
			d.Reason = "execution_launch_blocked"
			d.Description = "Investigate the recorded execution launch failure"
			d.Action = "wake"
			d.Target = owner
			return d
		}
		if !f.ExecutionCapable {
			d.Reason = "execution_capability"
			d.Description = "Resolve the required execution capability"
			d.Action = "wake"
			d.Target = owner
			return d
		}
		if f.ExecutionReserved {
			d.Reason = "execution_starting"
			d.Description = "An execution slot is preparing the work"
		} else {
			d.Reason = "execution_queued"
			d.Description = "Queued for available execution capacity"
		}
		return d
	}
	// A failed native start retains custody; exhaustion is a repair obligation
	// even when no process observation ever became available. Do not release or
	// duplicate an execution merely because its local startup failed.
	if f.ExecutorBound && !f.Alive && f.ExecutionAttempts >= 5 && f.ExecutionError != "" {
		d.Reason = "execution_launch_blocked"
		d.Description = "Investigate the recorded execution startup failure; retain custody until it is reconciled"
		d.Action = "wake"
		d.Target = f.PlanningCommander
		if d.Target == "" {
			d.Target = f.SupervisorID
		}
		if d.Target == "" {
			d.Target = "commander"
		}
		d.CheckAfter = time.Minute
		return d
	}
	if !f.PresenceKnown {
		d.Reason = "runtime_unknown"
		d.Description = "Checking agent availability"
		d.CheckAfter = 5 * time.Second
		return d
	}
	result := f.ResultReady || state == beads.StateReview || managerHandoff(f.Labels)
	age := now.Unix() - f.ProgressAt
	if f.CompletionAt > 0 && now.Unix()-f.CompletionAt >= 2*60*60 {
		d.Reason = "completion_check"
		d.Description = "Investigate work without a completed outcome"
		if f.Alive && f.AgentState == store.AgentWorking {
			d.Target = f.SupervisorID
		}
		if d.Target == "" {
			d.Target = "commander"
		}
		d.Action = "wake"
		return d
	}
	if f.ChildCount > 0 && !result {
		if age < 20*60 {
			d.Reason = "subordinates"
			d.Description = "Subordinates are carrying out the work"
			return d
		}
		d.Reason = "subordinate_progress"
		d.Description = "Investigate subordinate progress"
	} else if result {
		d.Reason = "result_pending"
		d.Description = "Review and accept the recorded result"
	} else if f.ExecutorBound && f.Alive && (f.AgentState == store.AgentWorking || f.AgentState == store.AgentWaiting) {
		if age < 20*60 {
			d.Reason = "working"
			d.Description = "Agent is working"
			return d
		}
		d.Reason = "progress_check"
		d.Description = "Investigate work without a recorded advance"
		d.Target = f.SupervisorID
	} else {
		d.Reason = "work_pending"
		d.Description = "Carry out the next actionable step"
	}
	if d.Target == "" {
		d.Target = f.SupervisorID
	}
	if d.Target == "" {
		d.Target = "commander"
	}
	// A busy owner receives the obligation at its next safe turn, not an
	// interrupt in the middle of a tool. Persist it so idle transitions retry it.
	if f.Alive && d.Target == f.AgentID && (f.AgentState == store.AgentWorking || f.AgentState == store.AgentWaiting) {
		if age >= 20*60 && f.SupervisorID != "" && f.SupervisorID != f.AgentID {
			d.Target = f.SupervisorID
			d.Reason = "owner_progress"
			d.Action = "wake"
			return d
		}
		d.Action = "defer"
		d.CheckAfter = 5 * time.Second
		return d
	}
	if !f.Alive && f.ExecutorBound && d.Target == f.AgentID {
		d.Action = "resume"
	} else {
		d.Action = "wake"
	}
	d.CheckAfter = 15 * time.Second
	return d
}
func has(ls []string, want string) bool {
	for _, l := range ls {
		if l == want {
			return true
		}
	}
	return false
}
func managerHandoff(labels []string) bool {
	for _, w := range beads.WaitsOf(labels) {
		if beads.IsManagerHandoff(w) {
			return true
		}
	}
	return false
}
