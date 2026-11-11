package livedata

import (
	"github.com/example-user/commander/internal/store"
	"testing"
)

func TestObservedExecutionCountsIgnorePlanningAndUnverifiedClaims(t *testing.T) {
	s := Snapshot{Storage: "postgres", Projects: []SnapProject{{ID: 1, Name: "project"}}, Workstreams: []SnapWorkstream{{ProjectID: 1, Project: "project", BeadID: "root", Working: true, Steps: []SnapStep{{BeadID: "nested", Working: true}}}}}
	data := Data{Postgres: true, ObservedExecutions: []store.ObservedExecution{
		{RunID: 1, ProjectID: 1, Role: "worker", SessionID: "sid", BeadID: "hidden", Path: []string{"hidden", "nested", "root"}, Known: true, Alive: true, State: store.AgentWorking},
		{RunID: 2, ProjectID: 1, Role: "unverified", BeadID: "root", State: store.AgentWorking},
	}}
	s.Executions = executionOf(s, data, map[string]ChatState{"commander": {State: "working"}, "manager-nested": {State: "working"}})
	applyObservedWorking(&s)
	if len(s.Executions) != 1 || s.Executions[0].RunID != 1 || !s.Workstreams[0].Steps[0].Working {
		t.Fatalf("wrong live execution set: %+v", s.Executions)
	}
	if n := executionCount(s, 1, "root", s.Workstreams[0].Steps); n != 1 {
		t.Fatalf("count=%d", n)
	}
	data.ObservedExecutions[0].Known = false
	s.Executions = executionOf(s, data, map[string]ChatState{"commander": {State: "working"}})
	applyObservedWorking(&s)
	if s.Workstreams[0].Working || s.Workstreams[0].Steps[0].Working || len(s.Executions) != 0 {
		t.Fatal("stale claim or planning chat kept spinner active")
	}
}

func TestClassifiedActivityRequiresTheObservedExecutionIdentity(t *testing.T) {
	run := SnapExecution{Role: "executor.task", RunID: 19, SessionID: "current", State: store.AgentWorking}
	proof := StatusEvidence{Role: "executor-task", RunID: 19, SessionID: "current", Kind: "working"}
	if !classificationCompatible(proof, "working", []SnapExecution{run}) {
		t.Fatal("current run summary lost")
	}
	for _, bad := range []StatusEvidence{{Role: proof.Role, RunID: 18, SessionID: "current", Kind: "working"}, {Role: proof.Role, RunID: 19, SessionID: "old", Kind: "working"}, {Role: proof.Role, Kind: "working"}} {
		if classificationCompatible(bad, "working", []SnapExecution{run}) {
			t.Fatalf("wrong execution summary accepted: %+v", bad)
		}
	}
}
