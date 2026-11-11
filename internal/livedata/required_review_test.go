package livedata

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParkedRequiredReviewStillHoldsSnapshotDependents(t *testing.T) {
	review := SnapStep{BeadID: "review", Title: "Required review", Status: beads.StatusDeferred, WorkflowCheck: &workflowtemplate.Node{Bead: beads.Bead{ID: "review", Labels: []string{"workflow-template:pr-review"}}}}
	work := SnapStep{BeadID: "work", Title: "Release", Status: "open", State: "open", BlockedBy: []string{"review"}}
	snapshot := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "root", Title: "Goal", Status: "open", Steps: []SnapStep{review, work}}}}
	if problems := SnapshotViolations(snapshot); len(problems) != 0 {
		t.Fatalf("snapshot lost the required parked check and reported work unheld: %+v", problems)
	}
	snapshot.Workstreams[0].Steps[0].WorkflowCheck = nil
	if problems := SnapshotViolations(snapshot); len(problems) == 0 {
		t.Fatal("ordinary parked work unexpectedly retained required-check authority")
	}
}

func TestBoardRequiredParkedChecksStayVisibleWithoutInventingWork(t *testing.T) {
	var scenario struct {
		Cases []struct {
			Name, Status string
			Superseded   bool
			Labels       []string
			Want         string
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-required-check-display.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) != 4 {
		t.Fatal("required-check scenario cases missing")
	}
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			check := SnapStep{BeadID: "check", Title: "Required review", Status: tc.Status, Superseded: tc.Superseded}
			if tc.Labels != nil {
				check.WorkflowCheck = &workflowtemplate.Node{Bead: beads.Bead{ID: "check", Labels: tc.Labels}}
			}
			work := SnapStep{BeadID: "work", Title: "Release", Status: "open", BlockedBy: []string{"check"}}
			s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "root", Title: "Delivery", Status: "open", PlanningCommander: "commander", Steps: []SnapStep{work}}, {N: 2, Project: "p", BeadID: "reviews", Title: "Reviews", Status: "open", Steps: []SnapStep{check}}}}
			row := Board(s, time.Now().Unix()).Workstreams[0]
			if tc.Want != "" {
				if row.Text != tc.Want || row.Kind != "waiting" || row.Working != 0 || row.Flag != 0 {
					t.Fatalf("required check misreported: %+v", row)
				}
			} else if _, ok := newBoard(s, 0).requiredCheckLine(s.Workstreams[0]); ok {
				t.Fatalf("nonblocking check invented a dependency: %+v", row)
			}
			if tc.Want != "" {
				// A same-ID bead in another project must never satisfy this dependency.
				s.Workstreams = append(s.Workstreams, SnapWorkstream{Project: "other", BeadID: "foreign", Steps: []SnapStep{{BeadID: "check", Status: "closed"}}})
				if got := Board(s, 0).Workstreams[0]; got.Text != tc.Want {
					t.Fatalf("foreign project changed blocker: %+v", got)
				}
				s.Workstreams[0].BlockedBy = []string{"check"}
				s.Workstreams[0].Steps = nil
				if got := Board(s, 0).Workstreams[0]; got.Text != tc.Want {
					t.Fatalf("workstream root lost its dependency: %+v", got)
				}
				s.Workstreams[0].BlockedBy = nil
				s.Workstreams[0].Steps = []SnapStep{{BeadID: "parked-scope", Track: true, Status: beads.StatusDeferred, Children: []SnapStep{work}}}
				if _, ok := newBoard(s, 0).requiredCheckLine(s.Workstreams[0]); ok {
					t.Fatal("parked nested scope was implicitly resumed")
				}
				s.Workstreams[0].Status = beads.StatusDeferred
				if _, ok := newBoard(s, 0).requiredCheckLine(s.Workstreams[0]); ok {
					t.Fatal("parked whole scope was implicitly resumed")
				}
			}
		})
	}
}

// Exercise canonical beads -> rows -> snapshot -> board, not only a hand-built
// snapshot: generated workflow steps must retain their requirement labels.
func TestRequiredWorkflowStepSurvivesDisplayProjection(t *testing.T) {
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w", Title: "Delivery", Status: "open", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", Title: "Review", Status: beads.StatusDeferred, Labels: []string{"kind:objective", "workflow-step:review"}})
	mem.Seed(beads.Bead{ID: "w.2", Title: "Release", Status: "open", Labels: []string{"kind:objective"}})
	mem.Edges = append(mem.Edges, beads.Dep{IssueID: "w.2", DependsOnID: "w.1", Type: "blocks"})
	project := store.Project{ID: 1, Name: "p", Path: "/p", Status: "active"}
	rows, err := projectObjectiveRows(context.Background(), mem, project)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := BuildSnapshot(Data{Projects: []store.Project{project}, Objectives: rows, LoadedAt: time.Now()}, nil)
	if len(snapshot.Workstreams) != 1 {
		t.Fatalf("missing tree: %+v", snapshot.Workstreams)
	}
	row := Board(snapshot, time.Now().Unix()).Workstreams[0]
	if row.Text != "Waiting on deferred required check: review" || row.Working != 0 || row.Flag != 0 {
		t.Fatalf("canonical check lost in display: %+v", row)
	}
}
