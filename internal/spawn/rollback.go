package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// cleanupFn is one reversal step. Returns an error so the orchestrator can
// log/aggregate without aborting subsequent cleanups.
type cleanupFn func() error

// cleanups is an ordered stack of cleanup functions. Spawn pushes one entry
// per successful mutation step; on failure runReverse drains them LIFO so the
// most-recent mutation is reversed first.
type cleanups []cleanupFn

// push appends fn to the stack.
func (c *cleanups) push(fn cleanupFn) { *c = append(*c, fn) }

// runReverse runs every cleanup in reverse-insertion order and returns every
// non-nil error. ctx is accepted for API symmetry; cleanups manage their own
// timeouts internally so a cancelled outer ctx can't strand them.
func (c *cleanups) runReverse(_ context.Context) []error {
	var errs []error
	for i := len(*c) - 1; i >= 0; i-- {
		if err := (*c)[i](); err != nil {
			errs = append(errs, err)
		}
	}
	*c = nil
	return errs
}

// spawnLock is the per-(project, branch) flock guarding concurrent spawns.
// Released by .release() (idempotent; double-release returns nil).
type spawnLock struct {
	f      *os.File
	path   string
	closed bool
}

// release closes and removes the lock file. Idempotent — repeated calls return
// nil (so cleanup-stack semantics stay simple).
func (l *spawnLock) release() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	// Releasing the FD releases the flock as well.
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("spawn lock: close %s: %w", l.path, err)
	}
	// Best-effort: remove the lock file. Other processes may have re-acquired
	// it by now — that's fine, the unlink doesn't affect their lock.
	_ = os.Remove(l.path)
	return nil
}

// acquireSpawnLock acquires a non-blocking exclusive flock on
// ~/.commander/spawn-<project>-<flat-branch>.lock. Errors with a clear
// "already locked" message when another spawn is in progress.
func acquireSpawnLock(project, flatBranch string) (*spawnLock, error) {
	dir, err := commanderHomeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spawn lock: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "spawn-"+project+"-"+flatBranch+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("spawn lock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("spawn lock: another spawn already in progress for %s/%s", project, flatBranch)
		}
		return nil, fmt.Errorf("spawn lock: flock %s: %w", path, err)
	}
	return &spawnLock{f: f, path: path}, nil
}

// commanderHomeDir returns ~/.commander, creating it if missing. Used for
// spawn-lock files (host-wide, not project-scoped — cmdr might launch the
// same project from multiple checkouts).
func commanderHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("spawn lock: home dir: %w", err)
	}
	return filepath.Join(home, ".commander"), nil
}
