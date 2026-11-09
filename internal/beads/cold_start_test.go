package beads

import (
	"context"
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
}

func (c *showCountingFlusher) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	c.shown.Add(int64(len(ids)))
	return c.MemClient.Details(ctx, dir, ids)
}

func (c *showCountingFlusher) FlushExport(_ context.Context, dir string) error {
	c.flushes.Add(1)
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
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A closed workstream with closed nested workstreams under it.
	var lines []string
	var ids []string
	add := func(id, parent, status string) {
		ids = append(ids, id)
		dep := ""
		if parent != "" {
			dep = fmt.Sprintf(`,"dependencies":[{"issue_id":%q,"depends_on_id":%q,"type":"parent-child"}]`, id, parent)
		}
		lines = append(lines, fmt.Sprintf(`{"id":%q,"title":"Step %s","status":%q,"priority":2,"labels":["kind:objective","kind:workstream"],"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"%s}`, id, id, status, dep))
	}
	add("demo-w", "", "closed")
	for i := 1; i <= 30; i++ {
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
