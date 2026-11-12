package beads

import (
	"github.com/example-user/commander/internal/holding"
	"slices"
	"strings"
	"time"
)

func DeriveHolding(hb *holding.Bead) {
	state, owner := StepState(hb.Status, hb.Labels)
	hb.Gate = slices.Contains(hb.Labels, GateLabel) || slices.Contains(hb.Labels, "kind:gate")
	hb.NeedsUser = state == StateNeedsUser
	hb.HumanHold = slices.Contains(hb.Labels, "hold")
	hb.Superseded = slices.Contains(hb.Labels, "superseded") || slices.Contains(hb.Labels, "retired")
	hb.Owner, hb.Waits, hb.OperatorWaits, hb.Untyped, hb.Until = owner, 0, nil, "", false
	now := time.Now()
	for _, w := range WaitsOf(hb.Labels) {
		if w.Kind == WaitUser {
			hb.HumanHold = true
		}
		if !WaitHolds(w, now) {
			continue // an overdue handoff: the Manager's move, held by nothing
		}
		if w.Kind == WaitOperator {
			hb.OperatorWaits = append(hb.OperatorWaits, "operator-"+strings.TrimPrefix(w.Ref, "operator-"))
			continue
		}
		hb.Waits++
		if w.Untyped() && hb.Untyped == "" {
			hb.Untyped = w.Ref
		}
	}
	for _, l := range hb.Labels {
		if strings.HasPrefix(l, "until:") {
			hb.Until = true
		}
	}
}
