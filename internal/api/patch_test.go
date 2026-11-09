package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// boardOf is the pre-write board of the close-then-board world: w.1 working on CI, w.2 waiting on it.
func boardOf() livedata.Snapshot {
	s := livedata.Snapshot{
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []livedata.SnapWorkstream{{BeadID: "w", Project: "demo", Title: "Area", Status: "open", ManagerRole: "manager-w", Steps: []livedata.SnapStep{
			{BeadID: "w.1", Title: "Build", Status: "in_progress", State: beads.StateInProgress, Waiting: "CI #7"},
			{BeadID: "w.2", Title: "Ship", Status: "open", State: beads.StateNotStarted, BlockedBy: []string{"w.1"}},
		}}},
	}
	d := livedata.Board(s, 1000)
	s.Display = &d
	return s
}

type closeThenBoard struct {
	Beads []struct {
		ID, Title, Status, Parent string
		Labels                    []string
		BlockedBy                 []string `json:"blocked_by"`
	}
	Write      [][]string
	IntentBead string            `json:"intent_bead"`
	KeptLoads  int               `json:"kept_loads"`
	Want       map[string]string `json:"want"`
}

func readCloseThenBoard(t *testing.T) closeThenBoard {
	t.Helper()
	raw, err := os.ReadFile("../../tests/evals/scenarios/close-then-board.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc closeThenBoard
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	return sc
}

// patchWorld is a door over the scenario's beads whose loads keep the pre-write rows (a read that
// overlapped writes) for the first kept loads, then read fresh.
func patchWorld(t *testing.T, sc closeThenBoard) (*Server, string, *atomic.Int32) {
	t.Helper()
	t.Setenv("COMMANDER_HOME", t.TempDir())
	m := &beads.MemClient{EdgeParentsOnly: true}
	for _, b := range sc.Beads {
		m.Seed(beads.Bead{ID: b.ID, Title: b.Title, Status: b.Status, Labels: b.Labels})
		if b.Parent != "" {
			m.Edges = append(m.Edges, beads.Dep{IssueID: b.ID, DependsOnID: b.Parent, Type: "parent-child"})
		}
		for _, bl := range b.BlockedBy {
			m.Edges = append(m.Edges, beads.Dep{IssueID: b.ID, DependsOnID: bl, Type: "blocks"})
		}
	}
	var loads atomic.Int32
	var mu sync.Mutex
	written := false
	s := New(Options{Token: testToken, Beads: m, Write: func(context.Context, string, []string) ([]byte, []byte, error) {
		mu.Lock()
		written = true
		mu.Unlock()
		return []byte("ok"), nil, nil
	}, Source: func(context.Context) livedata.Snapshot {
		mu.Lock()
		defer mu.Unlock()
		snap := boardOf()
		if written && int(loads.Add(1)) <= sc.KeptLoads {
			snap.KeptLastGood = true // overlapped writes: the last rows, from before the write
			return snap
		}
		if written {
			snap.Workstreams[0].Steps[0].Status, snap.Workstreams[0].Steps[0].State = "closed", beads.StateDone
			snap.Workstreams[0].Steps[1].Waiting = "CI #9"
			d := livedata.Board(snap, 1000)
			snap.Display = &d
		}
		return snap
	}})
	s.Refresh(context.Background())
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts.URL, &loads
}

func served(t *testing.T, url string) livedata.Snapshot {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, url+"/api/snapshot", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s livedata.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func stepOf(s livedata.Snapshot, id string) livedata.SnapStep {
	for _, st := range s.Workstreams[0].Steps {
		if st.BeadID == id {
			return st
		}
	}
	return livedata.SnapStep{}
}

// noUnheldFrame fails when a served board shows the written state wrong: the closed step open, or
// the step released by it open with no holder.
func noUnheldFrame(t *testing.T, s livedata.Snapshot, want map[string]string, when string) {
	t.Helper()
	for id, status := range want {
		st := stepOf(s, id)
		if st.Status != status || st.Broken {
			t.Fatalf("%s: %s reads %s (broken %v), want %s held; pending %v", when, id, st.Status, st.Broken, status, s.Pending)
		}
	}
	if s.Display != nil && strings.Contains(s.Display.Workstreams[0].Text, "State missing") {
		t.Fatalf("%s: row reads %q", when, s.Display.Workstreams[0].Text)
	}
}

// TestBoardReflectsAnsweredWrite: the board served right after the door's answer shows the write,
// though the settle's load kept the pre-write rows.
func TestBoardReflectsAnsweredWrite(t *testing.T) {
	sc := readCloseThenBoard(t)
	_, url, _ := patchWorld(t, sc)
	_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: sc.Write, Env: []string{agent}, Intent: &beads.DoorIntent{Bead: sc.IntentBead}})
	if out.Exit != 0 {
		t.Fatal(out.Error)
	}
	s := served(t, url)
	noUnheldFrame(t, s, sc.Want, "after the answer")
	if !slices.Contains(s.Pending, "w.1") {
		t.Fatalf("not marked pending: %v", s.Pending)
	}
}

