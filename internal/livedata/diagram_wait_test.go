package livedata

import (
	"slices"
	"testing"
)

func TestLevelAfterOrdersAStepAfterTheOperatorItWaitsOn(t *testing.T) {
	steps := []SnapStep{
		{BeadID: "w.1", Title: "Grafana reworked", Children: []SnapStep{{BeadID: "w.1.2", Title: "CI green on the reworked head", Waiting: "operator-op-fix-1"}}},
		{BeadID: "w.2", Title: "Review findings fixed", Owner: "operator-op-fix-1"},
	}
	_, _, after := levelAfter(steps)
	if !slices.Equal(after["w.1"], []string{"w.2"}) {
		t.Fatalf("after = %v, want w.1 after w.2", after)
	}
	if got := workOrder(steps, after); got[0].BeadID != "w.2" {
		t.Fatalf("order = %s, %s; want the worked step first", got[0].BeadID, got[1].BeadID)
	}
}
