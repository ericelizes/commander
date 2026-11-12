package livedata

import (
	"strings"
	"time"

	"github.com/example-user/commander/internal/nextactor"
	"github.com/example-user/commander/internal/store"
)

// withdrawnLabel marks a cancelled request: never anyone's move.
const withdrawnLabel = "withdrawn"

// actorRow is a workstream's (or track's) next-actor state: every open bead
// in it decided by nextactor, rolled up. It is remembered on the board so the
// row, its tracks and the descriptions read the same decision.
func (b board) actorRow(ws SnapWorkstream) nextactor.Row {
	if b.actors == nil {
		b.actors = map[string]nextactor.Row{}
	}
	key := ws.Project + ":" + ws.BeadID
	if row, ok := b.actors[key]; ok {
		return row
	}
	ctx := b.actorContext(ws.Project, ws.ProjectID, ws.PlanningCommander)
	root := nextactor.Decide(b.actorBead(ws.Project, ws.ProjectID, ws.PlanningCommander, rootStep(ws)), ctx)
	results := []nextactor.Result{root}
	if root.Actor != nextactor.None || root.Reason == "children" {
		var walk func([]SnapStep)
		walk = func(steps []SnapStep) {
			for _, s := range steps {
				if s.Superseded || (stepClosed(s) && !s.HumanAnswerPending) {
					continue
				}
				d := nextactor.Decide(b.actorBead(ws.Project, ws.ProjectID, ws.PlanningCommander, s), ctx)
				results = append(results, d)
				if d.Actor == nextactor.None && d.Reason != "children" {
					continue // paused or withdrawn: its steps are out of play
				}
				walk(s.Children)
			}
		}
		walk(ws.Steps)
	}
	row := nextactor.Rollup(results)
	b.actors[key] = row
	return row
}

// actorLine is a row's line from its next-actor state.
func actorLine(row nextactor.Row) line {
	kind := map[nextactor.Actor]string{
		nextactor.User:      "needs",
		nextactor.Stuck:     "stuck",
		nextactor.Agent:     "working",
		nextactor.Commander: "working",
		nextactor.External:  "waiting",
		nextactor.None:      "quiet",
	}[row.Lead.Actor]
	// "why" draws a warning icon in the sidebar; a blocking chain is not a
	// warning (it stays on the row's Actor for anyone who wants it).
	return line{kind: kind, text: row.Summary(), since: row.Lead.Since}
}

func (b board) actorContext(project string, pid int64, commander string) nextactor.Context {
	if b.actorMemo == nil {
		b.actorMemo = map[string]map[string]nextactor.Result{}
	}
	memo := b.actorMemo[project]
	if memo == nil {
		memo = map[string]nextactor.Result{}
		b.actorMemo[project] = memo
	}
	return nextactor.Context{
		Now:  time.Unix(b.now, 0),
		Memo: memo,
		Commander: func(id string) (bool, int64) {
			a, ok := b.s.Agents[id]
			if !ok {
				return false, 0
			}
			if a.State == store.AgentWorking {
				return true, 0
			}
			return false, a.Since
		},
		Lookup: func(id string) (nextactor.Bead, bool) {
			s, ok := b.projectSteps[project][id]
			if !ok {
				return nextactor.Bead{}, false
			}
			return b.actorBead(project, pid, commander, s), true
		},
	}
}

