package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresReplayIncludesLateCommit(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	late, e := s.DB().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer late.Rollback()
	if _, e = late.Exec(`INSERT INTO agent_state(role,state,detail,since,updated) VALUES('late','working','',1,1)`); e != nil {
		t.Fatal(e)
	}
	if e = s.SetAgentState(ctx, "early", AgentWorking, ""); e != nil {
		t.Fatal(e)
	}
	events, e := s.StateEventsSince(ctx, 0, 100)
	if e != nil || len(events) != 1 {
		t.Fatalf("before late commit: %#v %v", events, e)
	}
	cursor := events[0].Seq
	if e = late.Commit(); e != nil {
		t.Fatal(e)
	}
	events, e = s.StateEventsSince(ctx, cursor, 100)
	if e != nil || len(events) != 1 || events[0].Seq <= cursor {
		t.Fatalf("late event lost: %#v %v", events, e)
	}
	if string(events[0].Identity) != "{\"role\": \"late\"}" {
		t.Fatalf("wrong event: %s", events[0].Identity)
	}
	if again, e := s.StateEventsSince(ctx, events[0].Seq, 100); e != nil || len(again) != 0 {
		t.Fatalf("duplicate delivery: %#v %v", again, e)
	}
}

func TestPostgresNotificationWakesAfterCommit(t *testing.T) {
	s := postgresTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	changes := s.WatchState(ctx)
	select {
	case <-changes:
	case <-ctx.Done():
		t.Fatal("watch did not connect")
	}
	if e := s.SetAgentState(ctx, "notify", AgentWorking, ""); e != nil {
		t.Fatal(e)
	}
	select {
	case <-changes:
	case <-ctx.Done():
		t.Fatal("committed event did not wake readers")
	}
}
