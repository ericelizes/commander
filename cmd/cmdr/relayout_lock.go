package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// relayoutLock serialises __relayout runs (select-window, window-resized and
// client hooks can all fire at once). ok is false when another run holds it.
func relayoutLock() (release func(), ok bool) {
	home := os.Getenv("COMMANDER_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return func() {}, true
		}
		home = filepath.Join(h, ".commander")
	}
	_ = os.MkdirAll(home, 0o755)
	f, err := os.OpenFile(filepath.Join(home, "relayout.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, true // never block layout on a lock problem
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, true
}
