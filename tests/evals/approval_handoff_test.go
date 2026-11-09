package evals

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

func TestApprovalHandoffScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/approval-handoff.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Cases []struct {
			Name, Want, Kind string
			Now              int64
		}
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			s := livedata.Snapshot{Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}}, Workstreams: []livedata.SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Repair dev3", Status: "open", ManagerRole: "manager-w", Steps: []livedata.SnapStep{
				{BeadID: "gate", Title: "Approved repair", Status: "closed", State: "done", Gate: true},
				{BeadID: "repair", Title: "Repair dev3", Status: "open", State: "not-started", Owner: "manager-w", BlockedBy: []string{"gate"}, Waiting: beads.ManagerDispatchWait, WaitSince: 90, WaitUntil: 200, WaitUntilAs: beads.ManagerDispatchWait},
			}}}}
			if holes := livedata.SnapshotViolations(s); len(holes) != 0 {
				t.Fatal(holes)
			}
			row := livedata.Board(s, tc.Now).Workstreams[0]
			if row.Text != tc.Want || row.Kind != tc.Kind || row.Working != 0 || row.Flag != 0 {
				t.Fatalf("row %+v", row)
			}
			if row.Diagram == nil {
				t.Fatal("missing diagram")
			}
			found := false
			for _, step := range row.Diagram.Steps {
				if step.ID == "repair" {
					found = true
					if step.Text != tc.Want || step.Actor != livedata.ActorManager {
						t.Fatalf("diagram %+v", step)
					}
				}
			}
			if !found {
				t.Fatal("repair missing from diagram")
			}
		})
	}
}
