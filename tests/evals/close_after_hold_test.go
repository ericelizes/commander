package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// exportingMem is a bd project in Dolt server mode: the export is written
// only when cmdrd flushes it.
type exportingMem struct{ *beads.MemClient }

func (m exportingMem) FlushExport(ctx context.Context, dir string) error {
	bs, err := m.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return err
	}
	var out []byte
	for _, b := range bs {
		row := map[string]any{"id": b.ID, "title": b.Title, "status": b.Status, "labels": b.Labels,
			"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-01T00:00:00Z"}
		if b.ParentID != "" {
			row["dependencies"] = []beads.Dep{{IssueID: b.ID, DependsOnID: b.ParentID, Type: "parent-child"}}
		}
		line, _ := json.Marshal(row)
		out = append(append(out, line...), '\n')
	}
	return os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), out, 0o600)
}

// il9.172.31: internal-close-objective's --then-* hold write and its close
// land back to back; the hold's settle flushed the export and the close's
// journal append came within 2s of it. The board, read as cmdrd reads it
// (export first), must show the task closed and nothing unheld right away.
func TestCloseAfterHoldScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/close-after-hold.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Hold                    struct{ Bead, Label string }
		Close                   string
		CloseJournalAfterExport string
		Beads                   []beads.Bead
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	lag, err := time.ParseDuration(sc.CloseJournalAfterExport)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	journal := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	commit := func(at time.Time) {
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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := exportingMem{&beads.MemClient{}}
	for _, b := range sc.Beads {
		mem.Seed(b)
	}
	commit(time.Now().Add(-time.Minute))
	if err := mem.FlushExport(ctx, dir); err != nil {
		t.Fatal(err)
	}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: beads.ExportReads(beads.NewCached(mem))}}
	src.Snapshot(ctx)

	if err := mem.SetLabel(ctx, dir, sc.Hold.Bead, sc.Hold.Label, true); err != nil {
		t.Fatal(err)
	}
	commit(time.Now())
	src.Snapshot(ctx) // the hold's settle load: flushes the export
	fi, err := os.Stat(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Close(ctx, dir, sc.Close); err != nil {
		t.Fatal(err)
	}
	commit(fi.ModTime().Add(lag))

	s := src.Snapshot(ctx)
	var status string
	var walk func([]livedata.SnapStep)
	walk = func(steps []livedata.SnapStep) {
		for _, x := range steps {
			if x.BeadID == sc.Close {
				status = x.Status
			}
			walk(x.Children)
		}
	}
	for _, w := range s.Workstreams {
		walk(w.Steps)
	}
	if status != "closed" {
		t.Fatalf("%s reads %q right after its close: the board served a stale export", sc.Close, status)
	}
	for _, v := range livedata.SnapshotViolations(s) {
		if v.BeadID == sc.Close {
			t.Fatalf("closed task reported unheld: %+v", v)
		}
	}
}
