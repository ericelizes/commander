package livedata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// flushingMem is a bd project: its export is written only when asked
// (bd in server mode never writes it), and every write appends to the journal.
type flushingMem struct {
	*beads.MemClient
	dir     string
	flushes int
}

func (m *flushingMem) FlushExport(ctx context.Context, dir string) error {
	m.flushes++
	bs, err := m.MemClient.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	for _, b := range bs {
		row := map[string]any{"id": b.ID, "title": b.Title, "status": b.Status, "labels": b.Labels,
			"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z"}
		if b.ParentID != "" {
			row["dependencies"] = []beads.Dep{{IssueID: b.ID, DependsOnID: b.ParentID, Type: "parent-child"}}
		}
		line, _ := json.Marshal(row)
		f.Write(append(line, '\n'))
	}
	return nil
}

// il9.172.31: internal-close-objective writes the --then-* hold and then the
// close, two door writes back to back. The hold's settle load flushed the
// export; the close appended to the Dolt journal under 2s later, so the
// export was taken as current and every snapshot showed the closed task
// open (and unheld, once its Operator was gone) until the project's next
// write, though close had answered closed:true. A journal write the export
// has not seen must never be served from the export.
func TestCloseRightAfterItsHoldIsNotReadOpenFromTheExport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	journal := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	appendJournal := func(at time.Time) {
		t.Helper()
		f, err := os.OpenFile(journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("commit;")
		f.Close()
		if err := os.Chtimes(journal, at, at); err != nil {
			t.Fatal(err)
		}
	}
	st := openStore(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &flushingMem{MemClient: &beads.MemClient{}, dir: dir}
	mem.Seed(beads.Bead{ID: "w", Title: "Release sample", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", ParentID: "w", Title: "Ship the queue", Status: "in_progress", Labels: []string{"kind:objective", "waiting:1:operator:op-x-w-3"}})
	mem.Seed(beads.Bead{ID: "w.2", ParentID: "w.1", Title: "Review the queue", Status: "in_progress", Labels: []string{"kind:objective", "kind:task", "waiting:1:until:1999999999:manager-close"}})
	mem.Seed(beads.Bead{ID: "w.3", ParentID: "w.1", Title: "Fix the queue", Status: "in_progress", Labels: []string{"kind:objective", "kind:task", "waiting:1:operator:op-x-w-3"}})
	appendJournal(time.Now().Add(-time.Minute))
	if err := mem.FlushExport(ctx, dir); err != nil {
		t.Fatal(err)
	}
	src := &Source{Deps: Deps{Store: st, Beads: beads.ExportReads(beads.NewCached(mem))}}
	if s := src.Snapshot(ctx); s.Degraded != nil || len(s.Workstreams) != 1 {
		t.Fatalf("first: %+v", s.Degraded)
	}

	// The hold write (--then-waiting on the parent step), settled by a load that flushes.
	if err := mem.SetLabel(ctx, dir, "w.1", "waiting:2:operator:op-x-w-3", true); err != nil {
		t.Fatal(err)
	}
	appendJournal(time.Now())
	src.Snapshot(ctx)
	fi, err := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The close: its journal append lands 1s after that export.
	if err := mem.Close(ctx, dir, "w.2"); err != nil {
		t.Fatal(err)
	}
	appendJournal(fi.ModTime().Add(time.Second))

	s := src.Snapshot(ctx)
	step := snapStepByID(s, "w.2")
	if step == nil || step.Status != "closed" {
		t.Fatalf("closed task read as %+v (flushes %d): served from a stale export", step, mem.flushes)
	}
	for _, v := range SnapshotViolations(s) {
		if v.BeadID == "w.2" {
			t.Fatalf("closed task reported unheld: %+v", v)
		}
	}
	// The successor's hold is intact.
	if w1 := snapStepByID(s, "w.1"); w1 == nil || w1.Waiting == "" {
		t.Fatalf("successor wait lost: %+v", w1)
	}
}

func snapStepByID(s Snapshot, id string) *SnapStep {
	var walk func([]SnapStep) *SnapStep
	walk = func(steps []SnapStep) *SnapStep {
		for i := range steps {
			if steps[i].BeadID == id {
				return &steps[i]
			}
			if f := walk(steps[i].Children); f != nil {
				return f
			}
		}
		return nil
	}
	for _, w := range s.Workstreams {
		if f := walk(w.Steps); f != nil {
			return f
		}
	}
	return nil
}
