package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestPausedWorkIsUnfinishedForProgressAndConfirmDone(t *testing.T) {
	for _, nested := range []bool{false, true} {
		paused := st("commander-mtq.6", func(s *SnapStep) { s.Status = beads.StatusDeferred; s.State = beads.StateDone })
		steps := []SnapStep{st("done", closedStep), paused, st("confirm", gateOf("Confirm done", "user"), blockedBy(paused.BeadID, "done"))}
		if nested {
			steps = []SnapStep{st("commander-fngn.1.1", asTrack, kids(steps...))}
		}
		ws := SnapWorkstream{Steps: steps}
		if p := progressOf(ws); p.Done != 1 || p.Total != 2 {
			t.Fatalf("leaf progress: %+v", p)
		}
		if p := DeliveryMilestones(ws); p.Done != 1 || p.Total != 2 {
			t.Fatalf("delivery progress: %+v", p)
		}
		if g := gateStatusOf(ws); g == nil || g.at {
			t.Fatalf("Confirm done ready with paused work: %+v", g)
		}
	}
}

func TestPausedWorkBlocksImplicitConfirmDone(t *testing.T) {
	paused := st("paused-area", asTrack, func(s *SnapStep) { s.Status = beads.StatusDeferred }, kids(st("finished-child", closedStep)))
	confirm := st("confirm", gateOf("Confirm done", "user"), func(s *SnapStep) { s.ClosingGate = true })
	steps := []SnapStep{st("finished", closedStep), paused, confirm}
	if readyClosingGate(steps) != "" {
		t.Fatal("paused area made Complete ready")
	}
	if g := gateStatusOf(SnapWorkstream{Steps: steps}); g == nil || g.at {
		t.Fatalf("implicit Confirm done ready: %+v", g)
	}
}
