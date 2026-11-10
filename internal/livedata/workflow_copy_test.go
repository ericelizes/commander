package livedata

import (
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
	"testing"
	"time"
)

func TestWorkflowEnrichmentCannotMutateRevisionValidatedGraph(t *testing.T) {
	original := []ObjectiveRow{{WorkflowCheck: &workflowtemplate.Node{PRStates: map[string]string{"1": "OPEN"}, Steps: []workflowtemplate.Node{{PRStates: map[string]string{"2": "OPEN"}}}}}}
	frame := cloneObjectiveRows(original)
	frame[0].WorkflowCheck.PRStates["1"] = "MERGED"
	frame[0].WorkflowCheck.Steps[0].PRStates["2"] = "MERGED"
	if original[0].WorkflowCheck.PRStates["1"] != "OPEN" || original[0].WorkflowCheck.Steps[0].PRStates["2"] != "OPEN" {
		t.Fatal("frame enrichment mutated source graph")
	}
}
func TestPostgresLongToolDoesNotBecomeIdleWithoutStopEvent(t *testing.T) {
	now := time.Now()
	data := Data{Postgres: true, LoadedAt: now, Agents: map[string]store.AgentState{"operator-task": {State: store.AgentWorking, Updated: now.Add(-time.Hour).Unix()}}}
	s := BuildSnapshot(data, nil)
	if s.Agents["operator-task"].State != store.AgentWorking {
		t.Fatal("long tool expired into idle")
	}
}
