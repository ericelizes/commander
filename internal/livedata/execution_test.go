package livedata

import (
	"github.com/example-user/commander/internal/store"
	"testing"
)

func TestExecutionCountsIncludeNestedManagerAndExcludeIdleSupervisor(t *testing.T) {
	s := Snapshot{Projects: []SnapProject{{ID: 1, Name: "project"}}, Agents: map[string]SnapAgent{"operator-root-1": {State: "working"}}, Live: &SnapLive{Operators: []string{"operator-root.1"}}, Workstreams: []SnapWorkstream{{ProjectID: 1, Project: "project", BeadID: "root", Steps: []SnapStep{{BeadID: "nested", Children: []SnapStep{{BeadID: "step", Owner: "operator-root-1"}}}}}}}
	data := Data{Objectives: []ObjectiveRow{{ProjectID: 1, BeadID: "root"}, {ProjectID: 1, BeadID: "nested"}, {ProjectID: 1, BeadID: "step", Owner: "operator-root-1"}}, Operators: []OperatorRow{{ProjectID: 1, RunID: 7, AgentName: "operator-root.1", TaskBeadID: "hidden-task"}}}
	s.Executions = executionOf(s, data, map[string]ChatState{"commander": {State: "working"}, "manager-root": {State: "idle"}, "manager-nested": {State: "working"}})
	if n := executionCount(s, 1, "root", s.Workstreams[0].Steps); n != 2 {
		t.Fatalf("root count=%d, want Operator and nested Manager", n)
	}
	if n := executionCount(s, 1, "nested", s.Workstreams[0].Steps[0].Children); n != 2 {
		t.Fatalf("nested count=%d", n)
	}
	if n := executionCount(s, 1, "step", nil); n != 1 {
		t.Fatalf("step count=%d", n)
	}
	if len(s.Executions) != 4 {
		t.Fatalf("presence set=%#v", s.Executions)
	}
	d := Display{Workstreams: []BoardRow{{Project: "project", BeadID: "root", Working: 99, Tracks: []BoardTrack{{BeadID: "nested", Working: 99}}}}}
	applyExecutionCounts(&s, &d)
	if d.Workstreams[0].Working != 2 || d.Workstreams[0].Tracks[0].Working != 2 {
		t.Fatalf("display disagrees with execution set: %#v", d)
	}
}

func TestProcessWithoutTurnEvidenceDoesNotSpin(t *testing.T) {
	s := Snapshot{Live: &SnapLive{Operators: []string{"operator-task"}}}
	data := Data{Operators: []OperatorRow{{ProjectID: 1, AgentName: "operator-task", TaskBeadID: "task"}}}
	s.Executions = executionOf(s, data, nil)
	if len(s.Executions) != 1 || s.Executions[0].State != "unknown" {
		t.Fatalf("presence became activity: %#v", s.Executions)
	}
	if n := executionCount(s, 1, "task", nil); n != 0 {
		t.Fatalf("unknown run counted: %d", n)
	}
}

func TestOwnRuntimesSkipNestedWorkstreams(t *testing.T) {
	s := Snapshot{Executions: []SnapExecution{
		{ProjectID: 1, BeadID: "w.1", Role: "a", State: store.AgentWorking, Runtime: "codex"},
		{ProjectID: 1, BeadID: "w.2.1", Role: "b", State: store.AgentWorking, Runtime: "claude"},
		{ProjectID: 1, BeadID: "w.3", Role: "c", State: store.AgentWorking},
	}}
	steps := []SnapStep{{BeadID: "w.1"}, {BeadID: "w.2", Track: true, Children: []SnapStep{{BeadID: "w.2.1"}}}, {BeadID: "w.3"}}
	got := ownRuntimes(s, 1, "w", steps)
	if len(got) != 2 || got[0] != "codex" || got[1] != "agent" {
		t.Fatalf("own runtimes %v", got)
	}
	if nested := ownRuntimes(s, 1, "w.2", steps[1].Children); len(nested) != 1 || nested[0] != "claude" {
		t.Fatalf("nested runtimes %v", nested)
	}
}
