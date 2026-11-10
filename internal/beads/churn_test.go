package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// On a Dolt project the export is rewritten by every flush, ours included, and
// the SQLite files are leftovers: neither is a write. Counting them made a busy
// project's own flush drop the cache and fail ReadConsistent (il9.202.25.1).
func TestFingerprintOnDoltFollowsOnlyTheJournal(t *testing.T) {
	dir := t.TempDir()
	writeExport(t, dir)
	j := doltJournal(t, dir, time.Now().Add(-time.Minute))
	before := fingerprint(dir)
	if before == "" {
		t.Fatal("no fingerprint")
	}
	rewrite := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(exportFixture+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		touch(t, filepath.Join(dir, ".beads", "beads.db"))
		touch(t, filepath.Join(dir, ".beads", "last-touched"))
	}
	rewrite()
	if got := fingerprint(dir); got != before {
		t.Fatalf("an export flush changed the fingerprint: %q -> %q", before, got)
	}
	reads := 0
	if err := ReadConsistent(context.Background(), dir, func() error { reads++; rewrite(); return nil }); err != nil || reads != 1 {
		t.Fatalf("a read that flushed the export: reads=%d err=%v, want one attempt", reads, err)
	}
	appendJournal(t, j, time.Now())
	if got := fingerprint(dir); got == before {
		t.Fatal("a journal append kept the fingerprint")
	}

	// No journal (a br project): the files still count.
	plain := t.TempDir()
	writeExport(t, plain)
	fp := fingerprint(plain)
	touch(t, filepath.Join(plain, ".beads", "issues.jsonl"))
	if fingerprint(plain) == fp {
		t.Fatal("without a journal a rewritten export must change the fingerprint")
	}
}

// The board's label filters of all statuses are answered from one cached list:
// same beads, same order, as br/bd filter after they sort.
func TestCachedLabelListsShareOneListCall(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	doltJournal(t, dir, time.Now().Add(-time.Minute))
	mem := &MemClient{Prefix: "ex"}
	ws, _ := mem.Create(ctx, dir, "Workstream", CreateOptions{Labels: []string{"kind:objective"}})
	_, _ = mem.Create(ctx, dir, "Step", CreateOptions{Parent: ws, Labels: []string{"kind:objective", "kind:task"}})
	_, _ = mem.Create(ctx, dir, "Gate", CreateOptions{Parent: ws, Labels: []string{"gate"}})
	_, _ = mem.Create(ctx, dir, "Card", CreateOptions{Parent: ws, Labels: []string{"kind:gate"}})
	_, _ = mem.Create(ctx, dir, "Other", CreateOptions{})
	inner := &countingClient{MemClient: mem}
	c := NewCached(inner)
	for _, opts := range []ListOptions{
		{Labels: []string{"kind:objective"}, Status: "all"},
		{LabelAny: []string{"gate", "kind:gate"}, Status: "all"},
		{Labels: []string{"kind:objective", "kind:task"}, Status: "all"},
		{Labels: []string{"kind:objective"}, LabelAny: []string{"kind:task", "gate"}, Status: "all"},
	} {
		got, err := c.List(ctx, dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := mem.List(ctx, dir, opts)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v: got %v, want %v", opts, got, want)
		}
		if len(got) > 0 {
			got[0].Labels[0] = "edited" // callers edit what they get
		}
	}
	if inner.lists != 1 {
		t.Fatalf("lists = %d, want one list of every status", inner.lists)
	}
	// Other filters still go to br.
	if _, err := c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}}); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 2 {
		t.Fatalf("lists = %d after an open-only list, want 2", inner.lists)
	}
}

// heldListClient holds every List until released.
type heldListClient struct {
	*MemClient
	release chan struct{}
	calls   atomic.Int64
}

func (c *heldListClient) List(ctx context.Context, dir string, opts ListOptions) ([]Bead, error) {
	c.calls.Add(1)
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.MemClient.List(ctx, dir, opts)
}

