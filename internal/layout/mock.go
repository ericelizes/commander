package layout

import (
	"context"
	"strings"
	"sync"
)

// MockRunner is a hand-written test double that records every tmux argv
// invocation and replays scripted responses. Use it to assert layout's argv
// without spawning real tmux.
//
// Usage:
//
//	mr := &MockRunner{}
//	mr.SetSessionExists("commander", false)  // first call to has-session: not exists
//	d := NewDirectorWith(mr)
//	_ = d.LaunchCommander(ctx, t.TempDir(), false)
//	// Inspect mr.Calls — each entry is the slice of args (no leading "tmux").
type MockRunner struct {
	mu sync.Mutex

	// Calls records each Run invocation as the args slice (no leading "tmux").
	Calls [][]string

	// runFunc lets tests script responses per call. When nil, all calls return
	// (nil, nil, nil) — i.e. tmux exited 0 with no output.
	runFunc func(args []string) (stdout, stderr []byte, err error)
}

// SetRunFunc installs a per-call responder. Useful for branching responses
// (e.g. has-session returns 1 on the first call, 0 on the second).
func (m *MockRunner) SetRunFunc(fn func(args []string) ([]byte, []byte, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runFunc = fn
}

// SetSessionExists configures the mock to return the right exit code for
// has-session calls targeting `name`. Other calls fall through to the
// default no-op response.
func (m *MockRunner) SetSessionExists(name string, exists bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.runFunc
	m.runFunc = func(args []string) ([]byte, []byte, error) {
		if len(args) >= 3 && args[0] == "has-session" && args[1] == "-t" && args[2] == name {
			if exists {
				return nil, nil, nil
			}
			return nil, []byte("can't find session: " + name), errExit1
		}
		if prev != nil {
			return prev(args)
		}
		return nil, nil, nil
	}
}

// SetWindowExists configures the mock to return windowName as a row in the
// list-windows output for the named session.
func (m *MockRunner) SetWindowExists(session, windowName string, exists bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.runFunc
	m.runFunc = func(args []string) ([]byte, []byte, error) {
		// list-windows -t <session> -F #W
		if len(args) >= 5 && args[0] == "list-windows" && args[1] == "-t" && args[2] == session {
			if exists {
				return []byte(windowName + "\n"), nil, nil
			}
			return []byte(""), nil, nil
		}
		if prev != nil {
			return prev(args)
		}
		return nil, nil, nil
	}
}

// Run records args and dispatches to runFunc.
func (m *MockRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, append([]string(nil), args...))
	fn := m.runFunc
	m.mu.Unlock()
	if fn != nil {
		return fn(args)
	}
	return nil, nil, nil
}

// CallStrings returns the recorded argv slices flattened into "tmux a b c"
// strings — useful for substring assertions in tests.
func (m *MockRunner) CallStrings() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.Calls))
	for _, c := range m.Calls {
		out = append(out, "tmux "+strings.Join(c, " "))
	}
	return out
}

// errExit1 is a sentinel error mimicking tmux's "exit status 1". Callers
// that scan stderr for "can't find session" still see it on top of a
// non-nil err.
var errExit1 = exit1Err("exit status 1")

type exit1Err string

func (e exit1Err) Error() string { return string(e) }
