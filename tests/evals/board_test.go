package evals

import (
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

// Exercise the shared display that the app and cmdr board consume, without
// starting agents or touching the live cockpit.
func TestBoardOpenCardScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/board-open-card.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Fixture    string
		Workstream string
		Cases      []struct {
			Name, Card, Kind string
			StepText         string `json:"step_text"`
			Lane             string
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	for _, c := range sc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			raw, err := os.ReadFile(sc.Fixture)
			if err != nil {
				t.Fatal(err)
			}
			var fx struct {
				Now      int64
				Snapshot livedata.Snapshot
			}
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatal(err)
			}
			var ws livedata.SnapWorkstream
			for _, w := range fx.Snapshot.Workstreams {
				if w.BeadID == sc.Workstream {
					ws = w
				}
			}
			if ws.BeadID == "" {
				t.Fatal("missing scenario workstream")
			}
			s := &ws.Steps[0].Children[0]
			fx.Snapshot.NeedsYou = nil
			switch c.Card {
			case "step":
				fx.Snapshot.NeedsYou = []livedata.NeedsYou{{Project: ws.Project, BeadID: s.BeadID, Title: s.Title, Workstream: ws.Title, TrackID: ws.Steps[0].BeadID}}
			case "gate":
				gate := livedata.SnapStep{BeadID: "w57.1.9", Title: "Approve login", State: "needs-user", Status: "open", Gate: true, Approvers: []string{"user"}, BlockedBy: []string{s.BeadID}}
				ws.Steps[0].Children = append(ws.Steps[0].Children, gate)
				fx.Snapshot.NeedsYou = []livedata.NeedsYou{{Project: ws.Project, BeadID: gate.BeadID, Title: gate.Title, Workstream: ws.Title, TrackID: ws.Steps[0].BeadID}}
			}
			fx.Snapshot.Workstreams = []livedata.SnapWorkstream{ws}
			d := livedata.Board(fx.Snapshot, fx.Now)
			row := d.Workstreams[0]
			if row.Kind != c.Kind || row.Tracks[0].Kind != c.Kind {
				t.Fatalf("row/track: %+v", row)
			}
			step := row.Workflow.Steps[0].Children[0]
			box := row.Diagram.Tracks[0].Steps[0]
			if step.Text != c.StepText || box.Text != c.StepText || box.Lane != c.Lane {
				t.Fatalf("workflow=%+v diagram=%+v", step, box)
			}
			if len(d.Asks) != len(fx.Snapshot.NeedsYou) {
				t.Fatal("NEEDS YOU lost the card")
			}
		})
	}
}

// Execute an external close against a warmed cache, then compose the board.
func TestBoardClosedGateScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/closed-gate.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Beads                       []beads.Bead
		Close                       string
		JournalBefore, JournalAfter string
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	journal := filepath.Join(dir, ".beads", "dolt", "example", ".dolt", "noms", "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv")
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(journal, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(sc.JournalBefore)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{}
	for _, b := range sc.Beads {
		mem.Seed(b)
	}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: beads.NewCached(mem)}}
	first := src.Snapshot(ctx)
	if first.Degraded != nil || len(first.Workstreams) != 1 || len(first.NoPitch) != 1 {
		t.Fatalf("open fixture: %+v", first)
	}
	diagram := func(s livedata.Snapshot) string {
		t.Helper()
		if len(s.Workstreams) != 1 {
			t.Fatalf("missing workstream: %+v", s)
		}
		return strings.Join(livedata.DiagramLines(livedata.Board(s, 100).Workstreams[0].Diagram), "\n")
	}
	if !strings.Contains(diagram(first), sc.Close+"#wait") {
		t.Fatal("missing initial synthetic wait")
	}
	if err := mem.Close(ctx, dir, sc.Close); err != nil {
		t.Fatal(err)
	}
	stale := src.Snapshot(ctx)
	if len(stale.NoPitch) != 1 || !strings.Contains(diagram(stale), sc.Close+"#wait") {
		t.Fatal("fixture did not reproduce cached open gate after external close")
	}
	write(sc.JournalAfter)
	next := src.Snapshot(ctx)
	if next.Degraded != nil || len(next.NoPitch) != 0 || len(next.NeedsYou) != 0 {
		t.Fatalf("journal failed to invalidate closed asks: %+v", next)
	}
	d := livedata.Board(next, 100)
	row := d.Workstreams[0]
	steps := row.Diagram.Steps
	if row.Kind != "waiting" || strings.Contains(row.Text, "pitch") || len(d.Asks) != 0 ||
		len(steps) != 2 || steps[0].ID != sc.Close || steps[0].Kind != "done" ||
		steps[1].Kind != "waiting" || !strings.Contains(steps[1].Who, "CI on main") ||
		strings.Contains(diagram(next), sc.Close+"#wait") {
		t.Fatalf("closed board: %+v diagram: %+v", row, steps)
	}
}
