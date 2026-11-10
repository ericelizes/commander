package livedata

import (
	"github.com/example-user/commander/internal/beads"
	"strings"
)

// recoveryLine keeps automation failures red until evidence changes. A Manager's
// turn or unread mail is not evidence that the failed handoff has recovered.
// This is a projection only: dispatch/settle retain their current-holder guards.
func recoveryLine(l line, ws SnapWorkstream) line {
	if l.kind != "stalled" && l.kind != "stuck" {
		return l
	}
	owner := ws.ManagerRole
	if strings.Contains(l.text, "Commander's move") {
		owner = "commander"
	}
	if owner == "" {
		owner = "commander"
	}
	target := ws.BeadID
	action := "inspect current evidence and record the next guarded recovery action"
	for s := range stepValues(ws.Steps) {
		if stepClosed(s) || parked(s) || s.Superseded {
			continue
		}
		if holdsHandoff(s) || s.Broken || s.OwnerState == OwnerDied || s.OwnerState == OwnerFinished {
			if s.BeadID != "" {
				target = s.BeadID
			}
			if recordsHandoff(s, beads.ManagerCloseWait) {
				action = "review the existing report and close or return the existing task"
			} else if recordsHandoff(s, beads.ManagerRecoverWait) {
				action = "inspect the stopped run and current holder, then resume or replan through guarded dispatch"
			} else if recordsHandoff(s, beads.ManagerPlanWait) {
				action = "record the missing plan and its actual next holder"
			} else if holdsHandoff(s) {
				action = "dispatch the existing ready work through the guarded dispatch door"
			} else if s.OwnerState == OwnerDied || s.OwnerState == OwnerFinished {
				action = "inspect the stopped run and current holder, then resume or replan through guarded dispatch"
			}
			break
		}
	}
	cause := l.text
	if cause == "Stalled" {
		cause = "no live worker or recorded next action"
	}
	if cause == "State missing" {
		cause = "open work has no valid holder"
	}
	if cause == "Stuck" {
		cause = "Manager cannot accept its queued work"
	}
	detail := "Automation failure: " + cause + ". Owner: " + owner + ". Next: " + action + " on " + target + ". Commander owns escalation if recovery fails."
	if l.why != "" && !strings.Contains(l.why, "Automation failure:") {
		detail = l.why + " · " + detail
	}
	l.why = detail
	return l
}
