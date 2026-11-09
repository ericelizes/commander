package store

import (
	"context"
	"testing"
)

func TestAgentState_SinceMovesOnlyWhenTheStateChanges(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	if err := s.SetAgentState(ctx, "manager-x", AgentWorking, "Bash: go test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_state SET since = since - 100`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentState(ctx, "manager-x", AgentWorking, "Edit: foo.go"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAgentStates(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[0].Detail != "Edit: foo.go" || got[0].Updated-got[0].Since < 100 {
		t.Errorf("same state must keep since and take the new detail: %+v", got[0])
	}
	if err := s.SetAgentState(ctx, "manager-x", AgentIdle, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListAgentStates(ctx)
	if got[0].State != AgentIdle || got[0].Updated-got[0].Since > 5 {
		t.Errorf("a state change restarts since: %+v", got[0])
	}
}
