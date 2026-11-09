package livedata

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestWatchGroupsRunningOperatorsByManager(t *testing.T) {
	data := Data{
		Operators: []OperatorRow{
			{AgentName: "operator-op-a", TaskBeadID: "example-app-t11.2.4.4", SpawnTime: time.Unix(100, 0), ActiveAt: time.Unix(160, 0)},
			{AgentName: "operator-op-b", TaskBeadID: "example-app-t11.3", SpawnTime: time.Unix(120, 0)},
		},
		Checkins: map[string]store.Checkin{"manager-example-app-t11": {Due: 900, Checked: 170}},
	}
	got := watchOf(data)["manager-example-app-t11"]
	want := SnapWatch{CheckedAt: 170, CheckinAt: 900, Operators: []SnapWatchedOp{
		{Role: "operator-op-a", BeadID: "example-app-t11.2.4.4", ActiveAt: 160, Since: 100},
		{Role: "operator-op-b", BeadID: "example-app-t11.3", Since: 120},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watch = %+v\nwant  %+v", got, want)
	}
	if watchOf(Data{}) != nil {
		t.Fatal("no Operators should mean no watch")
	}
	cw := watchOf(Data{
		Checkins: map[string]store.Checkin{"commander": {Due: 2000, Checked: 1900}},
		Issues:   []store.WatchIssue{{WS: "c-1", Text: "stalled", Since: 10, Active: true}, {WS: "t-1", Text: "CI", Since: 20}},
	})["commander"]
	if cw.CheckinAt != 2000 || len(cw.Issues) != 1 || cw.Issues[0] != (SnapIssue{Workstream: "c-1", Text: "stalled", Since: 10}) {
		t.Fatalf("commander watch = %+v", cw)
	}
}

// A recently ended Operator reaches its Manager's watch as finished (with its
// report line) or died, even when nothing is running.
func TestWatchCarriesRecentlyFinishedOperators(t *testing.T) {
	data := Data{Finished: []store.CostEvent{
		{AgentName: "operator-op-a", TaskBeadID: sql.NullString{String: "example-app-t11.2.4", Valid: true}, EventType: store.EventOperatorExited, Reason: sql.NullString{String: "All green", Valid: true}, OccurredAt: 500},
		{AgentName: "operator-op-b", TaskBeadID: sql.NullString{String: "example-app-t11.3", Valid: true}, EventType: store.EventOperatorDied, OccurredAt: 520},
	}}
	got := watchOf(data)["manager-example-app-t11"]
	want := []SnapFinishedOp{
		{Role: "operator-op-a", BeadID: "example-app-t11.2.4", At: 500, Report: "All green"},
		{Role: "operator-op-b", BeadID: "example-app-t11.3", At: 520, Died: true},
	}
	if !reflect.DeepEqual(got.Finished, want) || len(got.Operators) != 0 {
		t.Fatalf("watch = %+v", got)
	}
}

// The live case: example-app-t02.8 was promoted to a workstream (its parent edge
// removed), so its running and finished Operators are watched by
// manager-example-app-t02.8, decided from the parents the snapshot already read.
func TestWatchFollowsRealParentsOfAPromotedWorkstream(t *testing.T) {
	bc := &beads.MemClient{EdgeParentsOnly: true}
	for _, b := range []beads.Bead{
		{ID: "example-app-t02", Labels: []string{"kind:objective"}},
		{ID: "example-app-t02.2", Labels: []string{"kind:objective"}},
		{ID: "example-app-t02.2.1", Labels: []string{"kind:task"}, Status: "in_progress"},
		{ID: "example-app-t02.8", Labels: []string{"kind:objective"}},
		{ID: "example-app-t02.8.4", Labels: []string{"kind:objective"}},
		{ID: "example-app-t02.8.4.5", Labels: []string{"kind:task"}, Status: "in_progress"},
	} {
		bc.Seed(b)
	}
	bc.Edges = []beads.Dep{
		{IssueID: "example-app-t02.2", DependsOnID: "example-app-t02", Type: "parent-child"},
		{IssueID: "example-app-t02.2.1", DependsOnID: "example-app-t02.2", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4", DependsOnID: "example-app-t02.8", Type: "parent-child"},
		{IssueID: "example-app-t02.8.4.5", DependsOnID: "example-app-t02.8.4", Type: "parent-child"},
	}
	rows, err := projectObjectiveRows(context.Background(), bc, store.Project{ID: 1, Name: "example-app", Path: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	data := Data{
		Objectives: rows,
		Operators: []OperatorRow{
			{AgentName: "operator-op-a", TaskBeadID: "example-app-t02.8.4.5", SpawnTime: time.Unix(100, 0)},
			{AgentName: "operator-op-b", TaskBeadID: "example-app-t02.2.1", SpawnTime: time.Unix(100, 0)},
		},
		Finished: []store.CostEvent{{AgentName: "operator-op-c", TaskBeadID: sql.NullString{String: "example-app-t02.8.4.9", Valid: true}, EventType: store.EventOperatorExited, OccurredAt: 500}},
	}
	w := watchOf(data)
	promoted := w["manager-example-app-t02.8"]
	if len(promoted.Operators) != 1 || promoted.Operators[0].BeadID != "example-app-t02.8.4.5" {
		t.Fatalf("promoted Manager watches %+v; all: %+v", promoted.Operators, w)
	}
	if len(promoted.Finished) != 1 || promoted.Finished[0].Role != "operator-op-c" {
		t.Fatalf("a finished bead no longer loaded still reaches its real Manager: %+v", promoted.Finished)
	}
	if old := w["manager-example-app-t02"]; len(old.Operators) != 1 || old.Operators[0].BeadID != "example-app-t02.2.1" {
		t.Fatalf("ordinary nested Operator moved: %+v", old.Operators)
	}
}
