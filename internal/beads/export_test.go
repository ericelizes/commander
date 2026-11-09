package beads

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const exportFixture = `{"id":"p-1","title":"Workstream","status":"in_progress","priority":2,"labels":["kind:objective"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"}
{"id":"p-1.1","title":"Older step","status":"closed","priority":2,"labels":["kind:objective","gate"],"created_at":"2026-10-03T00:00:00Z","updated_at":"2026-10-03T00:00:00Z","dependencies":[{"issue_id":"p-1.1","depends_on_id":"p-1","type":"parent-child"}],"comments":[{"author":"a","text":"first","created_at":"2026-10-03T01:00:00Z"},{"author":"b","text":"second","created_at":"2026-10-03T02:00:00Z"}]}
{"id":"p-1.2","title":"Newer step","status":"open","priority":2,"labels":["kind:objective"],"created_at":"2026-10-04T00:00:00Z","updated_at":"2026-10-04T00:00:00Z","dependencies":[{"issue_id":"p-1.2","depends_on_id":"p-1","type":"parent-child"},{"issue_id":"p-1.2","depends_on_id":"p-1.1","type":"blocks"}]}
{"id":"p-1.3","title":"Deleted","status":"tombstone","priority":2,"labels":["kind:objective"],"created_at":"2026-10-05T00:00:00Z","updated_at":"2026-10-05T00:00:00Z","dependencies":[{"issue_id":"p-1.3","depends_on_id":"p-1","type":"parent-child"}]}
{"id":"p-9","title":"Urgent task","status":"open","priority":0,"labels":["kind:task"],"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z"}
`

func writeExport(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(exportFixture), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExportReadsAnswerTheBoardWithoutBr(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	inner := &countingClient{MemClient: &MemClient{}}
	c := ExportReads(inner)

	got, err := c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	// br's order: priority, then newest first; tombstones are gone.
	if want := []string{"p-1.2", "p-1.1", "p-1"}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if got[2].DependentCount != 2 || got[0].DependencyCount != 2 {
		t.Fatalf("counts: p-1 dependents %d (want 2, the tombstone's edge not counted), p-1.2 deps %d (want 2)", got[2].DependentCount, got[0].DependencyCount)
	}

	d, err := c.Details(ctx, dir, []string{"p-1.1", "p-1.2", "p-1.3", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if d["p-1.1"].Parent != "p-1" || len(d["p-1.1"].Comments) != 2 || d["p-1.1"].Comments[0].Text != "first" {
		t.Fatalf("p-1.1 detail = %+v", d["p-1.1"])
	}
	if len(d["p-1.2"].Deps) != 2 {
		t.Fatalf("p-1.2 deps = %+v", d["p-1.2"].Deps)
	}
	if _, ok := d["p-1.3"]; ok {
		t.Fatal("a tombstone has details")
	}
	if inner.lists != 0 {
		t.Fatalf("br list ran %d times; the export answers these", inner.lists)
	}
}

func TestExportReadsFallBackToBrWhenTheExportCannotAnswer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	inner := &countingClient{MemClient: &MemClient{}}
	c := ExportReads(inner)

	// A filter the export does not reproduce.
	_, _ = c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}})
	// A database written after the export: the export is lagging.
	db := filepath.Join(dir, ".beads", "beads.db")
	touch(t, db)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	_, _ = c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	// No export at all.
	_, _ = c.List(ctx, t.TempDir(), ListOptions{Status: "all"})
	if inner.lists != 3 {
		t.Fatalf("br list ran %d times, want 3 fallbacks", inner.lists)
	}
}

// flushingClient rewrites the export when asked, as br sync --flush-only does.
type flushingClient struct {
	*countingClient
	flushes atomic.Int64
}

func (c *flushingClient) FlushExport(_ context.Context, dir string) error {
	c.flushes.Add(1)
	now := time.Now().Add(time.Second)
	return os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), now, now)
}

func TestExportReadsRefreshALaggingExportOnceInsteadOfFallingBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, ".beads", "beads.db"))
	inner := &flushingClient{countingClient: &countingClient{MemClient: &MemClient{}}}
	c := ExportReads(inner)

	got, err := c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d beads, err %v", len(got), err)
	}
	if inner.flushes.Load() != 1 || inner.lists != 0 {
		t.Fatalf("flushes %d (want 1), br lists %d (want 0)", inner.flushes.Load(), inner.lists)
	}
}

func TestExportLagIgnoresWal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeExport(t, dir)
	inner := &countingClient{MemClient: &MemClient{}}
	c := ExportReads(inner)

	// The WAL's mtime runs far ahead of the export on a busy project; the
	// database file does not, so the export is still current.
	touch(t, filepath.Join(dir, ".beads", "beads.db-wal"))
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(dir, ".beads", "issues.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, ".beads", "beads.db"))
	if err := os.Chtimes(filepath.Join(dir, ".beads", "beads.db"), old, old); err != nil {
		t.Fatal(err)
	}
	got, err := c.List(ctx, dir, ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil || len(got) != 3 {
		t.Fatalf("list = %d beads, err %v", len(got), err)
	}
	if inner.lists != 0 {
		t.Fatalf("br list ran %d times; a WAL ahead of the export must not force a fallback", inner.lists)
	}
}
