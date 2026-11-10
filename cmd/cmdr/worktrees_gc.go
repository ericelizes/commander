package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/worktreelock"
)

// runWorktrees is `cmdr worktrees gc [--project p] [--apply] [--older-than 72h]
// [--archive] [--json]`: dry run by default; lists the finished Operator
// worktrees it would remove and why every other one is kept.
func runWorktrees(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 || args[0] != "gc" {
		emitErr(stderr, "usage: cmdr worktrees gc [--project p] [--apply] [--older-than 72h] [--archive] [--dangerous-no-keep-file] [--json]")
		return 1
	}
	fs := flag.NewFlagSet("worktrees gc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "only this project (default: all)")
	apply := fs.Bool("apply", false, "remove the worktrees (default: dry run)")
	olderThan := fs.Duration("older-than", 72*time.Hour, "a run this old counts as finished even with an open task")
	archive := fs.Bool("archive", false, "archive unique unshipped commits under refs/archive/op/ and remove (default: keep)")
	noKeepFile := fs.Bool("dangerous-no-keep-file", false, "allow --apply on a project of 100+ worktrees that has no .commander/worktree-keep")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if role := os.Getenv("COMMANDER_ROLE"); role != "" && !strings.HasPrefix(role, "manager-") && role != "commander" {
		emitErr(stderr, "worktrees gc is for Managers and the user; Operators must not run it")
		return 1
	}
	statePath, err := stateDBPath()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	st, err := store.Open(statePath)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open state db: %v", err))
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	projects, err := st.ListProjects(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	var reports []*projectGCReport
	matched := 0
	code := 0
	for _, p := range projects {
		if (*project != "" && p.Name != *project) || p.Type != "code" {
			continue
		}
		matched++
		rep, err := gcProject(ctx, st, p, *apply, *archive, *noKeepFile, *olderThan)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", p.Name, err)
			code = 1
			continue
		}
		reports = append(reports, rep)
	}
	if matched == 0 {
		emitErr(stderr, "no code project matches")
		return 1
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(reports)
		return code
	}
	for _, r := range reports {
		printGCReport(stdout, r)
	}
	return code
}

type projectGCReport struct {
	Project    string `json:"project"`
	ProjectDir string `json:"project_dir"`
	*worktreelock.GCReport
}

func gcProject(ctx context.Context, st *store.Store, p store.Project, apply, archive, noKeepFile bool, olderThan time.Duration) (*projectGCReport, error) {
	repo := p.RepoPath
	if repo == "" {
		repo = p.Path
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		return nil, fmt.Errorf("%s is not a git repo (set it with cmdr project set-repo)", repo)
	}
	runs, err := st.ListAgentRunsByProject(ctx, p.ID, "")
	if err != nil {
		return nil, err
	}
	// One list call per project, not one br call per worktree.
	bc := newBeadsClientFn()
	byID, err := gcBeadIndex(ctx, bc, p.Path)
	if err != nil {
		return nil, err
	}
	keep, err := worktreelock.LoadKeepList(p.Path)
	if err != nil {
		return nil, err
	}
	rep, err := worktreelock.GC(ctx, worktreelock.GCOptions{
		Repo: repo, Apply: apply, Archive: archive, OlderThan: olderThan,
		Keep:       keep,
		ProjectDir: p.Path, AllowNoKeepFile: noKeepFile,
		Lookup: gcLookup(ctx, bc, p.Path, runs, byID),
	})
	if err != nil {
		return nil, err
	}
	return &projectGCReport{Project: p.Name, GCReport: rep}, nil
}

// gcBeadIndex lists every bead once. A failed list would read every held
// worktree as free, so it is an error, never an empty index.
func gcBeadIndex(ctx context.Context, bc beads.Client, dir string) (map[string]beads.Bead, error) {
	list, err := bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("cannot read the beads (held worktrees would look free): %w", err)
	}
	byID := map[string]beads.Bead{}
	for _, b := range list {
		byID[b.ID] = b
	}
	return byID, nil
}

