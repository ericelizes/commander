package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// Writes are not queued: concurrent callers on one project overlap.
func TestWrite_ConcurrentWritesOfOneProjectOverlap(t *testing.T) {
	var inside, peak atomic.Int32
	ts := opsServer(t, func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		n := inside.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		inside.Add(-1)
		return nil, nil, nil
	}, nil)
	dir := projectDir(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"close", "x"}})
		}()
	}
	wg.Wait()
	if peak.Load() < 4 {
		t.Fatalf("peak concurrency %d: writes were queued", peak.Load())
	}
}

func opsServer(t *testing.T, run WriteRunner, batch BatchRunner) *httptest.Server {
	t.Helper()
	src := &fakeSource{}
	src.set("a")
	s := New(Options{Token: testToken, Source: src.load, Write: run, Batch: batch})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// Several ops in one request reach bd as one batch script, in one call.
func TestWrite_OpsAreOneTransaction(t *testing.T) {
	var scripts []string
	var ran atomic.Int32
	ts := opsServer(t, func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		ran.Add(1)
		return nil, nil, nil
	}, func(ctx context.Context, dir, script string) ([]byte, []byte, error) {
		scripts = append(scripts, script)
		return nil, nil, nil
	})
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Atomic: true, Ops: [][]string{{"close", "x-1"}, {"create", "Next step"}, {"update", "x-2", "--assignee", "a"}}})
	if out.Exit != 0 || !out.Atomic || out.Applied != 3 || len(scripts) != 1 || ran.Load() != 0 {
		t.Fatalf("out=%+v scripts=%v ran=%d", out, scripts, ran.Load())
	}
}

func TestWrite_OpsAllOrNothing(t *testing.T) {
	var ran atomic.Int32
	ts := opsServer(t, func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		ran.Add(1)
		return nil, nil, nil
	}, func(ctx context.Context, dir, script string) ([]byte, []byte, error) {
		return nil, []byte("line 2"), os.ErrNotExist
	})
	dir := projectDir(t)
	// The batch fails: nothing applied, nothing retried one by one.
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Atomic: true, Ops: [][]string{{"close", "x-1"}, {"close", "nope"}}})
	if out.Exit == 0 || out.Applied != 0 || ran.Load() != 0 {
		t.Fatalf("out=%+v ran=%d", out, ran.Load())
	}
	// Atomic asked, but a label has no transaction: refused before anything runs.
	_, out = postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Atomic: true, Ops: [][]string{{"close", "x-1"}, {"label", "add", "x-1", "a"}}})
	if out.Exit == 0 || out.Applied != 0 || ran.Load() != 0 {
		t.Fatalf("out=%+v ran=%d", out, ran.Load())
	}
	// A list holding a read is refused whole.
	if resp, _ := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Ops: [][]string{{"close", "x"}, {"list"}}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("read in ops: %d", resp.StatusCode)
	}
}

// Through the real beads client with the door on: Txn reaches the server.
func TestWrite_ClientTxnThroughTheDoor(t *testing.T) {
	var scripts []string
	ts := opsServer(t, nil, func(ctx context.Context, dir, script string) ([]byte, []byte, error) {
		scripts = append(scripts, script)
		return nil, nil, nil
	})
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	os.WriteFile(filepath.Join(home, "api-url"), []byte(ts.URL), 0o600)
	os.WriteFile(filepath.Join(home, "api-token"), []byte(testToken), 0o600)
	resp, err := beads.Txn(context.Background(), projectDir(t), [][]string{{"close", "x-1"}, {"update", "x-2", "--status", "open"}}, true)
	if err != nil || !resp.Atomic || len(scripts) != 1 || scripts[0] != "update x-1 status=closed\nupdate x-2 status=open\n" {
		t.Fatalf("resp=%+v err=%v scripts=%q", resp, err, scripts)
	}
}

func TestWrite_TokenInQueryIsRefused(t *testing.T) {
	ts := opsServer(t, nil, nil)
	body, _ := json.Marshal(beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "x"}})
	resp, err := http.Post(ts.URL+beads.DoorPath+"?token="+testToken, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWrite_TooManyOpsAreRefused(t *testing.T) {
	ts := opsServer(t, nil, nil)
	var ops [][]string
	for i := 0; i <= beads.MaxOps; i++ {
		ops = append(ops, []string{"close", "x"})
	}
	if resp, _ := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: ops}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// The deadline covers the whole request: operations after it do not start.
func TestWrite_DeadlineStopsLaterOps(t *testing.T) {
	old := writeDeadline
	writeDeadline = 50 * time.Millisecond
	defer func() { writeDeadline = old }()
	var ran atomic.Int32
	ts := opsServer(t, func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		ran.Add(1)
		time.Sleep(120 * time.Millisecond)
		return nil, nil, nil
	}, nil)
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: [][]string{{"label", "add", "x", "a"}, {"label", "add", "x", "b"}}})
	if out.Exit == 0 || out.Applied != 1 || ran.Load() != 1 {
		t.Fatalf("out=%+v ran=%d", out, ran.Load())
	}
}

func TestWrite_SettledIsFalseWhenTheLoadKeptLastGood(t *testing.T) {
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		return livedata.Snapshot{PreserveVersion: true}
	}, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) { return nil, nil, nil }})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "x"}})
	if out.Exit != 0 || out.Settled {
		t.Fatalf("out=%+v", out)
	}
}
