package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
)

// worktreeSweepEvery is how often finished Operators' worktrees are pruned.
const worktreeSweepEvery = 10 * time.Minute

// worktreeSweeper removes the worktrees of finished Operators whose bead is
// closed and whose work is all on main or kept by a refs/archive/ ref. A run's worktree is frozen when it
// ends (settle.freeze) so a respawn can resume it; a closed bead gets no
// respawn. In an invented batch of 40 completed tasks, retaining every tree
// would keep 40 unnecessary checkouts; only safely landed work may be pruned.
type worktreeSweeper struct {
	runs interface {
		ListFinishedRuns(ctx context.Context) ([]store.AgentRun, error)
		ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
	}
	projects interface {
		GetProjectByID(ctx context.Context, id int64) (*store.Project, error)
	}
	// beads is read with one List (status all) per project and sweep; give it
	// the cached/export reader, never Show per run.
	beads beads.Client
	// prune removes one worktree when safe; nil uses worktreelock.Prune.
	prune func(projectDir, dir string) (bool, error)
}

func (w worktreeSweeper) Run(ctx context.Context) {
	t := time.NewTicker(worktreeSweepEvery)
	defer t.Stop()
	for {
		if n := w.sweep(ctx); n > 0 {
			log.Printf("daemon: pruned %d finished operator worktree(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w worktreeSweeper) sweep(ctx context.Context) int {
	prune := w.prune
	if prune == nil {
		prune = worktreelock.Prune
	}
	finished, err := w.runs.ListFinishedRuns(ctx)
	if err != nil {
		return 0
	}
	running, err := w.runs.ListRunningRuns(ctx)
	if err != nil {
		return 0 // cannot tell what is live: touch nothing
	}
	live := map[string]bool{}
	for _, r := range running {
		live[r.AgentName] = true
		live[r.TaskBeadID] = true
	}
	inUse, ok := w.inUse(ctx, running)
	if !ok {
		return 0
	}
	seen := map[string]bool{}
	statuses := map[string]map[string]string{} // project dir -> bead id -> status; nil = unreadable
	statusOf := func(dir, id string) string {
		m, ok := statuses[dir]
		if !ok {
			list, err := w.beads.List(ctx, dir, beads.ListOptions{Status: "all"})
			if err == nil {
				m = make(map[string]string, len(list))
				for _, b := range list {
					m[b.ID] = b.Status
				}
			} else {
				log.Printf("daemon: worktree sweep: read beads of %s: %v (nothing pruned there this pass)", dir, err)
			}
			statuses[dir] = m
		}
		return m[id]
	}
	pruned := 0
	for _, r := range finished {
		if seen[r.AgentName] || live[r.AgentName] || live[r.TaskBeadID] {
			continue
		}
		seen[r.AgentName] = true
		p, err := w.projects.GetProjectByID(ctx, r.ProjectID)
		if err != nil || p == nil {
			continue
		}
		dir := spawn.RunWorktree(p.Path, r)
		if !worktreelock.IsProjectWorktree(p.Path, dir) {
			continue // never a checkout outside the Operators' folders
		}
		if inUse[resolvedDir(dir)] {
			continue // a running Operator works there
		}
		if _, err := os.Stat(dir); err != nil {
			continue // already gone: runs outlive their worktrees, so ask br only when it matters
		}
		if statusOf(p.Path, r.TaskBeadID) != "closed" {
			continue
		}
		ok, err := w.pruneClaimed(ctx, prune, p.Path, dir)
		if err != nil {
			log.Printf("daemon: prune worktree %s: %v", dir, err)
			continue
		}
		if ok {
			pruned++
		}
	}
	return pruned
}

// inUse is the real path of every worktree a running run works in. A follow-up
// spawned with --branch takes over a finished run's worktree under a new name
// and task, so names are not enough (paths compared as FreezeWorktree does). A
// running run whose project cannot be read leaves us blind: ok false.
func (w worktreeSweeper) inUse(ctx context.Context, running []store.AgentRun) (map[string]bool, bool) {
	in := map[string]bool{}
	for _, r := range running {
		p, err := w.projects.GetProjectByID(ctx, r.ProjectID)
		if err != nil || p == nil {
			return nil, false
		}
		in[resolvedDir(spawn.RunWorktree(p.Path, r))] = true
	}
	return in, true
}

// pruneClaimed removes dir under its worktree claim, after reading the running
// runs again. A --branch takeover holds that claim from before it unlocks the
// worktree and starts its pane until its agent_runs row names the worktree, so
// the pass's first read of running runs can miss it: under the claim either the
// spawn has not begun (and waits for us) or its row is there. A claim held
// elsewhere or a read that fails removes nothing.
func (w worktreeSweeper) pruneClaimed(ctx context.Context, prune func(string, string) (bool, error), projectDir, dir string) (bool, error) {
	release, held, err := spawn.TryClaimWorktree(dir)
	if err != nil {
		log.Printf("daemon: worktree sweep: claim %s: %v (not pruned)", dir, err)
		return false, nil
	}
	if !held {
		return false, nil // a spawn is taking it over, or a freeze runs
	}
	defer release()
	running, err := w.runs.ListRunningRuns(ctx)
	if err != nil {
		return false, nil
	}
	inUse, ok := w.inUse(ctx, running)
	if !ok || inUse[resolvedDir(dir)] {
		return false, nil
	}
	return prune(projectDir, dir)
}

// resolvedDir resolves symlinks; a path that cannot be resolved stays as is.
func resolvedDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
