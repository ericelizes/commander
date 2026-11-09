package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

// An open scope born with nothing in it reads by its holder, never "Nothing to do": with no plan
// wait (one made before atomic birth) planning is still the Manager's move (main's "Not started:
// Manager to plan"); with the wait, "Manager planning".
func TestEmptyScopeNeverReadsNothingToDo(t *testing.T) {
	r := boardWS(func(*SnapWorkstream) {})
	expectLine(t, r, "idle", notPlannedText)

	r = boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitSince, w.WaitUntil, w.WaitUntilAs = beads.ManagerPlanWait, caseNow-60, caseNow+120, beads.ManagerPlanWait
	})
	expectLine(t, r, "waiting", "Manager planning")

	// The handoff window passing does not turn the plan wait into nothing: the Manager still owes the plan.
	r = boardWS(func(w *SnapWorkstream) {
		w.Waiting, w.WaitSince, w.WaitUntil, w.WaitUntilAs = beads.ManagerPlanWait, caseNow-600, caseNow-300, beads.ManagerPlanWait
	})
	expectLine(t, r, "waiting", "Manager planning")
}

// A scope whose work is all finished is a stage to close, not a birth without a holder.
func TestFinishedScopeKeepsNothingToDo(t *testing.T) {
	r := boardWS(func(*SnapWorkstream) {}, st("a", closedStep))
	expectLine(t, r, "quiet", "Nothing to do")
}
