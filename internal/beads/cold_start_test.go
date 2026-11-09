package beads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// showCountingFlusher counts the beads it is asked to show (bd show) and
// rewrites the export when flushed, as bd export does.
type showCountingFlusher struct {
	*MemClient
	shown   atomic.Int64
	flushes atomic.Int64
	// release, when set, holds each flush until it is closed (a slow bd export).
	release chan struct{}
	// failing makes every flush fail (bd export timing out or broken).
	failing atomic.Bool
	// flushCost and batchCost model bd export and one bd show batch of 20.
	flushCost, batchCost time.Duration
}

func (c *showCountingFlusher) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	c.shown.Add(int64(len(ids)))
	time.Sleep(time.Duration((len(ids)+19)/20) * c.batchCost)
	return c.MemClient.Details(ctx, dir, ids)
}

func (c *showCountingFlusher) FlushExport(ctx context.Context, dir string) error {
	c.flushes.Add(1)
	time.Sleep(c.flushCost)
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.failing.Load() {
		return errors.New("bd export: signal: killed")
	}
	now := time.Now().Add(time.Second)
	return os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), now, now)
}

// The first load after a cmdrd start (il9.202.29): the export is behind the
// Dolt journal (bd in server mode never writes it) and the detail cache is cold.
// Answering it from bd sent every bead to bd show, one batch at a time: 6 min
// with no board for a 5,600-bead project. The first load waits for the export
// instead and reads no bead through bd show; later loads keep the background flush.
func TestColdDoltLoadWaitsForTheExportInsteadOfShowingEveryBead(t *testing.T) {
	ctx := context.Background()
	dir, ids, j := coldProject(t)

	inner := &showCountingFlusher{MemClient: &MemClient{}}
	c := ExportReads(NewCached(inner)) // as cmdrd wires its display reads
	defer StopBackground(c)

	got, err := c.Details(ctx, dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if n := inner.shown.Load(); n != 0 {
		t.Fatalf("cold load showed %d beads through bd; want the refreshed export", n)
	}
	if inner.flushes.Load() != 1 || len(got) != len(ids) || got["demo-w.7.2"].Parent != "demo-w.7" {
		t.Fatalf("flushes=%d details=%d parent=%q", inner.flushes.Load(), len(got), got["demo-w.7.2"].Parent)
	}

	// A later write: this client has flushed, so the load answers from bd now
	// and the export is refreshed in the background (il9.202.25 unchanged).
	time.Sleep(flushEvery)
	fi, _ := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl"))
	appendJournal(t, j, fi.ModTime().Add(time.Second))
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for inner.flushes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if inner.flushes.Load() != 2 {
		t.Fatalf("flushes=%d, want the warm load's background flush", inner.flushes.Load())
	}
}

// coldProject writes a project as a cmdrd finds it at start: a closed
// workstream with closed nested workstreams in an export an hour old, and a
// Dolt journal written after it (bd in server mode never writes the export).
func coldProject(t *testing.T) (dir string, ids []string, journal string) {
	return coldProjectOf(t, 30)
}

func coldProjectOf(t *testing.T, mids int) (dir string, ids []string, journal string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A closed workstream with closed nested workstreams under it.
	var lines []string
	add := func(id, parent, status string) {
		ids = append(ids, id)
		dep := ""
		if parent != "" {
			dep = fmt.Sprintf(`,"dependencies":[{"issue_id":%q,"depends_on_id":%q,"type":"parent-child"}]`, id, parent)
		}
		lines = append(lines, fmt.Sprintf(`{"id":%q,"title":"Step %s","status":%q,"priority":2,"labels":["kind:objective","kind:workstream"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"%s}`, id, id, status, dep))
	}
	add("demo-w", "", "closed")
	for i := 1; i <= mids; i++ {
		mid := fmt.Sprintf("demo-w.%d", i)
		add(mid, "demo-w", "closed")
		for j := 1; j <= 3; j++ {
			add(fmt.Sprintf("%s.%d", mid, j), mid, "closed")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour) // written by the cmdrd before the restart
	setExportTime(t, dir, old)
	j := doltJournal(t, dir, old.Add(time.Minute))

	return dir, ids, j
}

// Loads beside the first one (the health loop and API clients load while
// Poll's first load waits for the export): each waits for the one export in
// flight instead of reading every bead through bd show, and none starts a
// second export.
func TestColdDoltLoadsBesideTheFirstShareItsExport(t *testing.T) {
	ctx := context.Background()
	dir, ids, _ := coldProject(t)
	inner := &showCountingFlusher{MemClient: &MemClient{}, release: make(chan struct{})}
	c := ExportReads(NewCached(inner))
	defer StopBackground(c)

	const loads = 4
	errs := make(chan error, loads)
	for range loads {
		go func() {
			got, err := c.Details(ctx, dir, ids)
			if err == nil && len(got) != len(ids) {
				err = fmt.Errorf("details=%d, want %d", len(got), len(ids))
			}
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for inner.flushes.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // the others reach the wait
	if n := inner.shown.Load(); n != 0 {
		t.Fatalf("a load beside the first showed %d beads through bd while the export ran", n)
	}
	close(inner.release)
	for range loads {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := inner.shown.Load(); n != 0 || inner.flushes.Load() != 1 {
		t.Fatalf("shown=%d flushes=%d, want 0 shows and one shared export", n, inner.flushes.Load())
	}
}

// A load that gives up waiting (its own context ended) does not end the export
// the others wait for.
func TestColdDoltLoadCancelledWhileWaitingLeavesTheExportRunning(t *testing.T) {
	dir, ids, _ := coldProject(t)
	inner := &showCountingFlusher{MemClient: &MemClient{}, release: make(chan struct{})}
	c := ExportReads(NewCached(inner))
	defer StopBackground(c)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Details(ctx, dir, ids) // falls back once cancelled; the result does not matter
	}()
	for inner.flushes.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	close(inner.release)
	got, err := c.Details(context.Background(), dir, ids)
	if err != nil || len(got) != len(ids) || inner.flushes.Load() != 1 {
		t.Fatalf("err=%v details=%d flushes=%d, want the first export's result (ids %d)", err, len(got), inner.flushes.Load(), len(ids))
	}
}

// An export that keeps failing (bd export killed or timing out) must neither
// make every load wait for it nor send every bead through bd show: after the
// first failure loads answer from the export on disk, flagged as a partial
// (degraded) read naming the failure, and the export is retried in the
// background with backoff until it works.
func TestColdDoltLoadWithAFailingExportAnswersDegradedWithoutAStorm(t *testing.T) {
	ctx := context.Background()
	dir, ids, _ := coldProject(t)
	inner := &showCountingFlusher{MemClient: &MemClient{}}
	inner.failing.Store(true)
	c := ExportReads(NewCached(inner))
	defer StopBackground(c)

	for i := range 5 {
		start := time.Now()
		got, err := c.Details(ctx, dir, ids)
		var partial *PartialDetailsError
		if !errors.As(err, &partial) || !strings.Contains(err.Error(), "export") {
			t.Fatalf("load %d: err=%v, want a partial read naming the failing export", i, err)
		}
		if len(got) != len(ids) || got["demo-w.7.2"].Parent != "demo-w.7" {
			t.Fatalf("load %d: details=%d, want every bead from the export on disk", i, len(got))
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("load %d took %v", i, took)
		}
	}
	if n := inner.shown.Load(); n != 0 {
		t.Fatalf("failing export: %d beads went through bd show", n)
	}
	if n := inner.flushes.Load(); n > 2 {
		t.Fatalf("flushes=%d over five quick loads, want backoff", n)
	}

	// The export works again: a background retry (after its backoff) warms the
	// project and loads answer cleanly from the fresh export.
	inner.failing.Store(false)
	c.(*exportClient).retryNow(dir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := c.Details(ctx, dir, ids)
		if err == nil && len(got) == len(ids) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("export never recovered: err=%v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := inner.shown.Load(); n != 0 {
		t.Fatalf("recovered: %d beads went through bd show", n)
	}
}

// retryNow ends dir's backoff, as if its retry time had come.
func (c *exportClient) retryNow(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.failed[dir]; ok {
		f.retryAt = time.Time{}
		c.failed[dir] = f
	}
}

// The cold first load of a cmdrd start on a synthetic 5,600-bead project, with
// four loads arriving within 300ms (Poll, the health loop, API clients) and
// with the export failing. Costs model bd export (5s) and bd show (one batch of
// 20 beads ~1.3s, as a busy Dolt project) at 1/10 scale. Measurement only:
// COMMANDER_COLD_BENCH=1 go test ./internal/beads -run TestColdLoadBench -v
func TestColdLoadBench(t *testing.T) {
	if os.Getenv("COMMANDER_COLD_BENCH") == "" {
		t.Skip("measurement; set COMMANDER_COLD_BENCH=1")
	}
	for _, failing := range []bool{false, true} {
		dir, ids, _ := coldProjectOf(t, 1400) // 1 + 1400*4 = 5,601 beads
		inner := &showCountingFlusher{MemClient: &MemClient{}, flushCost: 500 * time.Millisecond, batchCost: 130 * time.Millisecond}
		inner.failing.Store(failing)
		c := ExportReads(NewCached(inner))
		start := time.Now()
		var slowest atomic.Int64
		done := make(chan struct{}, 4)
		for i := range 4 {
			go func() {
				time.Sleep(time.Duration(i) * 100 * time.Millisecond)
				c.Details(context.Background(), dir, ids)
				took := int64(time.Since(start))
				for cur := slowest.Load(); took > cur && !slowest.CompareAndSwap(cur, took); cur = slowest.Load() {
				}
				done <- struct{}{}
			}()
		}
		for range 4 {
			<-done
		}
		StopBackground(c)
		t.Logf("failing export=%v: all four loads answered in %v, %d beads through bd show, %d exports", failing, time.Duration(slowest.Load()).Round(time.Millisecond), inner.shown.Load(), inner.flushes.Load())
	}
}
