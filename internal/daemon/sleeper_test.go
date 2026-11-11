package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

type fakeChats struct {
	list    []chat.Status
	talking map[string]bool
	stopped []string
}

func (f *fakeChats) ListRunning() []chat.Status { return f.list }
func (f *fakeChats) Talking(role string) bool   { return f.talking[role] }
func (f *fakeChats) Stop(_ context.Context, role string) error {
	f.stopped = append(f.stopped, role)
	return nil
}

func TestSleeperStopsOnlyLongIdleManagersWithNothingWaiting(t *testing.T) {
	now := time.Unix(10_000, 0)
	long, recent := now.Add(-20*time.Minute).Unix(), now.Add(-5*time.Minute).Unix()
	f := &fakeChats{
		list: []chat.Status{
			{Role: "manager-p-idle", Running: true, State: "idle"},
			{Role: "manager-p-recent", Running: true, State: "idle"},
			{Role: "manager-p-working", Running: true, State: "working"},
			{Role: "manager-p-talking", Running: true, State: "idle"},
			{Role: "manager-p-mail", Running: true, State: "idle"},
			{Role: "manager-p-asleep", Running: false, State: "idle"},
			{Role: "commander", Running: true, State: "idle"},
		},
		talking: map[string]bool{"manager-p-talking": true},
	}
	states := []store.AgentState{
		{Role: "manager-p-idle", State: store.AgentIdle, Since: long},
		{Role: "manager-p-recent", State: store.AgentIdle, Since: recent},
		{Role: "manager-p-working", State: store.AgentWorking, Since: long},
		{Role: "manager-p-talking", State: store.AgentIdle, Since: long},
		{Role: "manager-p-mail", State: store.AgentIdle, Since: long},
		{Role: "manager-p-asleep", State: store.AgentIdle, Since: long},
		{Role: "commander", State: store.AgentIdle, Since: long},
	}
	s := managerSleeper{
		chat:   f,
		states: func(context.Context) ([]store.AgentState, error) { return states, nil },
		unread: func(_ context.Context, role string) (int, error) {
			if role == "manager-p-mail" {
				return 1, nil
			}
			return 0, nil
		},
		idle: managerIdleBeforeSleep, now: func() time.Time { return now },
	}
	got := s.sweep(context.Background())
	if !slices.Equal(got, []string{"manager-p-idle"}) || !slices.Equal(f.stopped, got) {
		t.Fatalf("slept %v (stopped %v), want only manager-p-idle", got, f.stopped)
	}
}
