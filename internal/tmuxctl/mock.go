package tmuxctl

import (
	"context"
	"strings"
	"sync"
)

// Compile-time guarantee that MockClient satisfies Client.
var _ Client = (*MockClient)(nil)

// MockClient is a hand-written test double for Client. It records every
// method invocation as a stringified call and replays scripted responses.
//
// Usage:
//
//	m := &MockClient{}
//	m.NewSession(ctx, "name", "/cwd")
//	// m.Calls == []string{"NewSession(name=name cwd=/cwd)"}
//
// Set the *Func fields for full per-call control; otherwise the static
// scripted slices/values are returned.
type MockClient struct {
	mu sync.Mutex

	// Calls records each method invocation as "MethodName(arg1=v1 ...)".
	Calls []string

	// NewSession knobs.
	NewSessionFunc func(ctx context.Context, name, cwd string) error
	NewSessionErr  error

	// NewSessionCommand / RespawnPane knobs.
	NewSessionCommandErr error
	RespawnPaneErr       error

	// SendKeys knobs.
	SendKeysFunc func(ctx context.Context, target, keys string) error
	SendKeysErr  error

	// PipePane knobs.
	PipePaneFunc func(ctx context.Context, target, fifoPath string) error
	PipePaneErr  error

	// KillPane knobs.
	KillPaneFunc func(ctx context.Context, target string) error
	KillPaneErr  error

	// SessionExists knobs.
	SessionExistsFunc   func(ctx context.Context, name string) (bool, error)
	SessionExistsResult bool
	SessionExistsErr    error
}

func (m *MockClient) record(s string) {
	m.mu.Lock()
	m.Calls = append(m.Calls, s)
	m.mu.Unlock()
}

// NewSession satisfies Client.
func (m *MockClient) NewSession(ctx context.Context, name, cwd string) error {
	m.record("NewSession(name=" + name + " cwd=" + cwd + ")")
	if m.NewSessionFunc != nil {
		return m.NewSessionFunc(ctx, name, cwd)
	}
	return m.NewSessionErr
}

// NewSessionCommand satisfies Client.
func (m *MockClient) NewSessionCommand(ctx context.Context, name, cwd string, argv ...string) error {
	m.record("NewSessionCommand(name=" + name + " cwd=" + cwd + " argv=" + strings.Join(argv, " ") + ")")
	return m.NewSessionCommandErr
}

// RespawnPane satisfies Client.
func (m *MockClient) RespawnPane(ctx context.Context, target, cwd string, argv ...string) error {
	m.record("RespawnPane(target=" + target + " cwd=" + cwd + " argv=" + strings.Join(argv, " ") + ")")
	return m.RespawnPaneErr
}

// SendKeys satisfies Client.
func (m *MockClient) SendKeys(ctx context.Context, target, keys string) error {
	m.record("SendKeys(target=" + target + " keys=" + keys + ")")
	if m.SendKeysFunc != nil {
		return m.SendKeysFunc(ctx, target, keys)
	}
	return m.SendKeysErr
}

// PipePane satisfies Client.
func (m *MockClient) PipePane(ctx context.Context, target, fifoPath string) error {
	m.record("PipePane(target=" + target + " fifo=" + fifoPath + ")")
	if m.PipePaneFunc != nil {
		return m.PipePaneFunc(ctx, target, fifoPath)
	}
	return m.PipePaneErr
}

// KillPane satisfies Client.
func (m *MockClient) KillPane(ctx context.Context, target string) error {
	m.record("KillPane(target=" + target + ")")
	if m.KillPaneFunc != nil {
		return m.KillPaneFunc(ctx, target)
	}
	return m.KillPaneErr
}

// SessionExists satisfies Client.
func (m *MockClient) SessionExists(ctx context.Context, name string) (bool, error) {
	m.record("SessionExists(name=" + name + ")")
	if m.SessionExistsFunc != nil {
		return m.SessionExistsFunc(ctx, name)
	}
	return m.SessionExistsResult, m.SessionExistsErr
}
