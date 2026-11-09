package ceilings

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
)

// withFrozenTime overrides nowFunc for the duration of one test. Restores in
// cleanup so tests stay independent.
func withFrozenTime(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = prev })
}

func TestTickOnce_WallclockBreachKills(t *testing.T) {
	fs := newFakeStore()
	dm := &docker.MockClient{}
	w := NewWatchdog(fs, dm, &opencode.MockClient{}, &beads.MockClient{}, &agentmail.MockClient{}, Limits{
		MaxWallclockMinutes: 1, // 1-minute wallclock for fast test
	})

	spawn := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	withFrozenTime(t, spawn.Add(2*time.Minute))

	st := &runState{
		runID:       11,
		containerID: "c-11",
		projectID:   1,
		projectName: "p",
		projectPath: "/p",
		taskBeadID:  "bd-11",
		agentName:   "operator-old",
		spawnTime:   spawn,
	}
	w.runs.Store(st.runID, st)

	w.tickOnce(context.Background())

	if !fs.callsContains("MarkClosed:killed-ceiling") {
		t.Fatalf("expected wallclock kill to MarkClosed:killed-ceiling; calls: %v", fs.calls)
	}
	if !fs.callsContains("InsertCostEvent:ceiling-hit") {
		t.Fatalf("expected cost_events ceiling-hit row")
	}
	if len(fs.costEvents) != 1 || !strings.Contains(fs.costEvents[0].Reason.String, "wallclock") {
		t.Fatalf("expected wallclock reason; got %+v", fs.costEvents)
	}
}

func TestTickOnce_UnderWallclockNoKill(t *testing.T) {
	fs := newFakeStore()
	w := NewWatchdog(fs, &docker.MockClient{}, &opencode.MockClient{}, &beads.MockClient{}, &agentmail.MockClient{}, Limits{
		MaxWallclockMinutes: 60,
	})

	spawn := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	withFrozenTime(t, spawn.Add(5*time.Minute))

	st := &runState{
		runID:     12,
		spawnTime: spawn,
	}
	w.runs.Store(st.runID, st)

	w.tickOnce(context.Background())

	if fs.callsContains("MarkClosed:killed-ceiling") {
		t.Fatalf("did not expect wallclock kill at 5 min < 60 min limit")
	}
}

func TestTickOnce_RetryBreachKills(t *testing.T) {
	fs := newFakeStore()
	w := NewWatchdog(fs, &docker.MockClient{}, &opencode.MockClient{}, &beads.MockClient{}, &agentmail.MockClient{}, Limits{
		MaxWallclockMinutes: 60,
		MaxRetries:          3,
	})
	withFrozenTime(t, time.Now())

	st := &runState{
		runID:               13,
		spawnTime:           nowFunc(),
		consecutiveFailures: 3,
		projectName:         "p",
		projectPath:         "/p",
		taskBeadID:          "bd-13",
		agentName:           "operator-flaky",
	}
	w.runs.Store(st.runID, st)

	w.tickOnce(context.Background())

	if !fs.callsContains("MarkClosed:killed-ceiling") {
		t.Fatalf("expected retry-ceiling kill on tick")
	}
}
