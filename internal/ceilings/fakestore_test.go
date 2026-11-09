package ceilings

import (
	"context"
	"sync"

	"github.com/example-user/commander/internal/store"
)

// fakeStore is the in-memory WatchdogStore used by every ceilings test. It
// records call sequences so tests can assert ordering (kill chain), and
// holds simple counters for the capacity-cap tests.
type fakeStore struct {
	mu sync.Mutex

	// recorded calls in insertion order.
	calls []string

	// scriptable run state.
	tokens map[int64]int64
	usd    map[int64]float64
	closed map[int64]string
	fails  map[int64]int64

	// cost_events captured.
	costEvents []store.CostEvent

	// capacity knobs.
	perProjectRunning map[int64]int // projectID -> count
	globalRunning     int

	// errors.
	UpdateUsageErr      error
	MarkClosedErr       error
	IncrementErr        error
	InsertCostEventErr  error
	ListRunningRunsList []store.AgentRun
	ListRunningRunsErr  error
	CountByProjectErr   error
	CountAllErr         error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tokens:            map[int64]int64{},
		usd:               map[int64]float64{},
		closed:            map[int64]string{},
		fails:             map[int64]int64{},
		perProjectRunning: map[int64]int{},
	}
}

func (f *fakeStore) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeStore) UpdateUsage(_ context.Context, runID int64, tokens int64, usd float64) error {
	f.record("UpdateUsage")
	if f.UpdateUsageErr != nil {
		return f.UpdateUsageErr
	}
	f.mu.Lock()
	f.tokens[runID] = tokens
	f.usd[runID] = usd
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) MarkClosed(_ context.Context, runID int64, status string) error {
	f.record("MarkClosed:" + status)
	if f.MarkClosedErr != nil {
		return f.MarkClosedErr
	}
	f.mu.Lock()
	f.closed[runID] = status
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) IncrementFailures(_ context.Context, runID int64) (int64, error) {
	f.record("IncrementFailures")
	if f.IncrementErr != nil {
		return 0, f.IncrementErr
	}
	f.mu.Lock()
	f.fails[runID]++
	n := f.fails[runID]
	f.mu.Unlock()
	return n, nil
}

func (f *fakeStore) InsertCostEvent(_ context.Context, e store.CostEvent) (int64, error) {
	f.record("InsertCostEvent:" + e.EventType)
	if f.InsertCostEventErr != nil {
		return 0, f.InsertCostEventErr
	}
	f.mu.Lock()
	f.costEvents = append(f.costEvents, e)
	id := int64(len(f.costEvents))
	f.mu.Unlock()
	return id, nil
}

func (f *fakeStore) ListRunningRuns(_ context.Context) ([]store.AgentRun, error) {
	f.record("ListRunningRuns")
	return f.ListRunningRunsList, f.ListRunningRunsErr
}

func (f *fakeStore) CountRunsByProject(_ context.Context, projectID int64, status string) (int, error) {
	f.record("CountRunsByProject:" + status)
	if f.CountByProjectErr != nil {
		return 0, f.CountByProjectErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perProjectRunning[projectID], nil
}

func (f *fakeStore) CountAllRuns(_ context.Context, status string) (int, error) {
	f.record("CountAllRuns:" + status)
	if f.CountAllErr != nil {
		return 0, f.CountAllErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.globalRunning, nil
}

// callsContains returns true if any recorded call equals s exactly.
func (f *fakeStore) callsContains(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

// callsCount returns how many recorded calls equal s.
func (f *fakeStore) callsCount(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}
