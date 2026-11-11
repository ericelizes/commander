package api

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/pgtest"
)

func TestOneFailedLiveStateReadIsRetriedBeforeReportingUnavailable(t *testing.T) {
	st, _ := pgtest.Open(t)
	healthy := true
	s := New(Options{Token: "t", StateStore: st, Source: func(context.Context) livedata.Snapshot {
		if healthy {
			return livedata.Snapshot{Storage: "postgres", Workstreams: []livedata.SnapWorkstream{{BeadID: "a"}}}
		}
		return livedata.Snapshot{Storage: "postgres", Degraded: &livedata.SnapDegraded{Reason: "timeout"}}
	}})
	ctx := context.Background()
	shown := func() livedata.Snapshot { s.mu.Lock(); defer s.mu.Unlock(); return s.cur }
	s.Refresh(ctx)
	healthy = false
	s.Refresh(ctx)
	if c := shown(); c.Degraded != nil || len(c.Workstreams) != 1 {
		t.Fatalf("one failed read reported unavailable: %#v", c)
	}
	s.failingSince = time.Now().Add(-unavailableGrace)
	s.Refresh(ctx)
	if c := shown(); c.Degraded == nil || len(c.Workstreams) != 0 {
		t.Fatalf("reads failing past the grace were hidden: %#v", c)
	}
	healthy = true
	s.Refresh(ctx)
	if c := shown(); c.Degraded != nil {
		t.Fatalf("recovery: %#v", c)
	}
}

func TestFailedLiveStateReadWithNoGoodFrameIsUnavailableAtOnce(t *testing.T) {
	st, _ := pgtest.Open(t)
	s := New(Options{Token: "t", StateStore: st, Source: func(context.Context) livedata.Snapshot {
		return livedata.Snapshot{Storage: "postgres", Degraded: &livedata.SnapDegraded{Reason: "timeout"}}
	}})
	s.Refresh(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.Degraded == nil {
		t.Fatal("no good frame to keep, but unavailable was not reported")
	}
}

func TestBoardSkipsChangesItNeverReads(t *testing.T) {
	for _, c := range []struct {
		entities []string
		want     bool
	}{
		{nil, true},
		{[]string{"usage_turns"}, false},
		{[]string{"usage_turns", "bead_supervision", "supervision_attempts"}, false},
		{[]string{"usage_turns", "bead_tasks"}, true},
		{[]string{"agent_state"}, true},
	} {
		if got := boardReadsAny(c.entities); got != c.want {
			t.Errorf("%v: %v, want %v", c.entities, got, c.want)
		}
	}
}
