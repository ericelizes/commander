package livedata

import (
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

const managerPlanningText = "Manager planning"

const resultReviewText = "Result ready for Manager review"
const approvalDispatchText = "Approved; Manager dispatch pending"
const approvalDispatchOverdueText = "Approved work not started; Manager recovery needed"
const startPendingText = "Manager to start the next step"
const startOverdueText = "Next step not started; Manager recovery needed"

// handoffs are the Manager custody receipts (beads.IsManagerHandoff) and what
// a row reads while each is pending and once it is overdue, in the order a
// row prefers them at equal urgency.
var handoffs = []struct{ ref, pending, overdue string }{
	{beads.ManagerDispatchWait, approvalDispatchText, approvalDispatchOverdueText},
	{beads.ManagerStartWait, startPendingText, startOverdueText},
	{beads.ManagerCloseWait, resultReviewText, "Manager review overdue"},
	{beads.ManagerPlanWait, managerPlanningText, "Manager planning deadline missed"},
	{beads.ManagerRecoverWait, "Operator recovery required", "Operator recovery deadline missed"},
}

// handoffText: text is a line resultHandoff writes, or a wait line naming a receipt.
func handoffText(text string) bool {
	for _, h := range handoffs {
		if text == h.pending || text == h.overdue || strings.Contains(text, h.ref) {
			return true
		}
	}
	return false
}

// startText: text is the line of a "Manager to start" receipt.
func startText(text string) bool {
	return text == startPendingText || text == startOverdueText || strings.Contains(text, beads.ManagerStartWait)
}

// holdsHandoff: s records a Manager custody receipt.
func holdsHandoff(s SnapStep) bool {
	return slices.ContainsFunc(handoffs, func(h struct{ ref, pending, overdue string }) bool { return recordsHandoff(s, h.ref) })
}

func recordsHandoff(s SnapStep, ref string) bool {
	return strings.Contains(" · "+s.Waiting+" · ", " · "+ref+" · ")
}

// resultHandoff projects recorded Manager custody, never an active Operator or user gate.
// A "Manager to start" receipt on a step now behind an open blocker (a gate
// linked after it was made) reads as nothing: the step is not ready, so it is
// nobody's move yet.
func resultHandoff(steps []SnapStep, now int64) (line, bool) {
	ws := SnapWorkstream{Steps: steps}
	if workingOperators(ws) > 0 {
		return line{}, false
	}
	var best line
	found := false
	var walk func([]SnapStep)
	walk = func(list []SnapStep) {
		for _, s := range list {
			if stepClosed(s) || parked(s) || s.Superseded {
				continue
			}
			for _, h := range handoffs {
				if !recordsHandoff(s, h.ref) {
					continue
				}
				if h.ref == beads.ManagerStartWait && OpenBlockerOf(ws, s.BeadID) != "" {
					continue
				}
				candidate := line{kind: "waiting", text: h.pending, since: s.WaitSince, handoffTarget: s.BeadID, handoffRef: h.ref}
				if h.ref == beads.ManagerRecoverWait {
					candidate.kind = "stalled" // a stopped run is a failure, even during its repair window
				}
				if s.waitExpired(now, h.ref) {
					candidate.kind = "stalled"
					candidate.text = h.overdue
				}
				if !found || (candidate.kind == "stalled" && best.kind != "stalled") || (candidate.kind == best.kind && candidate.since > 0 && (best.since == 0 || candidate.since < best.since)) {
					best = candidate
					found = true
				}
			}
			walk(s.Children)
		}
	}
	walk(steps)
	return best, found
}
