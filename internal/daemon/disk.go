package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/example-user/commander/internal/diskguard"
	"github.com/example-user/commander/internal/store"
)

// diskEvery is how often cmdrd reads how full its disks are.
const diskEvery = time.Minute

// tmpSweepEvery and tmpLeftoverAge bound the Operators' scratch root: an
// entry no running run owns and untouched this long is a leftover.
const (
	tmpSweepEvery  = 10 * time.Minute
	tmpLeftoverAge = time.Hour
)

// diskWatcher keeps the disks off the floor. Every minute it reads the disks
// cmdr writes to (the commander home, the system temp dir, every project and
// repo) and puts what it sees on the health board: `disk:<mount>` at
// diskguard.WarnPct (Commander mailed once), `disk-full:<mount>` at
// diskguard.RefusePct, when spawns refuse too (spawn.CheckDisk). Both clear
// when the disk drops. Every 10 minutes it sweeps the Operators' TMPDIR root.
type diskWatcher struct {
	home     string
	health   *healthBoard
	projects interface {
		ListProjects(ctx context.Context) ([]store.Project, error)
	}
	runs interface {
		ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
	}
	now func() time.Time
	// shown are the keys this watcher has raised, so a disk that is no longer
	// read (a project removed) is cleared too.
	shown map[string]bool
}

func (w *diskWatcher) Run(ctx context.Context) {
	disk := time.NewTicker(diskEvery)
	defer disk.Stop()
	sweep := time.NewTicker(tmpSweepEvery)
	defer sweep.Stop()
	w.check(ctx)
	w.sweepTmp(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-disk.C:
			w.check(ctx)
		case <-sweep.C:
			w.sweepTmp(ctx)
		}
	}
}

func (w *diskWatcher) paths(ctx context.Context) []string {
	paths := []string{w.home, diskguard.TmpRoot(w.home), os.TempDir()}
	if w.projects != nil {
		if ps, err := w.projects.ListProjects(ctx); err == nil {
			for _, p := range ps {
				paths = append(paths, p.Path, p.RepoPath)
			}
		}
	}
	return paths
}

// check reads the disks once and updates the board.
func (w *diskWatcher) check(ctx context.Context) {
	if w.shown == nil {
		w.shown = map[string]bool{}
	}
	warn, refuse := diskguard.WarnPct(), diskguard.RefusePct()
	now := map[string]bool{}
	for _, u := range diskguard.Disks(w.paths(ctx)...) {
		warnKey, fullKey := "disk:"+u.Mount, "disk-full:"+u.Mount
		switch {
		case u.UsedPct >= refuse:
			w.health.fail(fullKey, fmt.Sprintf("Disk full: %s; new Operators are refused until it is below %.0f%%. Free space now: cmdr worktrees gc --project <p>, remove old build caches.", u.Describe(), refuse))
			now[fullKey] = true
		case u.UsedPct >= warn:
			w.health.fail(warnKey, fmt.Sprintf("Disk filling up: %s (warning at %.0f%%, new Operators refused at %.0f%%). Free space before it fills: cmdr worktrees gc --project <p>, remove old build caches.", u.Describe(), warn, refuse))
			now[warnKey] = true
		}
	}
	for key := range w.shown {
		if !now[key] {
			w.health.ok(key)
			delete(w.shown, key)
		}
	}
	for key := range now {
		if !w.shown[key] {
			log.Printf("daemon: disk: %s", key)
		}
		w.shown[key] = true
	}
}

// sweepTmp removes leftovers under the scratch root: entries of no running run
// untouched for tmpLeftoverAge. A run's launcher removes its own TMPDIR as it
// ends; this catches runs ended from outside. Unreadable runs = touch nothing.
func (w *diskWatcher) sweepTmp(ctx context.Context) {
	if w.runs == nil {
		return
	}
	running, err := w.runs.ListRunningRuns(ctx)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, r := range running {
		if r.TmuxPaneID.Valid && r.TmuxPaneID.String != "" {
			keep[diskguard.RunTmpName(r.TmuxPaneID.String)] = true
		}
	}
	nowFn := w.now
	if nowFn == nil {
		nowFn = time.Now
	}
	removed, err := diskguard.SweepTmp(diskguard.TmpRoot(w.home), keep, tmpLeftoverAge, nowFn())
	if err != nil {
		log.Printf("daemon: operator scratch sweep: %v", err)
		return
	}
	if len(removed) > 0 {
		log.Printf("daemon: removed %d operator scratch dir(s) left under %s", len(removed), diskguard.TmpRoot(w.home))
	}
}
