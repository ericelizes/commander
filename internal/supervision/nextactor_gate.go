package supervision

import (
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/nextactor"
	"github.com/example-user/commander/internal/store"
)

// actorOf is the shared next-actor decision (the same the sidebar shows) for a
// supervised bead. A parent whose children carry its state takes the most
// urgent decision among the children no job of their own supervises.
func actorOf(f store.SupervisionFact, now time.Time) (nextactor.Result, bool) {
	ctx := nextactor.Context{Now: now, Commander: func(string) (bool, int64) {
		return f.CommanderState == store.AgentWorking, f.CommanderSince
	}}
	own := nextactor.Decide(beadOf(f), ctx)
	if own.Actor != nextactor.None || own.Reason != "children" {
		return own, false
	}
	var results []nextactor.Result
	for _, c := range f.Children {
		if supervisedOnItsOwn(c.Labels) {
			continue
		}
		r := nextactor.Decide(childBeadOf(c, f), ctx)
		// The row may lead with the user, but a move an agent owes on another
		// child still has to be delivered: supervision acts on it.
		if !observedActor(r.Actor) {
			return r, true
		}
		results = append(results, r)
	}
	if len(results) == 0 {
		return own, false
	}
	return nextactor.Rollup(results).Lead, true
}

// observedActor reports whether the decision leaves the move with someone
// supervision must not nudge: the user, someone external, nobody, or an
// agent already carrying it.
func observedActor(a nextactor.Actor) bool {
	return a == nextactor.User || a == nextactor.External || a == nextactor.None || a == nextactor.Agent
}

// supervisionReason names an observed decision in supervision's vocabulary.
func supervisionReason(r nextactor.Result, fromChildren bool) string {
	if fromChildren {
		return "subordinates_waiting"
	}
	reason := strings.TrimPrefix(r.Reason, "blocked:")
	switch {
	case strings.HasPrefix(r.Reason, "blocked:") || r.Reason == "dependency" || strings.HasPrefix(reason, "wait:"):
		return "dependency"
	case reason == "paused":
		return "held"
	case reason == "decision" || reason == "waiting_user":
		return "user_decision"
	case reason == "executing" || reason == "waiting_agent":
		return "working"
	case reason == "queued":
		return "execution_queued"
	case reason == "starting":
		return "execution_starting"
	case reason == "children" || reason == "empty":
		return "subordinates"
	}
	return reason
}

func beadOf(f store.SupervisionFact) nextactor.Bead {
	state, _ := beads.StepState(f.Status, f.Labels)
	result := f.ResultReady || state == beads.StateReview || managerHandoff(f.Labels)
	b := nextactor.Bead{
		ID: f.BeadID, Title: f.Title, Status: f.Status,
		Superseded: has(f.Labels, "superseded") || has(f.Labels, "retired"), Withdrawn: has(f.Labels, "withdrawn"),
		Held: f.Held, NeedsUser: state == beads.StateNeedsUser || has(f.Labels, beads.GateLabel) && !beads.IsClosingGate(f.Labels),
		ClosingGate:        beads.IsClosingGate(f.Labels),
		HumanAnswerPending: f.PendingHumanAnswer, PendingQuestions: f.PendingQuestions, QuestionWaiting: f.QuestionWaiting,
		Waits: beads.WaitsOf(f.Labels), HasOpenChildren: f.ChildCount > 0, ProgressAt: f.ProgressAt,
		PlanningCommander: f.PlanningCommander,
	}
	if f.OpenBlockers > 0 {
		b.BlockedBy = []string{"open-blocker"} // supervision does not follow chains: the blocker has its own job
	}
	switch {
	case f.ExecutorBound && f.Alive:
		b.Executing, b.ExecRole = true, f.AgentID
		b.ExecWorking = f.AgentState == store.AgentWorking || f.AgentState == store.AgentWaiting
	case f.ExecutorBound && f.PresenceKnown && !result:
		b.ExitedWithoutReport, b.ExecRole = true, f.AgentID
	}
	if f.ExecutionError != "" && f.ExecutionAttempts >= 5 {
		b.LaunchFailed, b.LaunchError = true, f.ExecutionError
	}
	if f.ExecutionReady && !b.Executing && !b.LaunchFailed {
		if f.ExecutionReserved {
			b.Unverified, b.UnverifiedSince = true, f.ProgressAt
		} else {
			b.ReadyQueued, b.NoCapableSlot = true, !f.ExecutionCapable
		}
	}
	if result {
		b.ResultReady, b.ResultSince = true, f.ProgressAt
	}
	return b
}

func childBeadOf(c store.SupervisionChild, parent store.SupervisionFact) nextactor.Bead {
	state, _ := beads.StepState(c.Status, c.Labels)
	b := nextactor.Bead{
		ID: c.ID, Status: c.Status, Held: has(c.Labels, "hold"), Withdrawn: has(c.Labels, "withdrawn"),
		Superseded:      has(c.Labels, "superseded") || has(c.Labels, "retired"),
		NeedsUser:       state == beads.StateNeedsUser || has(c.Labels, beads.GateLabel),
		QuestionWaiting: c.QuestionWaiting, Waits: beads.WaitsOf(c.Labels), HasOpenChildren: c.HasOpenChildren,
		PlanningCommander: parent.PlanningCommander,
	}
	if c.OpenBlockers > 0 {
		b.BlockedBy = []string{"open-blocker"}
	}
	if c.Running {
		b.Executing, b.ExecWorking = true, true // its own run is supervised by its own facts
	} else if c.ExecutionReady {
		b.ReadyQueued = true
	}
	return b
}

// supervisedOnItsOwn: beads with a kind label get their own supervision job.
func supervisedOnItsOwn(labels []string) bool {
	for _, kind := range []string{"kind:objective", "kind:task", "kind:workstream", "kind:gate"} {
		if has(labels, kind) {
			return true
		}
	}
	return false
}
