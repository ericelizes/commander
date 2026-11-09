package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type fakeRuns struct{ finished, running []store.AgentRun }

func (f fakeRuns) ListFinishedRuns(context.Context) ([]store.AgentRun, error) { return f.finished, nil }
func (f fakeRuns) ListRunningRuns(context.Context) ([]store.AgentRun, error)  { return f.running, nil }

type projectAt struct{ path string }

func (o projectAt) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return &store.Project{ID: 1, Path: o.path}, nil
}

func TestSweepPrunesOnlyClosedBeadsWithNoLiveOperator(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"op-done", "op-open", "op-again"} {
		os.MkdirAll(filepath.Join(dir, ".worktrees", n), 0o755)
	}
	mem := &beads.MemClient{}
	ctx := context.Background()
	done, _ := mem.Create(ctx, dir, "done", beads.CreateOptions{})
	open, _ := mem.Create(ctx, dir, "open", beads.CreateOptions{})
	again, _ := mem.Create(ctx, dir, "again", beads.CreateOptions{})
	mem.Close(ctx, dir, done)
	mem.Close(ctx, dir, again)
	var pruned []string
	w := worktreeSweeper{
		runs: fakeRuns{
			finished: []store.AgentRun{
				{ProjectID: 1, AgentName: "operator-op-done", TaskBeadID: done},
				{ProjectID: 1, AgentName: "operator-op-open", TaskBeadID: open},   // bead still open: a respawn may want it
				{ProjectID: 1, AgentName: "operator-op-again", TaskBeadID: again}, // closed, but a new run is live on it
				{ProjectID: 1, AgentName: "operator-op-gone", TaskBeadID: done},   // worktree already removed
			},
			running: []store.AgentRun{{ProjectID: 1, AgentName: "operator-op-new", TaskBeadID: again, Status: "running"}},
		},
		projects: projectAt{dir},
		beads:    mem,
		prune: func(_, wt string) (bool, error) {
			pruned = append(pruned, filepath.Base(wt))
			return true, nil
		},
	}
	if n := w.sweep(ctx); n != 1 || len(pruned) != 1 || pruned[0] != "op-done" {
		t.Fatalf("sweep pruned %d: %v; want only op-done", n, pruned)
	}
}

// countingBeads counts sweep calls. An invented batch of 200 worktrees should
// need one project List, rather than 200 separate Show calls.
type countingBeads struct {
	*beads.MemClient
	shows int
	lists map[string]int
}

func (c *countingBeads) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	c.shows++
	return c.MemClient.Show(ctx, dir, id)
}

func (c *countingBeads) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	c.lists[dir]++
	return c.MemClient.List(ctx, dir, opts)
}

type projectsByID map[int64]string

func (p projectsByID) GetProjectByID(_ context.Context, id int64) (*store.Project, error) {
	return &store.Project{ID: id, Path: p[id]}, nil
}

func TestSweepReadsStatusesWithOneListPerProject(t *testing.T) {
	ctx := context.Background()
	a, b := t.TempDir(), t.TempDir()
	mem := &beads.MemClient{}
	var finished []store.AgentRun
	want := map[string]bool{}
	for pi, dir := range []string{a, b} {
		for i, closed := range []bool{true, false, true, false, true} {
			id, _ := mem.Create(ctx, dir, "t", beads.CreateOptions{})
			if closed {
				mem.Close(ctx, dir, id)
			}
			name := "op-" + string(rune('a'+pi)) + string(rune('0'+i))
			os.MkdirAll(filepath.Join(dir, ".worktrees", name), 0o755)
			finished = append(finished, store.AgentRun{ProjectID: int64(pi + 1), AgentName: "operator-" + name, TaskBeadID: id})
			if closed {
				want[name] = true
			}
		}
	}
	cb := &countingBeads{MemClient: mem, lists: map[string]int{}}
	got := map[string]bool{}
	w := worktreeSweeper{
		runs:     fakeRuns{finished: finished},
		projects: projectsByID{1: a, 2: b},
		beads:    cb,
		prune: func(_, wt string) (bool, error) {
			got[filepath.Base(wt)] = true
			return true, nil
		},
	}
	if n := w.sweep(ctx); n != len(want) {
		t.Fatalf("pruned %d (%v), want %v", n, got, want)
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("closed %s not pruned: %v", n, got)
		}
	}
	if cb.shows != 0 {
		t.Fatalf("sweep made %d Show calls, want 0", cb.shows)
	}
	if len(cb.lists) != 2 || cb.lists[a] != 1 || cb.lists[b] != 1 {
		t.Fatalf("want one List per project, got %v", cb.lists)
	}
}

type failingList struct {
	*countingBeads
	err error
}

func (f *failingList) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	f.lists[dir]++
	return nil, f.err
}

// A failed List prunes nothing and is not repeated for every run of the project
// (one per finished run while bd fails would be the storm again).
func TestSweepOnAFailedListPrunesNothingAndListsOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mem := &beads.MemClient{}
	var finished []store.AgentRun
	for i := 0; i < 3; i++ {
		id, _ := mem.Create(ctx, dir, "t", beads.CreateOptions{})
		mem.Close(ctx, dir, id)
		name := "op-" + string(rune('a'+i))
		os.MkdirAll(filepath.Join(dir, ".worktrees", name), 0o755)
		finished = append(finished, store.AgentRun{ProjectID: 1, AgentName: "operator-" + name, TaskBeadID: id})
	}
	fl := &failingList{countingBeads: &countingBeads{MemClient: mem, lists: map[string]int{}}, err: errors.New("bd: database is locked")}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w := worktreeSweeper{
		runs:     fakeRuns{finished: finished},
		projects: projectAt{dir},
		beads:    fl,
		prune: func(_, wt string) (bool, error) {
			t.Errorf("pruned %s on a failed read", wt)
			return true, nil
		},
	}
	if n := w.sweep(ctx); n != 0 || fl.lists[dir] != 1 || fl.shows != 0 {
		t.Fatalf("pruned %d, lists %d, shows %d; want 0, 1, 0", n, fl.lists[dir], fl.shows)
	}
	if !strings.Contains(buf.String(), "database is locked") {
		t.Fatalf("the failed read is not logged: %q", buf.String())
	}
}
