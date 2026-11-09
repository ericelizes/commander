package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
)

// worktreeSweepEvery is how often finished Operators' worktrees are pruned.
const worktreeSweepEvery = 10 * time.Minute

// worktreeSweeper removes the worktrees of finished Operators whose bead is
// closed and whose work is all on main. A run's worktree is frozen when it
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
		dir := filepath.Join(p.Path, ".worktrees", strings.TrimPrefix(r.AgentName, "operator-"))
		if _, err := os.Stat(dir); err != nil {
			continue // already gone: runs outlive their worktrees, so ask br only when it matters
		}
		if statusOf(p.Path, r.TaskBeadID) != "closed" {
			continue
		}
		ok, err := prune(p.Path, dir)
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
