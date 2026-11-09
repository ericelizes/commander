package livedata

import "testing"

func TestDeliveryMilestonesIgnoreFailureRecoveryTasks(t *testing.T) {
	stage := SnapStep{BeadID: "ship", Status: "open", Children: []SnapStep{{BeadID: "attempt", Task: true, Status: "closed"}}}
	ws := SnapWorkstream{Steps: []SnapStep{stage, {BeadID: "delivered", Status: "closed"}}}
	want := Progress{Done: 1, Total: 2}
	if got := DeliveryMilestones(ws); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := 0; i < 100; i++ {
		ws.Steps[0].Children = append(ws.Steps[0].Children, SnapStep{Task: true, Status: "closed", OwnerState: "died"})
	}
	if got := DeliveryMilestones(ws); got != want {
		t.Fatalf("failed task history inflated milestones: %+v", got)
	}
	ws.Steps[0].Status = "closed"
	ws.Steps[0].Gates = []SnapGate{{Status: "failed"}}
	if got := DeliveryMilestones(ws); got != want {
		t.Fatalf("failed gate counted: %+v", got)
	}
	ws.Steps[0].Gates[0].Status = "passed"
	if got := DeliveryMilestones(ws); got != (Progress{Done: 2, Total: 2}) {
		t.Fatalf("validated stage not counted: %+v", got)
	}
}

func TestDeliveryMilestonesTracksAndAdministrativeRows(t *testing.T) {
	ws := SnapWorkstream{Steps: []SnapStep{
		{Track: true, Children: []SnapStep{{BeadID: "stage", Status: "open"}, {Task: true, Status: "closed"}}},
		{Gate: true, Status: "closed"}, {Task: true, Status: "closed"}, {Superseded: true, Status: "closed"},
	}}
	if got := DeliveryMilestones(ws); got != (Progress{Total: 1}) {
		t.Fatalf("got %+v", got)
	}
}