// TestKeptLastGoodKeepsPendingPatch: loads that keep the last rows keep the patch; the first clean
// load after the write replaces it with what it read and clears pending.
func TestKeptLastGoodKeepsPendingPatch(t *testing.T) {
	sc := readCloseThenBoard(t)
	srv, url, loads := patchWorld(t, sc)
	if _, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: sc.Write, Env: []string{agent}, Intent: &beads.DoorIntent{Bead: sc.IntentBead}}); out.Exit != 0 {
		t.Fatal(out.Error)
	}
	for int(loads.Load()) < sc.KeptLoads {
		srv.Refresh(context.Background())
		noUnheldFrame(t, served(t, url), sc.Want, "kept load")
	}
	srv.Refresh(context.Background()) // clean
	s := served(t, url)
	noUnheldFrame(t, s, sc.Want, "clean load")
	if len(s.Pending) != 0 {
		t.Fatalf("a clean load after the write leaves pending %v", s.Pending)
	}
}

// TestCloseThenBoardScenario runs scenarios/close-then-board.json: every board cmdrd publishes
// from the answer on (each WebSocket frame is one) reads the write, never the step open and unheld.
func TestCloseThenBoardScenario(t *testing.T) {
	sc := readCloseThenBoard(t)
	srv, url, _ := patchWorld(t, sc)
	if _, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: sc.Write, Env: []string{agent}, Intent: &beads.DoorIntent{Bead: sc.IntentBead}}); out.Exit != 0 {
		t.Fatal(out.Error)
	}
	seen := map[uint64]bool{}
	for i := 0; i < sc.KeptLoads+2; i++ {
		s := served(t, url)
		seen[s.Version] = true
		noUnheldFrame(t, s, sc.Want, "frame")
		srv.Refresh(context.Background())
	}
	if len(seen) < 2 {
		t.Fatalf("expected the patched and the read-back boards, saw versions %v", seen)
	}
}

// m7: a patch kept by loads that keep the last rows ends at pendingPatchTTL; the board then reads
// as the loads show it, so a later write to the bead is never hidden behind an old patch.
func TestPendingPatchExpires(t *testing.T) {
	sc := readCloseThenBoard(t)
	srv, url, _ := patchWorld(t, sc)
	if _, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: projectDir(t), Ops: sc.Write, Env: []string{agent}, Intent: &beads.DoorIntent{Bead: sc.IntentBead}}); out.Exit != 0 {
		t.Fatal(out.Error)
	}
	srv.mu.Lock()
	if len(srv.pending) == 0 {
		srv.mu.Unlock()
		t.Fatal("no patch pending after the write")
	}
	for id, p := range srv.pending {
		p.at = time.Now().Add(-pendingPatchTTL - time.Second)
		srv.pending[id] = p
	}
	srv.mu.Unlock()
	srv.Refresh(context.Background()) // a kept load (the scenario's first loads keep the last rows)
	srv.mu.Lock()
	n := len(srv.pending)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("an expired patch outlived a kept load: %d pending", n)
	}
}