// actorBead is the nextactor view of one step: only recorded facts.
func (b board) actorBead(project string, pid int64, commander string, s SnapStep) nextactor.Bead {
	n := nextactor.Bead{
		ID: s.BeadID, Title: s.Title, Status: s.Status, Superseded: s.Superseded, Withdrawn: s.Withdrawn,
		Held: s.Hold, HoldReason: s.HoldReason, HumanAnswerPending: s.HumanAnswerPending, QuestionWaiting: s.QuestionWaiting,
		Waits: s.TypedWaits, ProgressAt: s.ProgressAt, ResultReady: s.ResultReadyAt > 0, ResultSince: s.ResultReadyAt,
		PlanningPending: s.PlanningPending, PlanningCommander: s.PlanningCommander,
	}
	if n.PlanningCommander == "" {
		n.PlanningCommander = commander
	}
	if cause := b.s.PlanningUsage[n.PlanningCommander]; s.PlanningPending && cause != "" {
		n.PlanningUsage = store.UsageCapacityError + cause
	}
	if s.ExecutionReady && s.ExecutionEligible && s.ExecutionMode == "active" && strings.HasPrefix(s.ExecutionError, store.UsageCapacityError) {
		n.UsageCapacity = s.ExecutionError
	}
	if b.needsIDs[s.BeadID] {
		n.NeedsUser, n.UserAsk = true, b.askText(project, s)
	} else if s.Decision != nil && strings.TrimSpace(s.Decision.Question) != "" {
		n.UserAsk = s.Decision.Question
	} else if strings.TrimSpace(s.Ask) != "" {
		n.UserAsk = s.Ask
	}
	for _, id := range s.BlockedBy {
		if dep, ok := b.projectSteps[project][id]; ok && !dependencyComplete(dep) && id != s.BeadID {
			n.BlockedBy = append(n.BlockedBy, id)
		}
	}
	for _, c := range s.Children {
		if !stepClosed(c) && !c.Superseded {
			n.HasOpenChildren = true
			n.Children = append(n.Children, c.BeadID)
		}
	}
	for _, run := range b.s.Executions {
		if run.ProjectID == pid && run.BeadID == s.BeadID {
			n.Executing, n.ExecRole, n.ExecWorking = true, run.Role, run.State == store.AgentWorking
			break
		}
	}
	if !n.Executing {
		for _, run := range b.s.ExecutionChecks {
			if run.ProjectID == pid && run.BeadID == s.BeadID {
				n.ExecRole = run.Role
				if run.StartupAttempts >= 5 && run.StartupError != "" {
					n.LaunchFailed, n.LaunchError = true, run.StartupError
				} else {
					// Claiming a bead restarts its progress clock: that is when it began.
					n.Unverified, n.UnverifiedSince = true, s.ProgressAt
				}
				break
			}
		}
	}
	if s.ExecutionAttempts >= 5 && s.ExecutionError != "" {
		n.LaunchFailed, n.LaunchError = true, s.ExecutionError
	}
	if s.OwnerState == OwnerDied && !n.Executing && s.ResultReadyAt == 0 {
		n.ExitedWithoutReport, n.ExitedAt = true, s.OwnerSince
	}
	if s.ExecutionReady && !n.Executing && !n.Unverified && !n.LaunchFailed {
		n.ReadyQueued, n.NoCapableSlot = true, !s.ExecutionCapable
	}
	return n
}

// askText is what the user is asked on s, from its card when there is one.
func (b board) askText(project string, s SnapStep) string {
	for _, a := range b.s.NeedsYou {
		if a.Project == project && a.BeadID == s.BeadID {
			if q := a.decisionQuestion(); q != "" {
				return q
			}
			if a.Ask != "" {
				return a.Ask
			}
			return a.Title
		}
	}
	if s.Ask != "" {
		return s.Ask
	}
	return s.Title
}

// rootStep views a workstream's root bead as a step.
func rootStep(ws SnapWorkstream) SnapStep {
	return SnapStep{
		BeadID: ws.BeadID, Title: ws.Title, Status: ws.Status, Hold: ws.Hold, HoldReason: ws.HoldReason, Withdrawn: ws.Withdrawn,
		ProgressAt: ws.ProgressAt, TypedWaits: ws.TypedWaits, BlockedBy: ws.BlockedBy, HumanAnswerPending: ws.HumanAnswerPending,
		QuestionWaiting: ws.QuestionWaiting, ResultRunID: ws.ResultRunID, ResultReadyAt: ws.ResultReadyAt,
		ExecutionMode: ws.ExecutionMode, ExecutionEligible: ws.ExecutionEligible, ExecutionReady: ws.ExecutionReady, ExecutionCapable: ws.ExecutionCapable, ExecutionReserved: ws.ExecutionReserved,
		ExecutionAttempts: ws.ExecutionAttempts, ExecutionError: ws.ExecutionError, PlanningPending: ws.PlanningPending,
		PlanningCommander: ws.PlanningCommander, Children: ws.Steps,
	}
}