// Loads that miss together (the poll, the health loop, a settle) share one bd
// call; a waiter whose leader was cancelled reads on its own.
func TestCachedMissesShareOneCall(t *testing.T) {
	dir := t.TempDir()
	doltJournal(t, dir, time.Now().Add(-time.Minute))
	mem := &MemClient{}
	_, _ = mem.Create(context.Background(), dir, "A", CreateOptions{Labels: []string{"kind:objective"}})
	inner := &heldListClient{MemClient: mem, release: make(chan struct{})}
	c := NewCached(inner)

	leaderCtx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := c.List(leaderCtx, dir, ListOptions{Status: "all"})
		leader <- err
	}()
	waitCalls := func(n int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for inner.calls.Load() < n {
			if time.Now().After(deadline) {
				t.Fatalf("calls = %d, want %d", inner.calls.Load(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitCalls(1)
	var wg sync.WaitGroup
	results := make([][]Bead, 3)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = c.List(context.Background(), dir, ListOptions{Status: "all"})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("calls = %d while one ran, want 1", got)
	}
	// The leader is cancelled: the waiters may not inherit its error.
	cancel()
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader: %v", err)
	}
	waitCalls(2)
	close(inner.release)
	wg.Wait()
	for i, r := range results {
		if len(r) != 1 {
			t.Fatalf("waiter %d got %v", i, r)
		}
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want the cancelled one and one shared retry", got)
	}
}

// blockingFlusher never finishes its export until released.
type blockingFlusher struct {
	*countingClient
	release chan struct{}
	flushes atomic.Int64
}

func (c *blockingFlusher) FlushExport(context.Context, string) error {
	c.flushes.Add(1)
	<-c.release
	return nil
}

// A Dolt load after the first never waits for the export it asks for: bd answers now.
func TestDoltLoadDoesNotWaitForTheExport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	e := time.Now().Add(-time.Minute)
	setExportTime(t, dir, e)
	doltJournal(t, dir, e.Add(time.Second))
	inner := &blockingFlusher{countingClient: &countingClient{MemClient: &MemClient{}}, release: make(chan struct{})}
	c := ExportReads(inner)
	warmExport(c, dir)
	defer func() { close(inner.release); StopBackground(c) }()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the load waited for the export")
	}
	// The flush starts in its own goroutine: wait for it, then check it stays one.
	deadline := time.Now().Add(10 * time.Second)
	for inner.flushes.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if inner.lists != 3 || inner.flushes.Load() != 1 {
		t.Fatalf("lists=%d flushes=%d, want bd for each read and one export running", inner.lists, inner.flushes.Load())
	}
}

// panicListClient blocks its first List until released, then panics; later calls answer.
type panicListClient struct {
	*MemClient
	calls   atomic.Int64
	release chan struct{}
}

func (c *panicListClient) List(ctx context.Context, dir string, o ListOptions) ([]Bead, error) {
	if c.calls.Add(1) == 1 {
		<-c.release
		panic("boom")
	}
	return c.MemClient.List(ctx, dir, o)
}

// A leader that panics leaves its followers a result of their own, not a crash.
func TestCachedLeaderPanicDoesNotCrashFollowers(t *testing.T) {
	dir := t.TempDir()
	doltJournal(t, dir, time.Now().Add(-time.Minute))
	inner := &panicListClient{MemClient: &MemClient{}, release: make(chan struct{})}
	c := NewCached(inner)
	led := make(chan any, 1)
	go func() {
		defer func() { led <- recover() }()
		_, _ = c.List(context.Background(), dir, ListOptions{Status: "all"})
	}()
	for inner.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	fol := make(chan error, 1)
	go func() {
		_, err := c.List(context.Background(), dir, ListOptions{Status: "all"})
		fol <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(inner.release)
	if r := <-led; r == nil {
		t.Fatal("leader did not panic")
	}
	select {
	case err := <-fol:
		if err != nil {
			t.Fatalf("follower: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower hung")
	}
}
