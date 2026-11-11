package livedata

import (
	"github.com/example-user/commander/internal/store"
	"testing"
	"time"
)

func TestBoardExecutionQueueIsTruthfulWithoutWorkingAgents(t *testing.T) {
	queued := SnapStep{BeadID: "task", Title: "Task", Status: "open", State: "open", PlanningCommander: "commander", ExecutionReady: true, ExecutionEligible: true, ExecutionCapable: true}
	s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", ProjectID: 1, BeadID: "root", Title: "Goal", Status: "open", PlanningCommander: "commander", Steps: []SnapStep{queued}}}}
	d := Board(s, time.Now().Unix())
	if len(d.Workstreams) != 1 {
		t.Fatal(d)
	}
	row := d.Workstreams[0]
	if row.Kind != "waiting" || row.Text != "Queued for available execution capacity" || row.Working != 0 {
		t.Fatalf("queue misreported: %+v", row)
	}
	if violations := SnapshotViolations(s); len(violations) != 0 {
		t.Fatalf("durable queue reported unowned: %+v", violations)
	}
	s.Workstreams[0].Steps[0].ExecutionReserved = true
	if row = Board(s, time.Now().Unix()).Workstreams[0]; row.Text != "Preparing an execution" || row.Working != 0 {
		t.Fatalf("startup reported as working: %+v", row)
	}
	s.Workstreams[0].Steps[0].ExecutionReserved = false
	s.Workstreams[0].Steps[0].ExecutionCapable = false
	if row = Board(s, time.Now().Unix()).Workstreams[0]; row.Text != "Waiting for the required execution capability" || row.Kind == "needs" {
		t.Fatalf("missing capability was made a user request: %+v", row)
	}
	s.Workstreams[0].Steps[0].ExecutionMode = "shadow"
	if row = Board(s, time.Now().Unix()).Workstreams[0]; row.Text != "Ready; automatic execution is in shadow mode" {
		t.Fatalf("shadow mode promised dispatch: %+v", row)
	}
	s.Workstreams[0].Steps[0].ExecutionMode = "off"
	if row = Board(s, time.Now().Unix()).Workstreams[0]; row.Text != "Ready; automatic execution is disabled" {
		t.Fatalf("disabled mode promised dispatch: %+v", row)
	}
	s.Workstreams[0].Steps[0].ExecutionReady = false
	s.Workstreams[0].Steps[0].QuestionWaiting = true
	row = Board(s, time.Now().Unix()).Workstreams[0]
	if row.Kind != "waiting" || row.Working != 0 || row.Text != "Waiting for the responsible Commander's decision" {
		t.Fatalf("internal question misreported: %+v", row)
	}
	if violations := SnapshotViolations(s); len(violations) != 0 {
		t.Fatalf("recorded planning question became unowned state: %+v", violations)
	}
}

func TestBoardSavedHumanAnswerIsInternalWorkNotAnotherUserAsk(t *testing.T) {
	row := ObjectiveRow{ProjectID: 1, ProjectName: "p", BeadID: "root", Title: "Release", Status: "open", State: "needs-user", Gate: true, Approvers: []string{"user"}, GateNote: "Release the agreed scope", Decision: &store.Decision{Question: "Release?", Options: []store.DecisionOption{{Label: "Approve", Approves: true}}}, HumanAnswerPending: true, PlanningCommander: "commander"}
	project := store.Project{ID: 1, Name: "p", Path: "/p", Status: "active"}
	data := Data{Projects: []store.Project{project}, Objectives: []ObjectiveRow{row}, workstreams: []Workstream{{Project: project, Root: row}}}
	unanswered := row
	unanswered.HumanAnswerPending = false
	if asks := NeedsYouList(Data{Projects: []store.Project{project}, Objectives: []ObjectiveRow{unanswered}, workstreams: []Workstream{{Project: project, Root: unanswered}}}); len(asks) != 1 {
		t.Fatalf("fixture did not contain a genuine user decision: %+v", asks)
	}
	if asks := NeedsYouList(data); len(asks) != 0 {
		t.Fatalf("saved answer asked twice: %+v", asks)
	}
	snapshot := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", ProjectID: 1, BeadID: "root", Title: "Release", Status: "open", State: "review", HumanAnswerPending: true, PlanningCommander: "commander"}}}
	board := Board(snapshot, time.Now().Unix())
	if len(board.Workstreams) != 1 || board.Workstreams[0].Text != "Applying your recorded answer" || board.Workstreams[0].Working != 0 {
		t.Fatalf("saved answer was misreported: %+v", board)
	}
	if problems := SnapshotViolations(snapshot); len(problems) != 0 {
		t.Fatalf("durable answer application was treated as unowned: %+v", problems)
	}
}
