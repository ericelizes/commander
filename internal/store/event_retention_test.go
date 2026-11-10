package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPostgresReplayRetentionRequiresResync(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	if e := s.SetAgentState(ctx, "operator-example", AgentWorking, "first"); e != nil {
		t.Fatal(e)
	}
	events, e := s.StateEventsSince(ctx, 0, 100)
	if e != nil || len(events) == 0 {
		t.Fatalf("events=%v err=%v", events, e)
	}
	cursor := events[len(events)-1].Seq
	if e = s.SetAgentState(ctx, "operator-example", AgentIdle, "second"); e != nil {
		t.Fatal(e)
	}
	events, e = s.StateEventsSince(ctx, cursor, 100)
	if e != nil || len(events) == 0 {
		t.Fatal(e)
	}
	generation, _, e := s.StateGeneration(ctx)
	if e != nil || generation == "" {
		t.Fatal(e)
	}
	if _, e = s.PruneStateEvents(ctx, time.Now().Add(time.Hour), 5000); e != nil {
		t.Fatal(e)
	}
	if _, e = s.StateEventsSince(ctx, cursor, 100); !errors.Is(e, ErrReplayExpired) {
		t.Fatalf("expired cursor accepted: %v", e)
	}
	if _, e = s.StateEventsSince(ctx, 0, 100); e != nil {
		t.Fatalf("fresh reload refused: %v", e)
	}
	states, e := s.ListAgentStates(ctx)
	if e != nil || len(states) != 1 || states[0].State != AgentIdle {
		t.Fatalf("authoritative state lost: %#v %v", states, e)
	}
}
