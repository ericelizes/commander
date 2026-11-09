package livedata

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestOverlayChats(t *testing.T) {
	since := map[string]SnapAgent{}
	s := Snapshot{Agents: map[string]SnapAgent{"commander": {State: "idle", Since: 10}, "manager-w": {State: "working", Since: 20}}}
	chats := map[string]ChatState{"commander": {State: "working", Detail: "Bash"}, "manager-w": {State: "idle"}}
	overlayChats(&s, chats, since, 100)
	if a := s.Agents["commander"]; a.State != "working" || a.Detail != "Bash" || a.Since != 10 {
		t.Fatalf("commander %+v", a)
	}
	if a := s.Agents["manager-w"]; a.State != "idle" || a.Since != 100 {
		t.Fatalf("manager %+v", a)
	}
	// The next snapshot still shows the turn ending at 100, not now.
	s = Snapshot{Agents: map[string]SnapAgent{"manager-w": {State: "working", Since: 20}}}
	overlayChats(&s, map[string]ChatState{"manager-w": {State: "idle"}}, since, 160)
	if a := s.Agents["manager-w"]; a.Since != 100 {
		t.Fatalf("since moved: %+v", a)
	}
	// Once the store agrees, the chat changes nothing.
	s = Snapshot{Agents: map[string]SnapAgent{"manager-w": {State: "idle", Since: 150}}}
	overlayChats(&s, map[string]ChatState{"manager-w": {State: "idle"}}, since, 200)
	if a := s.Agents["manager-w"]; a.Since != 150 || len(since) != 0 {
		t.Fatalf("agreed %+v %v", a, since)
	}
}

func TestSinceTrackerPersists(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	d := func() Display {
		return Display{Workstreams: []BoardRow{{Key: "p:w", Kind: "stalled", Text: "Stalled", Tracks: []BoardTrack{{Key: "p:w.1", Kind: "waiting", Text: "Waiting for CI", Since: 42}}}}}
	}
	var tr sinceTracker
	got := d()
	if err := tr.fill(ctx, st, &got, 100, false); err != nil || got.Workstreams[0].Since != 100 || got.Workstreams[0].Tracks[0].Since != 42 {
		t.Fatalf("first %+v %v", got.Workstreams[0], err)
	}
	// A restarted cmdrd keeps the clock.
	var again sinceTracker
	got = d()
	_ = again.fill(ctx, st, &got, 500, false)
	if got.Workstreams[0].Since != 100 {
		t.Fatalf("restart reset the clock: %d", got.Workstreams[0].Since)
	}
	// A new state starts a new clock.
	got = d()
	got.Workstreams[0].Text = "State missing"
	_ = again.fill(ctx, st, &got, 600, false)
	if got.Workstreams[0].Since != 600 {
		t.Fatalf("new state since %d", got.Workstreams[0].Since)
	}
	// A row that left the board is forgotten.
	got = Display{}
	_ = again.fill(ctx, st, &got, 700, false)
	if rows, _ := st.BoardSince(ctx); len(rows) != 0 {
		t.Fatalf("kept %v", rows)
	}
}

func TestBoardLinesByRole(t *testing.T) {
	d := Display{
		Commander: BoardCommander{Kind: "idle", Text: "monitoring", Since: 40},
		Workstreams: []BoardRow{
			{Key: "p:a", N: 1, Title: "Alpha", ManagerRole: "manager-a", Kind: "needs", Text: "Needs you: pick", Progress: Progress{Done: 1, Total: 3}, Flag: 1, Working: 2, Since: 70,
				Tracks: []BoardTrack{{Letter: "A", Title: "One", Text: "Stalled", Progress: Progress{Total: 1}}, {Letter: "B", Title: "Two", Text: "Waiting for CI", Progress: Progress{Done: 1, Total: 1}}}},
			{Key: "p:b", N: 2, Title: "Beta", ManagerRole: "manager-b", Kind: "quiet", Text: "Nothing to do", Gate: &BoardGate{Title: "Merge", Who: "you"}},
		},
		Asks: []BoardAsk{{Rank: 1, Text: "pick", Workstream: "Alpha", Track: "Two", TrackLetter: "B", WsKey: "p:a"}},
	}
	all := strings.Join(BoardLines(d, 100, "commander"), "\n")
	want := `0 Commander: monitoring (for 1m)
1 Alpha: Needs you: pick (33% done, no gates, ⚑ 1, 2 working, for 30s)
  1A One: Stalled (0% done)
  1B Two: Waiting for CI (100% done)
2 Beta: Nothing to do (100% done, next gate: Merge, approved by you)
Needs you (1):
  #1B pick (Alpha › Two)`
	if all != want {
		t.Fatalf("got\n%s", all)
	}
	mine := BoardLines(d, 100, "manager-b")
	if len(mine) != 1 || !strings.HasPrefix(mine[0], "2 Beta:") {
		t.Fatalf("manager view %q", mine)
	}
}

// A failed load showing no rows (the store read expired) keeps their ages; a
// clean load that no longer shows a row forgets it.
func TestSinceKeepsRowAgesThroughAFailedLoad(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	row := func() Display {
		return Display{Workstreams: []BoardRow{{Key: "p:w", Kind: "stalled", Text: "Stalled"}}}
	}
	var tr sinceTracker
	got := row()
	if err := tr.fill(ctx, st, &got, 100, false); err != nil {
		t.Fatal(err)
	}
	empty := Display{}
	if err := tr.fill(ctx, st, &empty, 200, true); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.BoardSince(ctx); len(rows) != 1 {
		t.Fatalf("the failed load deleted stored rows: %v", rows)
	}
	got = row()
	if err := tr.fill(ctx, st, &got, 300, false); err != nil || got.Workstreams[0].Since != 100 {
		t.Fatalf("since = %d, %v; the failed load reset the age", got.Workstreams[0].Since, err)
	}
	empty = Display{}
	if err := tr.fill(ctx, st, &empty, 400, false); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.BoardSince(ctx); len(rows) != 0 {
		t.Fatalf("a clean load kept a row that left the board: %v", rows)
	}
}

// Through Source: a load on an expired context reads no projects and shows no
// rows; the tracker keeps them, so the next clean load keeps their ages.
func TestSnapshotWithAFailedLoadKeepsBoardSince(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{ListResult: []beads.Bead{{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}}}}
	src := &Source{Deps: Deps{Store: st, Beads: bc}}
	src.Snapshot(ctx)
	before := maps.Clone(src.since.rows)
	if len(before) == 0 {
		t.Fatal("bad fixture: no row is timed")
	}
	if _, err := st.DB().Exec(`ALTER TABLE agent_runs RENAME TO agent_runs_gone`); err != nil {
		t.Fatal(err)
	}
	if snap := src.Snapshot(ctx); snap.Degraded == nil {
		t.Fatalf("bad fixture: the load did not fail: %+v", snap.Display)
	}
	if !maps.Equal(src.since.rows, before) {
		t.Fatalf("the failed load changed the rows: %v, want %v", src.since.rows, before)
	}
}
