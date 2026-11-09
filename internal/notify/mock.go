package notify

import (
	"context"
	"sync"
)

// NotifyCall is one observed Notify invocation, captured for assertions.
type NotifyCall struct {
	Title string
	Body  string
}

// MockNotifier is a Notifier that records every Notify call instead of
// shelling out. Tests inject it into the daemon and then assert against
// Calls / NotifyErr. Safe for concurrent use — the daemon calls Notify from
// goroutines.
type MockNotifier struct {
	mu        sync.Mutex
	Calls     []NotifyCall
	NotifyErr error // returned from every Notify call when non-nil
}

// Compile-time guard: MockNotifier implements Notifier.
var _ Notifier = (*MockNotifier)(nil)

// Notify records the call and returns NotifyErr.
func (m *MockNotifier) Notify(_ context.Context, title, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, NotifyCall{Title: title, Body: body})
	return m.NotifyErr
}

// Snapshot returns a copy of the recorded calls. Use this in assertions
// instead of touching Calls directly so concurrent Notify calls don't race
// with the test goroutine reading the slice.
func (m *MockNotifier) Snapshot() []NotifyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NotifyCall, len(m.Calls))
	copy(out, m.Calls)
	return out
}
