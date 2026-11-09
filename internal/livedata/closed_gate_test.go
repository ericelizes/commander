package livedata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// An external Dolt close must invalidate the open gate snapshot even before
// an export flush. Retained needs-user and wait labels must not revive it.
func TestClosedGateDoltTransition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	journal := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(journal, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("before")
	st := openStore(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "w", Title: "Release sample", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: "w.1", ParentID: "w", Title: "Approve sample", Labels: []string{"kind:objective", "gate", "approver:user", beads.NeedsUserLabel, "waiting:1:main-ci:abcdef1234"}})
	mem.Seed(beads.Bead{ID: "w.2", ParentID: "w", Title: "Verify sample", Labels: []string{"kind:objective", "waiting:2:main-ci:abcdef1234"}})
	src := &Source{Deps: Deps{Store: st, Beads: beads.NewCached(mem)}}
	first := src.Snapshot(ctx)
	if first.Degraded != nil || len(first.Workstreams) != 1 {
		t.Fatalf("first: %+v", first)
	}
	if !strings.Contains(strings.Join(DiagramLines(Board(first, 100).Workstreams[0].Diagram), "\n"), "w.1#wait") {
		t.Fatal("fixture must reproduce the synthetic wait before close")
	}
	if len(first.NoPitch) != 1 {
		t.Fatalf("expected prior pitch: %+v", first.NoPitch)
	}
	if err := mem.Close(ctx, dir, "w.1"); err != nil {
		t.Fatal(err)
	}
	// No watched file has changed yet: reproduce the stale open display.
	stale := src.Snapshot(ctx)
	if stale.Workstreams[0].Steps[0].Status == "closed" {
		t.Fatal("fixture did not warm the cache")
	}
	write("after external close")
	next := src.Snapshot(ctx)
	if next.Degraded != nil || len(next.NoPitch) != 0 || len(next.NeedsYou) != 0 {
		t.Fatalf("closed ask survived: %+v", next)
	}
	d := Board(next, 100)
	gate := diagramStep(d.Workstreams[0].Diagram, "w.1")
	if gate == nil || gate.Kind != "done" {
		t.Fatalf("gate: %+v", gate)
	}
	text := strings.Join(DiagramLines(d.Workstreams[0].Diagram), "\n")
	if strings.Contains(text, "w.1#wait") || strings.Contains(d.Workstreams[0].Text, "pitch") || !strings.Contains(text, "CI on main") {
		t.Fatalf("display: %s\n%s", d.Workstreams[0].Text, text)
	}
}

func TestClosedNeedsUserRowsHaveNoAsk(t *testing.T) {
	for _, pitched := range []bool{true, false} {
		t.Run(fmt.Sprint("pitched=", pitched), func(t *testing.T) {
			row := ObjectiveRow{BeadID: "w.1", ParentBeadID: "w", ProjectID: 1, Status: "open",
				State: beads.StateNeedsUser, Gate: true, Approvers: []string{"user"}}
			if pitched {
				row.Ask = "Approve sample?"
				row.Decision = &store.Decision{Question: row.Ask, Context: "Prior review complete."}
			}
			d := Data{Projects: []store.Project{{ID: 1, Name: "example"}}, Objectives: []ObjectiveRow{
				{BeadID: "w", ProjectID: 1, Status: "open"}, row}}
			if len(Workstreams(d)) != 1 || len(Workstreams(d)[0].Steps) != 1 {
				t.Fatal("step must reach askLists under an open root")
			}
			asks, missing := askLists(d)
			if pitched && (len(asks) != 1 || len(missing) != 0) || !pitched && (len(asks) != 0 || len(missing) != 1) {
				t.Fatalf("open gate suppressed: asks=%+v missing=%+v", asks, missing)
			}
			d.Objectives[1].Status = "closed" // keep stale state and exact decision metadata
			asks, missing = askLists(d)
			if len(asks) != 0 || len(missing) != 0 {
				t.Fatalf("closed gate survived: asks=%+v missing=%+v", asks, missing)
			}
		})
	}
}
