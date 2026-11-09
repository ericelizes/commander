package docker

import (
	"context"
	"sync"
)

// Compile-time assertion that MockClient satisfies Client.
var _ Client = (*MockClient)(nil)

// MockClient is a hand-written test double for Client. It records every
// method invocation as a stringified call and replays scripted responses.
//
// Usage:
//
//	m := &MockClient{SpawnIDs: []string{"abc123"}}
//	id, _ := m.Spawn(ctx, SpawnOptions{Image: "x", Name: "y"})
//	// m.Calls == []string{"Spawn(image=x name=y)"} (or similar)
//
// Set the *Func fields for full per-call control; otherwise the static
// scripted slices/values are returned in FIFO order.
type MockClient struct {
	mu sync.Mutex

	// Calls records each method call as "MethodName(arg1=v1 ...)".
	Calls []string

	// Spawn knobs.
	SpawnFunc func(ctx context.Context, opts SpawnOptions) (string, error)
	SpawnIDs  []string // FIFO scripted return IDs
	SpawnErr  error

	// Stop knobs.
	StopFunc func(ctx context.Context, idOrName string) error
	StopErr  error

	// Inspect knobs.
	InspectFunc      func(ctx context.Context, idOrName string) (*ContainerInfo, error)
	InspectResponse  *ContainerInfo
	InspectResponses []*ContainerInfo // FIFO scripted, takes precedence over InspectResponse
	InspectErr       error

	// Stats knobs.
	StatsFunc     func(ctx context.Context, idOrName string) (*RuntimeStats, error)
	StatsResponse *RuntimeStats
	StatsErr      error

	// ListByLabel knobs.
	ListFunc     func(ctx context.Context, labelKey string) ([]ContainerInfo, error)
	ListResponse []ContainerInfo
	ListErr      error
}

func (m *MockClient) record(s string) {
	m.mu.Lock()
	m.Calls = append(m.Calls, s)
	m.mu.Unlock()
}

// Spawn satisfies Client. Returns SpawnIDs in FIFO order; once exhausted,
// returns the empty string with SpawnErr.
func (m *MockClient) Spawn(ctx context.Context, opts SpawnOptions) (string, error) {
	m.record("Spawn(image=" + opts.Image + " name=" + opts.Name + ")")
	if m.SpawnFunc != nil {
		return m.SpawnFunc(ctx, opts)
	}
	if len(m.SpawnIDs) > 0 {
		m.mu.Lock()
		id := m.SpawnIDs[0]
		m.SpawnIDs = m.SpawnIDs[1:]
		m.mu.Unlock()
		return id, m.SpawnErr
	}
	return "", m.SpawnErr
}

// Stop satisfies Client.
func (m *MockClient) Stop(ctx context.Context, idOrName string) error {
	m.record("Stop(id=" + idOrName + ")")
	if m.StopFunc != nil {
		return m.StopFunc(ctx, idOrName)
	}
	return m.StopErr
}

// Inspect satisfies Client. Returns InspectResponses in FIFO order if set,
// else falls back to the static InspectResponse.
func (m *MockClient) Inspect(ctx context.Context, idOrName string) (*ContainerInfo, error) {
	m.record("Inspect(id=" + idOrName + ")")
	if m.InspectFunc != nil {
		return m.InspectFunc(ctx, idOrName)
	}
	if len(m.InspectResponses) > 0 {
		m.mu.Lock()
		r := m.InspectResponses[0]
		m.InspectResponses = m.InspectResponses[1:]
		m.mu.Unlock()
		return r, m.InspectErr
	}
	return m.InspectResponse, m.InspectErr
}

// Stats satisfies Client.
func (m *MockClient) Stats(ctx context.Context, idOrName string) (*RuntimeStats, error) {
	m.record("Stats(id=" + idOrName + ")")
	if m.StatsFunc != nil {
		return m.StatsFunc(ctx, idOrName)
	}
	return m.StatsResponse, m.StatsErr
}

// ListByLabel satisfies Client.
func (m *MockClient) ListByLabel(ctx context.Context, labelKey string) ([]ContainerInfo, error) {
	m.record("ListByLabel(key=" + labelKey + ")")
	if m.ListFunc != nil {
		return m.ListFunc(ctx, labelKey)
	}
	return m.ListResponse, m.ListErr
}
