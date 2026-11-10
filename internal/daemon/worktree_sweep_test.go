package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
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

// A follow-up spawned with --branch works in a finished run's worktree under a
// new name and task. The finished run's closed bead must not take that live
// tree, whichever spelling of the path either row holds.
func TestSweepNeverTakesAWorktreeARunningRunUses(t *testing.T) {
	dir := t.TempDir()
	noindex := filepath.Join(dir, ".worktrees.noindex")
	for _, n := range []string{"op-a", "op-c", "op-custom"} {
		os.MkdirAll(filepath.Join(noindex, n), 0o755)
	}
	os.Symlink(noindex, filepath.Join(dir, ".worktrees")) // .worktrees links to .worktrees.noindex
	outside := filepath.Join(dir, "elsewhere", "op-out")
	os.MkdirAll(outside, 0o755)
	mem := &beads.MemClient{}
	ctx := context.Background()
	a, _ := mem.Create(ctx, dir, "a", beads.CreateOptions{})
	b, _ := mem.Create(ctx, dir, "b", beads.CreateOptions{})
	c, _ := mem.Create(ctx, dir, "c", beads.CreateOptions{})
	cu, _ := mem.Create(ctx, dir, "custom", beads.CreateOptions{})
	out, _ := mem.Create(ctx, dir, "out", beads.CreateOptions{})
	for _, id := range []string{a, c, cu, out} {
		mem.Close(ctx, dir, id)
	}
	path := func(p string) sql.NullString { return sql.NullString{String: p, Valid: true} }
	var pruned []string
	w := worktreeSweeper{
		runs: fakeRuns{
			finished: []store.AgentRun{
				// NULL path = .worktrees/op-a, which the follow-up names by its real folder.
				{ID: 1, ProjectID: 1, AgentName: "operator-op-a", TaskBeadID: a},
				{ID: 2, ProjectID: 1, AgentName: "operator-op-c", TaskBeadID: c},
				{ID: 3, ProjectID: 1, AgentName: "operator-op-x", TaskBeadID: cu, WorktreePath: path(filepath.Join(noindex, "op-custom"))},
				{ID: 4, ProjectID: 1, AgentName: "operator-op-y", TaskBeadID: out, WorktreePath: path(outside)},
			},
			running: []store.AgentRun{{ID: 5, ProjectID: 1, AgentName: "operator-op-follow", TaskBeadID: b, Status: "running",
				WorktreePath: path(filepath.Join(noindex, "op-a"))}},
		},
		projects: projectAt{dir},
		beads:    mem,
		prune: func(_, wt string) (bool, error) {
			pruned = append(pruned, filepath.Base(wt))
			return true, nil
		},
	}
	w.sweep(ctx)
	got := strings.Join(pruned, ",")
	if got != "op-c,op-custom" {
		t.Fatalf("pruned %q; want op-c,op-custom (op-a is the follow-up's, op-out is outside the worktree folders)", got)
	}
}

type failingProjects struct{}

func (failingProjects) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return nil, errors.New("db busy")
}

func TestSweepWithAnUnreadableRunningProjectPrunesNothing(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".worktrees", "op-done"), 0o755)
	mem := &beads.MemClient{}
	ctx := context.Background()
	done, _ := mem.Create(ctx, dir, "done", beads.CreateOptions{})
	mem.Close(ctx, dir, done)
	w := worktreeSweeper{
		runs: fakeRuns{
			finished: []store.AgentRun{{ProjectID: 1, AgentName: "operator-op-done", TaskBeadID: done}},
			running:  []store.AgentRun{{ProjectID: 2, AgentName: "operator-op-live", Status: "running"}},
		},
		projects: failingProjects{},
		beads:    mem,
		prune: func(string, string) (bool, error) {
			t.Fatal("pruned with a running run's project unread")
			return false, nil
		},
	}
	if n := w.sweep(ctx); n != 0 {
		t.Fatalf("pruned %d", n)
	}
}

// takeoverRuns answers the pass's first ListRunningRuns with nothing and every
// later read with the follow-up whose row landed meanwhile.
type takeoverRuns struct {
	finished []store.AgentRun
	follow   store.AgentRun
	reads    *int
}

func (f takeoverRuns) ListFinishedRuns(context.Context) ([]store.AgentRun, error) {
	return f.finished, nil
}
func (f takeoverRuns) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	*f.reads++
	if *f.reads == 1 {
		return nil, nil
	}
	return []store.AgentRun{f.follow}, nil
}

func sweepRaceFixture(t *testing.T) (dir, wt, done string, mem *beads.MemClient) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	dir = t.TempDir()
	wt = filepath.Join(dir, ".worktrees", "op-done")
	os.MkdirAll(wt, 0o755)
	mem = &beads.MemClient{}
	ctx := context.Background()
	done, _ = mem.Create(ctx, dir, "done", beads.CreateOptions{})
	mem.Close(ctx, dir, done)
	return dir, wt, done, mem
}

func TestSweepSkipsAWorktreeASpawnHoldsTheClaimOf(t *testing.T) {
	dir, wt, done, mem := sweepRaceFixture(t)
	// A --branch takeover holds the claim from before it unlocks the worktree
	// until its row is written: the pass's running runs do not show it yet.
	release, held, err := spawn.TryClaimWorktree(wt)
	if err != nil || !held {
		t.Fatalf("claim: held=%v err=%v", held, err)
	}
	w := worktreeSweeper{
		runs:     fakeRuns{finished: []store.AgentRun{{ID: 1, ProjectID: 1, AgentName: "operator-op-done", TaskBeadID: done}}},
		projects: projectAt{dir},
		beads:    mem,
		prune: func(string, string) (bool, error) {
			t.Fatal("pruned a worktree a spawn holds the claim of")
			return false, nil
		},
	}
	if n := w.sweep(context.Background()); n != 0 {
		t.Fatalf("pruned %d", n)
	}
	release()
	// Once the claim is free (and no run works there) the next pass prunes it.
	var pruned int
	w.prune = func(string, string) (bool, error) { pruned++; return true, nil }
	if n := w.sweep(context.Background()); n != 1 || pruned != 1 {
		t.Fatalf("after release: pruned %d (%d calls), want 1", n, pruned)
	}
}

func TestSweepRereadsRunningRunsUnderTheClaim(t *testing.T) {
	dir, wt, done, mem := sweepRaceFixture(t)
	reads := 0
	w := worktreeSweeper{
		runs: takeoverRuns{
			finished: []store.AgentRun{{ID: 1, ProjectID: 1, AgentName: "operator-op-done", TaskBeadID: done}},
			follow: store.AgentRun{ID: 2, ProjectID: 1, AgentName: "operator-op-follow", Status: "running",
				WorktreePath: sql.NullString{String: wt, Valid: true}},
			reads: &reads,
		},
		projects: projectAt{dir},
		beads:    mem,
		prune: func(string, string) (bool, error) {
			t.Fatal("pruned a worktree whose takeover's row landed during the pass")
			return false, nil
		},
	}
	if n := w.sweep(context.Background()); n != 0 {
		t.Fatalf("pruned %d", n)
	}
	if reads < 2 {
		t.Fatalf("running runs read %d times; want a re-read under the claim", reads)
	}
	// The claim is released after the pass: a spawn can take it.
	release, held, err := spawn.TryClaimWorktree(wt)
	if err != nil || !held {
		t.Fatalf("claim left held after the sweep: held=%v err=%v", held, err)
	}
	release()
}
