package livedata

import (
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Active orchestration has planning obligations and run-bound executors. A
// missing permanent Manager is not missing state; a chat turn is not execution.
func (b board) activeWorkstreamLine(ws SnapWorkstream, asks int) line {
	if ws.Status == "closed" {
		return line{kind: "quiet", text: "Complete"}
	}
	// Reuse the existing approval projection, never its runtime/Manager inference.
	if asks > 0 {
		human := b.workstreamBaseLine(ws, SnapAgent{State: store.AgentIdle}, true, asks)
		if human.kind == "needs" {
			return human
		}
	}
	decisions, _ := b.heldByLive(userDecisions(ws, b.needsIDs))
	if text, ready := boardNeedsYou(ws, decisions, b.s.NeedsYou); len(ready) > 0 {
		return line{kind: "needs", text: text, since: waitSince(ready, func(w string) bool { return w == "you" })}
	}
	for _, ask := range b.s.NeedsYou {
		if ask.Project == ws.Project && ask.BeadID == ws.BeadID {
			text := ask.decisionQuestion()
			if text == "" {
				text = ask.Title
			}
			return line{kind: "needs", text: "Needs you · " + text}
		}
	}
	if question, ok := planningQuestionLine(ws); ok {
		return question
	}
	if ws.Hold || ws.Status == beads.StatusDeferred {
		return line{kind: "waiting", text: "Paused by recorded direction", why: ws.HoldReason}
	}
	if executionCount(b.s, ws.ProjectID, ws.BeadID, ws.Steps) > 0 {
		return line{kind: "working", text: workingText(b.liveSteps(ws))}
	}
	// An unknown claim remains a concrete custody check, not an idle planner.
	for _, run := range append(append([]SnapExecution{}, b.s.ExecutionChecks...), b.s.Executions...) {
		if run.ProjectID == ws.ProjectID && executionScopeContains(ws.BeadID, ws.Steps, run.BeadID) {
			return line{kind: "waiting", text: waitingExecutionText(b.s, ws.ProjectID, ws.BeadID, ws.Steps)}
		}
	}
	if result, ok := recordedResultLine(ws); ok {
		return result
	}
	if required, ok := b.requiredCheckLine(ws); ok {
		return required
	}
	if queued, ok := executionQueueLine(ws); ok {
		return queued
	}

	// Typed external waits keep their real deadlines. Legacy Manager/Operator
	// custody receipts instead require the current planning Commander to act.
	roots := []SnapStep{{BeadID: ws.BeadID, Status: ws.Status, PlanningPending: ws.PlanningPending, Waiting: ws.Waiting, WaitSince: ws.WaitSince, WaitUntil: ws.WaitUntil, WaitUntilAs: ws.WaitUntilAs, WaitUntils: ws.WaitUntils, BlockedBy: ws.BlockedBy}}
	for step := range stepValues(ws.Steps) {
		if !stepClosed(step) && !parked(step) && !step.Superseded {
			roots = append(roots, step)
		}
	}
	waits := []string{}
	waitBeads := []SnapStep{}
	handoff := false
	planning := false
	pitch := false
	for _, step := range roots {
		planning = planning || step.PlanningPending
		if _, ok := b.noPitch[ws.Project+"\x00"+step.BeadID]; ok {
			pitch = true
		}
		for _, wait := range strings.Split(step.Waiting, " · ") {
			if wait == "" || wait == "you" || wait == heldElsewhere || step.waitExpired(b.now, wait) {
				continue
			}
			if wait == beads.ManagerPlanWait || handoffText(wait) {
				handoff = true
				continue
			}
			if strings.HasPrefix(wait, "operator") {
				continue
			}
			waits = append(waits, wait)
			waitBeads = append(waitBeads, step)
		}
		if step.ReviewFact != nil && step.ReviewFact.Acted {
			handoff = true
		}
	}
	if wait, ok := b.waitLine(waits, waitBeads); ok {
		return wait
	}
	for _, step := range roots {
		for _, id := range step.BlockedBy {
			if blocker, ok := b.projectSteps[ws.Project][id]; ok && !dependencyComplete(blocker) {
				return line{kind: "waiting", text: "Waiting on " + liveTitle(blocker.Title)}
			}
		}
	}
	if planning {
		if pitch {
			return line{kind: "waiting", text: "Commander to prepare the decision"}
		}
		if handoff {
			return line{kind: "waiting", text: "Commander to reconcile the recorded handoff"}
		}
		return line{kind: "waiting", text: "Commander to plan the next action"}
	}
	return line{kind: "stalled", text: "Assignment needs reconciliation"}
}
