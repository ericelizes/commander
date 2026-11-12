package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBrTimeoutKillsOnlyItsCall(t *testing.T) {
	script := filepath.Join(t.TempDir(), "br")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	defer func(old time.Duration) { brCallTimeout = old }(brCallTimeout)
	brCallTimeout = 500 * time.Millisecond
	ctx := context.Background()
	start := time.Now()
	_, _, err := defaultRunInDir(ctx, "", script)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL || time.Since(start) < brCallTimeout || time.Since(start) > brCallTimeout+10*time.Second || ctx.Err() != nil {
		t.Fatalf("timeout err=%v elapsed=%v parent=%v", err, time.Since(start), ctx.Err())
	}
}

func TestRetryIdempotentUpdatesOnly(t *testing.T) {
	for _, message := range []string{"signal: killed", "database is locked", "SQLITE_BUSY"} {
		t.Run(message, func(t *testing.T) {
			calls := 0
			c := &cmdClient{run: func(context.Context, string, string, ...string) ([]byte, []byte, error) {
				calls++
				if calls%2 == 1 {
					return nil, []byte(message), errors.New(message)
				}
				return nil, nil, nil
			}}
			if err := c.SetLabel(context.Background(), "", "p-1", "owner:operator-x", true); err != nil || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if _, err := c.Create(context.Background(), "", "Example", CreateOptions{}); err == nil || calls != 3 {
				t.Fatalf("create retried: calls=%d err=%v", calls, err)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		c := &cmdClient{run: func(context.Context, string, string, ...string) ([]byte, []byte, error) {
			calls++
			return nil, nil, errors.New("signal: killed")
		}}
		_ = c.SetStatus(ctx, "", "p-1", "open")
		if calls != 1 {
			t.Fatalf("canceled call retried %d times", calls)
		}
	})
}

func TestStateWriteSingleUpdate(t *testing.T) {
	var calls [][]string
	c := NewCached(&cmdClient{run: func(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		return nil, nil, nil
	}})
	w := NewStateWrite(c)
	_ = w.SetLabel(context.Background(), "", "p-1", "blocked", false)
	_ = w.SetLabel(context.Background(), "", "p-1", "owner:operator-x", true)
	_ = w.SetStatus(context.Background(), "", "p-1", "open")
	_ = w.SetStatus(context.Background(), "", "p-1", "in_progress")
	if len(calls) != 0 {
		t.Fatal("wrote before validation finished")
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "update p-1 --remove-label blocked --add-label owner:operator-x --status in_progress" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestLastGoodFingerprintMismatchRetriesListAndDetails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".beads", "last-touched")
	inner := &countingClient{MemClient: &MemClient{}}
	inner.Seed(Bead{ID: "p-1", Status: "open"})
	c := NewCached(inner)
	reads := 0
	err := ReadConsistent(context.Background(), dir, func() error {
		reads++
		bs, err := c.List(context.Background(), dir, ListOptions{})
		if err != nil {
			return err
		}
		if reads == 1 {
			if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
				return err
			}
		}
		_, err = c.Details(context.Background(), dir, []string{bs[0].ID})
		return err
	})
	if err != nil || reads != 2 || inner.lists != 3 { // list, change-key list, re-read list
		t.Fatalf("reads=%d lists=%d err=%v", reads, inner.lists, err)
	}
}

func TestFreshDetailsSeesDBEdgeBehindDisplayExport(t *testing.T) {
	// Exercise the legacy export/show contract independently of whichever CLI
	// (native bd, beads_rust, or a cutover fence) is installed on the host.
	t.Setenv("COMMANDER_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	step, gate := "sample-step", "sample-gate"
	// The process fixture represents the authoritative database after an edge
	// was added without refreshing the display export.
	binary := fakeBr(t, `test "$1" = show && test "$2" = sample-step && test "$3" = --json && test "$#" = 3 || exit 1
cat authoritative.json`)
	t.Setenv("COMMANDER_BEADS_BIN", binary)
	if err := os.WriteFile(filepath.Join(dir, "authoritative.json"), []byte(`[{"id":"sample-step","dependencies":[{"id":"sample-gate","dependency_type":"blocks"}]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	c := &cmdClient{run: defaultRunInDir}
	ctx := context.Background()
	path := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"`+step+`","dependencies":[]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cached := NewCached(c)
	display, err := cached.Details(ctx, dir, []string{step})
	if err != nil || len(display[step].Deps) != 0 {
		t.Fatalf("display=%v err=%v", display, err)
	}
	for _, client := range []Client{c, cached, NewStateWrite(cached), ForActions(cached)} {
		fresh, err := FreshDetails(ctx, client, dir, []string{step})
		if err != nil || len(fresh[step].Deps) != 1 || fresh[step].Deps[0].DependsOnID != gate {
			t.Fatalf("fresh %T=%v err=%v", client, fresh, err)
		}
	}
	t.Run("enforcement does not refresh bounded display", func(t *testing.T) {
		got, err := cached.Details(ctx, dir, []string{step})
		if err != nil || len(got[step].Deps) != 0 {
			t.Fatalf("display after DB-only blocks edge enforcement=%v err=%v", got, err)
		}
	})
	old := time.Now().Add(-maxExportAge - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// Populate a cached display value with the same fingerprint, but with its
	// original export age: expiry must work even when no file changes again.
	cached.forget(dir)
	key := "details:" + step
	cached.stamp[dir] = fingerprint(dir)
	cached.hits[dir] = map[string]any{key: cacheEntry[map[string]Detail]{value: display, at: old}}
	got, err := cached.Details(ctx, dir, []string{step})
	if err != nil || len(got[step].Deps) != 1 {
		t.Fatalf("expired display=%v err=%v", got, err)
	}
}

func TestReadRetryChangedDuringReadReturnsSentinelAfterBackoff(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	old := ReadRetryBackoff
	ReadRetryBackoff = func() time.Duration { return time.Millisecond }
	defer func() { ReadRetryBackoff = old }()
	path := filepath.Join(dir, ".beads", "last-touched")
	reads := 0
	err := ReadConsistent(context.Background(), dir, func() error {
		reads++
		return os.WriteFile(path, []byte(strings.Repeat("x", reads)), 0600) // size moves: Linux mtimes are coarse
	})
	if !errors.Is(err, ErrChangedDuringRead) || reads != 3 {
		t.Fatalf("reads=%d err=%v", reads, err)
	}
}

func TestReadRetryDeadlineKillIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		err   error
		calls int
	}{
		{fmt.Errorf("%w: signal: killed", context.DeadlineExceeded), 1},
		{errors.New("database is locked"), 2},
	} {
		calls := 0
		c := &cmdClient{run: func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
			calls++
			return nil, nil, tc.err
		}}
		if _, err := c.showOnce(context.Background(), t.TempDir(), []string{"p-1"}); err == nil || calls != tc.calls {
			t.Fatalf("%v: calls=%d err=%v, want %d calls", tc.err, calls, err, tc.calls)
		}
	}
}
