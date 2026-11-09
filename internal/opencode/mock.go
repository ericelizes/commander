package opencode

import (
	"context"
	"sync"
)

// MockClient is a hand-written test double for Client. It records every call
// and replays scripted responses. Not gomock; we want zero codegen + zero
// reflection so tests stay obvious.
//
// Usage:
//
//	m := &MockClient{}
//	m.RunResponse = &RunResult{SessionID: "ses_xyz"}
//	got, err := m.Run(ctx, "", "operator-code", "hi", RunOptions{})
//	// inspect m.RunCalls
type MockClient struct {
	mu sync.Mutex

	// RunCalls records every Run invocation in order.
	RunCalls []RunCall
	// StatsCalls records every Stats invocation in order.
	StatsCalls []StatsCall
	// ServeCalls records every Serve invocation in order.
	ServeCalls []ServeCall

	// RunFunc, if set, fully drives Run's behavior. Takes priority over
	// the static RunResponse / RunErr fields.
	RunFunc func(ctx context.Context, sessionID, agent, prompt string, opts RunOptions) (*RunResult, error)
	// RunResponse is returned by Run when RunFunc is nil. The Events channel
	// inside is closed by Run before return so it's safe to range over.
	RunResponse *RunResult
	// RunErr is returned alongside RunResponse.
	RunErr error

	// StatsFunc / StatsResponse / StatsErr mirror the Run knobs.
	StatsFunc     func(ctx context.Context, project string, daysBack int) (*Stats, error)
	StatsResponse *Stats
	StatsErr      error

	// ServeFunc / ServeResponse / ServeErr mirror the Run knobs.
	ServeFunc     func(ctx context.Context, port int) (Server, error)
	ServeResponse Server
	ServeErr      error
}

// RunCall captures the args of one Run invocation.
type RunCall struct {
	SessionID string
	Agent     string
	Prompt    string
	Opts      RunOptions
}

// StatsCall captures the args of one Stats invocation.
type StatsCall struct {
	Project  string
	DaysBack int
}

// ServeCall captures the args of one Serve invocation.
type ServeCall struct {
	Port int
}

// Run satisfies Client. If RunResponse has a nil Events channel, Run installs
// a closed empty channel so callers can range without blocking.
func (m *MockClient) Run(ctx context.Context, sessionID, agent, prompt string, opts RunOptions) (*RunResult, error) {
	m.mu.Lock()
	m.RunCalls = append(m.RunCalls, RunCall{SessionID: sessionID, Agent: agent, Prompt: prompt, Opts: opts})
	m.mu.Unlock()

	if m.RunFunc != nil {
		return m.RunFunc(ctx, sessionID, agent, prompt, opts)
	}
	resp := m.RunResponse
	if resp != nil && resp.Events == nil {
		ch := make(chan Event)
		close(ch)
		resp.Events = ch
	}
	return resp, m.RunErr
}

// Stats satisfies Client.
func (m *MockClient) Stats(ctx context.Context, project string, daysBack int) (*Stats, error) {
	m.mu.Lock()
	m.StatsCalls = append(m.StatsCalls, StatsCall{Project: project, DaysBack: daysBack})
	m.mu.Unlock()

	if m.StatsFunc != nil {
		return m.StatsFunc(ctx, project, daysBack)
	}
	return m.StatsResponse, m.StatsErr
}

// Serve satisfies Client.
func (m *MockClient) Serve(ctx context.Context, port int) (Server, error) {
	m.mu.Lock()
	m.ServeCalls = append(m.ServeCalls, ServeCall{Port: port})
	m.mu.Unlock()

	if m.ServeFunc != nil {
		return m.ServeFunc(ctx, port)
	}
	return m.ServeResponse, m.ServeErr
}

// MockServer is a trivial Server implementation for tests.
type MockServer struct {
	AddrValue   string
	ShutdownErr error
	Shutdowns   int
}

func (s *MockServer) Addr() string { return s.AddrValue }

func (s *MockServer) Shutdown(ctx context.Context) error {
	s.Shutdowns++
	return s.ShutdownErr
}