// gcLookup matches a worktree to its runs by task bead id (the dir or branch
// ends with "-<id>"; the longest id wins). A worktree with no run is not
// Matched and is kept. Held reads the real parent edges (beads.Lineage), and
// the last activity is the latest run start; GC adds when the run froze it.
// Runs are indexed by task and parents read once for every task (gcParents),
// so 2,000+ worktrees cost no read each.
func gcLookup(ctx context.Context, c beads.Client, dir string, runs []store.AgentRun, byID map[string]beads.Bead) func(dir, branch string) worktreelock.GCFacts {
	byTask := map[string][]store.AgentRun{}
	for _, r := range runs {
		if r.TaskBeadID != "" {
			byTask[r.TaskBeadID] = append(byTask[r.TaskBeadID], r)
		}
	}
	tasks := make([]string, 0, len(byTask))
	for id := range byTask {
		tasks = append(tasks, id)
	}
	sort.Strings(tasks)
	parents := gcParents(ctx, c, dir, byID, tasks)
	held := map[string]bool{}
	return func(wt, branch string) worktreelock.GCFacts {
		var f worktreelock.GCFacts
		task := longestTask(filepath.Base(wt), byTask)
		if t := longestTask(branch, byTask); len(t) > len(task) {
			task = t
		}
		if task == "" {
			return f
		}
		f.Matched, f.TaskID = true, task
		for _, r := range byTask[task] {
			if r.Status == "running" {
				f.Running = true
			}
			if t := time.Unix(r.SpawnTime, 0); t.After(f.LastActivity) {
				f.LastActivity = t
			}
		}
		f.TaskClosed = byID[task].Status == "closed"
		h, ok := held[task]
		if !ok {
			for _, id := range beads.Lineage(ctx, c, dir, parents, task) {
				for _, l := range byID[id].Labels {
					if l == beads.HoldLabel {
						h = true
					}
				}
			}
			held[task] = h
		}
		f.Held = h
		return f
	}
}

// longestTask is the longest task id name equals or ends with "-<id>".
func longestTask(name string, byTask map[string][]store.AgentRun) string {
	best := ""
	if _, ok := byTask[name]; ok {
		best = name
	}
	for i := 0; i < len(name); i++ {
		if name[i] != '-' {
			continue
		}
		if id := name[i+1:]; len(id) > len(best) {
			if _, ok := byTask[id]; ok {
				best = id
			}
		}
	}
	return best
}

// gcParents is every task's chain of real parents for beads.Lineage: the
// parents the one list already carries, plus one batched fresh read of the
// beads it names no parent for (roots, mostly), repeated while a read chain
// goes further. A failed read leaves those ids out: Lineage then reads them
// itself, as before.
func gcParents(ctx context.Context, c beads.Client, dir string, byID map[string]beads.Bead, tasks []string) beads.Parents {
	known := beads.Parents{}
	for id, b := range byID {
		if p := b.Parent; p != "" {
			known[id] = p
		} else if p := b.ParentID; p != "" {
			known[id] = p
		}
	}
	if c == nil {
		return known
	}
	for round := 0; round < 8; round++ {
		need := map[string]bool{}
		for _, t := range tasks {
			seen := map[string]bool{}
			for cur := t; cur != "" && !seen[cur]; {
				seen[cur] = true
				p, ok := known[cur]
				if !ok {
					need[cur] = true
					break
				}
				cur = p
			}
		}
		if len(need) == 0 {
			break
		}
		ids := make([]string, 0, len(need))
		for id := range need {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		got, err := beads.FreshDetails(ctx, c, dir, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "worktrees gc: parents of %d beads in %s: %v (read one by one)\n", len(ids), dir, err)
			break
		}
		for _, id := range ids {
			if d, ok := got[id]; ok {
				known[id] = d.Parent
			} else {
				known[id] = beads.ParentOf(id) // not readable: the dotted id, as Lineage would
			}
		}
	}
	return known
}

func endsWithTask(name, id string) bool {
	return name == id || strings.HasSuffix(name, "-"+id)
}

func printGCReport(w *os.File, r *projectGCReport) {
	mode := "DRY RUN"
	if r.Apply {
		mode = "APPLIED"
	}
	fmt.Fprintf(w, "== %s (%s) %s\n", r.Project, r.Repo, mode)
	if r.Warning != "" {
		fmt.Fprintf(w, "WARNING: %s\n", r.Warning)
	}
	if !r.Apply {
		fmt.Fprintf(w, "this dry run writes: %s\n", strings.Join(worktreelock.DryRunFiles(r.ProjectDir), ", "))
	}
	fmt.Fprintf(w, "worktrees: %d, candidates: %d, archive-needed: %d, removed: %d, after: %d, errors: %d\n",
		r.Before, r.Candidates, r.ArchiveNeeded, r.Removed, r.After, r.Errors)
	var reasons []string
	for k := range r.Kept {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	fmt.Fprintln(w, "kept by reason:")
	for _, k := range reasons {
		fmt.Fprintf(w, "  %5d  %s\n", r.Kept[k], k)
	}
	for _, e := range r.Entries {
		switch {
		case e.Remove:
			fmt.Fprintf(w, "CANDIDATE %s [%s] %s\n", e.Path, e.Branch, e.Reason)
		case e.Archive != "":
			fmt.Fprintf(w, "ARCHIVE-NEEDED %s [%s] -> %s\n", e.Path, e.Branch, e.Archive)
		}
		if e.Error != "" {
			fmt.Fprintf(w, "ERROR %s: %s\n", e.Path, e.Error)
		}
	}
}
