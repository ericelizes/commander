package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceInterval is the quiet period the watcher waits before reloading.
// Vim and similar editors save via write+rename which fires several events in
// a tight burst; 500ms is comfortably longer than that burst on every OS we
// target (Linux inotify, macOS kqueue) without feeling laggy to a human.
const debounceInterval = 500 * time.Millisecond

// WatchEvent is one delivery on the channel returned by Watch. Exactly one of
// Config or Err is non-nil:
//   - Config != nil: reload succeeded; this is the new authoritative *Config.
//   - Err != nil: reload failed (parse, unknown key, validation, fs error).
//     The caller should keep using the previously good *Config it already
//     holds; the watcher does not retain or re-emit the prior good config.
type WatchEvent struct {
	Config *Config
	Err    error
}

// Watch starts a goroutine that monitors path for changes and emits a
// WatchEvent on each successful reload (or each parse/validate failure).
//
// The returned channel is closed when ctx is cancelled. It is intentionally
// unbuffered-ish (small buffer) — slow consumers may miss intermediate
// states, but will always see the next event after they drain.
//
// Watch handles three save patterns:
//
//  1. Direct write (echo > file): WRITE event(s).
//  2. Atomic rename (vim, gofmt, most modern editors): RENAME on the watched
//     path, followed by CREATE of a fresh file with the same name. We watch
//     the parent directory so we hear the CREATE.
//  3. Remove + recreate: REMOVE then CREATE. Same parent-dir trick.
//
// All three converge on a debounced reload of `path`.
func Watch(ctx context.Context, path string) (<-chan WatchEvent, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("watch: resolve path %s: %w", path, err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch: new fsnotify watcher: %w", err)
	}

	dir := filepath.Dir(abs)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch: add %s: %w", dir, err)
	}

	out := make(chan WatchEvent, 4)

	go runWatchLoop(ctx, w, abs, out)
	return out, nil
}

// runWatchLoop is the watcher's goroutine. It debounces fsnotify events for
// `target` and emits exactly one WatchEvent per quiet-period boundary.
func runWatchLoop(ctx context.Context, w *fsnotify.Watcher, target string, out chan<- WatchEvent) {
	defer close(out)
	defer w.Close()

	var (
		mu    sync.Mutex
		timer *time.Timer
	)

	fire := func() {
		mu.Lock()
		timer = nil
		mu.Unlock()

		cfg, err := Load(target)
		// Load() returns (Defaults(), nil) when the file is missing. Inside
		// Watch() that's almost certainly an editor mid-rename rather than a
		// genuine "user wants defaults", so we report it as an error and let
		// the next CREATE event kick a real reload.
		if err == nil {
			if _, statErr := os.Stat(target); errors.Is(statErr, os.ErrNotExist) {
				select {
				case out <- WatchEvent{Err: fmt.Errorf("config file %s vanished", target)}:
				case <-ctx.Done():
				}
				return
			}
		}
		ev := WatchEvent{}
		if err != nil {
			ev.Err = err
		} else {
			ev.Config = cfg
		}
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	schedule := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(debounceInterval, fire)
	}

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			if timer != nil {
				timer.Stop()
			}
			mu.Unlock()
			return

		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// We watch the parent dir, so filter to the file we care about.
			if filepath.Clean(ev.Name) != target {
				continue
			}
			// Any of WRITE / CREATE / RENAME / REMOVE / CHMOD can indicate
			// the user finished saving; debounce and reload after the burst.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove|fsnotify.Chmod) != 0 {
				schedule()
			}

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			select {
			case out <- WatchEvent{Err: fmt.Errorf("fsnotify: %w", err)}:
			case <-ctx.Done():
				return
			}
		}
	}
}
