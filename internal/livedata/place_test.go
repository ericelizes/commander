package livedata

import (
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// A step whose Operator runs on the user's Mac reads what cmdrd sees of the
// run: working, waiting for the dialog, or unreachable for a while, which is
// not Broken and not a death.
func TestBoardPlaceOperator(t *testing.T) {
	now := time.Unix(1793214000, 0)
	for _, tc := range []struct{ state, text string }{
		{"live", "Operator working on mac:laptop"},
		{"unreachable", "Operator on mac:laptop unreachable 4 min"},
		{"offered", "Waiting for the user to start the run on mac:laptop"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			rows := []ObjectiveRow{
				{ProjectID: 1, ProjectName: "app", BeadID: "app-w", Title: "Audit", Status: "open", State: "in-progress", Owner: "manager-app-w"},
				{ProjectID: 1, ProjectName: "app", BeadID: "app-w.1", ParentBeadID: "app-w", Title: "Read the reports", Status: "in_progress", State: "in-progress", Owner: "operator-op-mac", OwnerState: OwnerWorking},
			}
			rows = MarkLiveOwners(rows, LiveOperators{"operator-op-mac": true})
			rows = MarkPlaceOwners(rows, []SnapPlace{{Operator: "operator-op-mac", Place: "mac:laptop", State: tc.state, Text: tc.text}})
			snap := BuildSnapshot(Data{Projects: []store.Project{{ID: 1, Name: "app"}}, Objectives: rows, LoadedAt: now,
				Agents: map[string]store.AgentState{"manager-app-w": {Role: "manager-app-w", State: "idle"}}}, nil)
			d := Board(snap, now.Unix())
			if len(d.Workstreams) != 1 {
				t.Fatalf("rows %+v", d.Workstreams)
			}
			r := d.Workstreams[0]
			if r.Kind != "working" || !strings.HasPrefix(r.Text, "Working on read the reports") || r.Why != tc.text {
				t.Fatalf("row reads %q (%s) why %q", r.Text, r.Kind, r.Why)
			}
			found := false
			for _, s := range r.Workflow.Steps {
				if s.ID == "app-w.1" {
					found = true
					if s.Text != tc.text {
						t.Fatalf("workflow step reads %q", s.Text)
					}
				}
			}
			if !found {
				t.Fatalf("no workflow step: %+v", r.Workflow.Steps)
			}
			if len(SnapshotViolations(snap)) != 0 {
				t.Fatalf("broken: %+v", SnapshotViolations(snap))
			}
		})
	}
}
