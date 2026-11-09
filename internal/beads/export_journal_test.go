package beads

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// doltJournal makes the bd Dolt journal of dir with mtime at.
func doltJournal(t *testing.T, dir string, at time.Time) string {
	t.Helper()
	p := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendJournal(t *testing.T, p string, at time.Time) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("+commit"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func setExportTime(t *testing.T, dir string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), at, at); err != nil {
		t.Fatal(err)
	}
}

// waitFlushes waits for the background flushes to reach n.
func waitFlushes(t *testing.T, c *flushingClient, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.flushes.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("flushes = %d, want %d", c.flushes.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	// The flush records what it saw once FlushExport returns.
	time.Sleep(20 * time.Millisecond)
}

// A bd write that lands within exportLagMax of the last export (the close
// that follows a --then-* hold write, il9.172.31) used to be served from the
// export as if it had not happened, until the project's next write: close
// returned closed:true and graph check read the bead open and unheld. A Dolt
// journal newer than the export, by any amount, means the export is behind:
// the read goes to bd at once and the export is refreshed in the background
// (waiting for it made a busy project's loads pay seconds of export, il9.202.25).
func TestExportJournalWriteJustAfterTheExportIsNotServedStale(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	e := time.Now().Add(-time.Minute)
	setExportTime(t, dir, e)
	doltJournal(t, dir, e.Add(time.Second)) // the close, 1s after the hold's export
	inner := &flushingClient{countingClient: &countingClient{MemClient: &MemClient{}}}
	c := ExportReads(inner)

	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 1 {
		t.Fatalf("lists = %d: a journal 1s ahead of the export was served from the stale export", inner.lists)
	}
	waitFlushes(t, inner, 1)
	// The refreshed export answers the next read.
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 1 || inner.flushes.Load() != 1 {
		t.Fatalf("after the flush: lists=%d flushes=%d, want the export", inner.lists, inner.flushes.Load())
	}
}

// After this client's own flush, a journal append it did not see (a write that
// committed during or just after the export) makes the export behind even when
// the export's mtime is later than the append; an untouched journal does not.
func TestExportJournalChangedSinceOurFlushIsBehind(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	e := time.Now().Add(-time.Minute)
	setExportTime(t, dir, e)
	j := doltJournal(t, dir, e.Add(5*time.Second)) // far ahead: flushed under the old rule too
	inner := &flushingClient{countingClient: &countingClient{MemClient: &MemClient{}}}
	c := ExportReads(inner)

	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	waitFlushes(t, inner, 1)
	if inner.lists != 1 {
		t.Fatalf("first read: flushes=%d lists=%d", inner.flushes.Load(), inner.lists)
	}
	// Journal unchanged: the export stays the answer, no flush, no br list.
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.flushes.Load() != 1 || inner.lists != 1 {
		t.Fatalf("unchanged journal: flushes=%d lists=%d", inner.flushes.Load(), inner.lists)
	}
	// A write lands with an mtime BEFORE the export the flush stamped.
	fi, _ := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl"))
	appendJournal(t, j, fi.ModTime().Add(-500*time.Millisecond))
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.lists != 2 {
		t.Fatalf("after an unseen journal append: flushes=%d lists=%d; the export was served stale", inner.flushes.Load(), inner.lists)
	}
}

// A journal older than the export (no write since) keeps the export.
func TestExportJournalOlderThanTheExportKeepsIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	e := time.Now().Add(-time.Minute)
	setExportTime(t, dir, e)
	doltJournal(t, dir, e.Add(-time.Second))
	inner := &flushingClient{countingClient: &countingClient{MemClient: &MemClient{}}}
	c := ExportReads(inner)
	if _, err := c.List(ctx, dir, ListOptions{Status: "all"}); err != nil {
		t.Fatal(err)
	}
	if inner.flushes.Load() != 0 || inner.lists != 0 {
		t.Fatalf("flushes=%d lists=%d for a current export", inner.flushes.Load(), inner.lists)
	}
}
