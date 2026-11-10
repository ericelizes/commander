package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise actual 20-id show scheduling: the first batch completes and the
// second outlives the invariant budget. Concurrent display misses must coalesce
// independently, and the next invariant pass must resume after the first batch.
func TestIsolatedIncrementalRetainsProgressWithoutBlockingDisplay(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	var all []Bead
	var ids []string
	var export []byte
	for i := 0; i < 6219; i++ {
		id := fmt.Sprintf("demo-%d", i)
		all = append(all, Bead{ID: id, Status: "open"})
		ids = append(ids, id)
		raw, _ := json.Marshal(map[string]any{"id": id, "status": "open"})
		export = append(append(export, raw...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), export, 0600); err != nil {
		t.Fatal(err)
	}
	list, _ := json.Marshal(all)
	blocked := make(chan struct{})
	var batches atomic.Int64
	var resumed atomic.Bool
	var firstBatchRepeats atomic.Int64
	cmd := &cmdClient{run: func(ctx context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
		if args[0] == "list" {
			return list, nil, nil
		}
		if args[0] != "show" {
			t.Errorf("unexpected command %v", args)
			return nil, nil, errors.New("unexpected command")
		}
		n := batches.Add(1)
		if n == 2 {
			close(blocked)
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		if resumed.Load() && args[1] == ids[0] {
			firstBatchRepeats.Add(1)
		}
		var out []map[string]any
		for _, id := range args[1:] {
			if id[0] == '-' {
				break
			}
			out = append(out, map[string]any{"id": id})
		}
		raw, _ := json.Marshal(out)
		return raw, nil, nil
	}}
	display := NewCached(cmd)
	inv := IsolatedIncremental(display)
	ctx, cancel := context.WithCancel(WithReadCaller(context.Background(), "invariant-test"))
	done := make(chan error, 1)
	go func() { _, err := inv.Details(ctx, dir, ids); done <- err }()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("invariant never entered second batch")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := display.Details(ctx, dir, ids)
			if err != nil || len(got) != len(ids) {
				t.Errorf("display blocked by invariant: %d %v", len(got), err)
			}
		}()
	}
	wg.Wait()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("invariant err=%v", err)
	}
	for _, want := range []string{"caller=invariant-test", "requested=6219", "stale=6219", "show_batches=2", "reason=cold", "noExport=true", "wait=", "hold=", "context canceled"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("diagnostics lack %q: %s", want, logs.String())
		}
	}
	if batches.Load() != 2 {
		t.Fatalf("cancel launched more batches: %d", batches.Load())
	}
	resumed.Store(true)
	got, err := inv.Details(context.Background(), dir, ids)
	if err != nil || len(got) != len(ids) || firstBatchRepeats.Load() != 0 {
		t.Fatalf("resume=%d err=%v repeated completed batch=%d", len(got), err, firstBatchRepeats.Load())
	}
	before := batches.Load()
	if _, err := inv.Details(context.Background(), dir, ids); err != nil || batches.Load() != before {
		t.Fatalf("warm invariant reread storm: %d -> %d %v", before, batches.Load(), err)
	}
	// Enforcement bypasses the display and invariant detail caches.
	if _, err := FreshDetails(context.Background(), display, dir, ids[:1]); err != nil || batches.Load() != before+1 {
		t.Fatalf("action did not read DB: %v", err)
	}
	t.Logf("6219 rows: display completed while invariant held; cancellation stopped at 2 batches; resume skipped completed batch; warm pass 0 batches; action fresh")
}
