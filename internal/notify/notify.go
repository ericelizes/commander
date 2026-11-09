// Package notify dispatches one-shot, fire-and-forget user notifications to
// whatever native notification surface is available on the host. It exists so
// the daemon can wake the user (or whoever) up at 2am when an Operator hits a
// ceiling or an urgent mail lands — without the TUI being open.
//
// Backends, by GOOS:
//
//   - darwin: osascript -> Notification Center
//   - linux:  notify-send (libnotify) -> the desktop's notification daemon,
//     unless we detect WSL, in which case there's no graphical
//     surface to render to and we fall back to the console.
//   - other:  console fallback ([NOTIFY] <title>: <body> on stderr).
//
// Errors are surfaced (not swallowed) so callers can log them; the production
// daemon treats them as non-fatal. The package never panics on an absent
// notifier — every backend has a console fallback path.
package notify

import (
	"context"
	"os/exec"
	"runtime"
)

// Notifier dispatches a single notification. Implementations are expected to
// be cheap / non-blocking for the common case: the daemon calls Notify from
// hot inbox-fanout paths and can't afford a slow notifier wedging the loop.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// execFunc matches exec.CommandContext's signature, so production paths use
// the real one and tests inject a recording stub. Captured here (not in the
// stdlib alias) to keep the seam in one place.
type execFunc func(ctx context.Context, name string, args ...string) *exec.Cmd

// readFileFunc abstracts os.ReadFile so the WSL detection probe can be driven
// by a fake /proc/version in tests.
type readFileFunc func(path string) ([]byte, error)

// system is the production Notifier. runtimeOS, exec, and readFile are all
// overridable so tests can pin GOOS, capture argv, and stub /proc/version
// without the real syscalls firing.
type system struct {
	runtimeOS string
	exec      execFunc
	readFile  readFileFunc
}

// NewSystem returns the platform-detecting production Notifier. Constructed
// once at daemon startup; reused for the process lifetime.
func NewSystem() Notifier {
	return &system{
		runtimeOS: runtime.GOOS,
		exec:      exec.CommandContext,
	}
}

// Notify dispatches by GOOS. Unknown platforms get the console fallback so
// nothing is lost on, e.g., a Windows build that didn't go through WSL.
func (s *system) Notify(ctx context.Context, title, body string) error {
	switch s.runtimeOS {
	case "darwin":
		return s.notifyDarwin(ctx, title, body)
	case "linux":
		return s.notifyLinux(ctx, title, body)
	default:
		return s.notifyConsole(title, body)
	}
}
