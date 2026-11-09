package worktreelock

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gcRepo(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	git(t, project, "init", "-q", "-b", "main")
	git(t, project, "config", "user.name", "t")
	git(t, project, "config", "user.email", "t@t")
	os.WriteFile(filepath.Join(project, "a.txt"), []byte("a"), 0o644)
	git(t, project, "add", ".")
	git(t, project, "commit", "-qm", "a")
	// spawn.appendGitExcludes does this for a real project.
	os.WriteFile(filepath.Join(project, ".git", "info", "exclude"), []byte(".worktrees/\n"), 0o644)
	t.Cleanup(func() { // frozen worktrees are read-only
		filepath.WalkDir(filepath.Join(project, ".worktrees"), func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
		os.Chmod(filepath.Join(project, ".worktrees"), 0o755)
		ents, _ := os.ReadDir(filepath.Join(project, ".worktrees"))
		for _, e := range ents {
			Unlock(filepath.Join(project, ".worktrees", e.Name()))
		}
	})
	return project
}

// gcWorktree adds a frozen worktree on branch with one commit; merged fast-forwards main to it.
func gcWorktree(t *testing.T, project, name, branch string, merged bool) string {
	t.Helper()
	wt := filepath.Join(project, ".worktrees", name)
	git(t, project, "worktree", "add", "-q", "-b", branch, wt)
	os.WriteFile(filepath.Join(wt, name+".txt"), []byte(name), 0o644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", name)
	if merged {
		git(t, project, "merge", "-q", "--no-edit", branch)
	}
	if err := Lock(wt); err != nil {
		t.Fatal(err)
	}
	return wt
}

func gcRun(t *testing.T, project string, apply bool, lookup func(dir, branch string) GCFacts) *GCReport {
	t.Helper()
	if lookup != nil {
		inner := lookup
		lookup = func(dir, branch string) GCFacts { f := inner(dir, branch); f.Matched = true; return f }
	}
	rep, err := GC(context.Background(), GCOptions{
		Repo: project, Apply: apply, Archive: true, OlderThan: time.Hour, Now: time.Now().Add(48 * time.Hour), Lookup: lookup,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func gcEntry(t *testing.T, rep *GCReport, wt string) GCEntry {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Path == wt || strings.HasSuffix(e.Path, wt) {
			return e
		}
	}
	t.Fatalf("no entry for %s in %+v", wt, rep.Entries)
	return GCEntry{}
}

func branchExists(project, branch string) bool {
	return exec.Command("git", "-C", project, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func TestWorktreeGCRemovesMergedCleanWorktree(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-x", "op/x", true)
	dry := gcRun(t, project, false, nil)
	if e := gcEntry(t, dry, "op-x"); !e.Remove || e.Archive != "" {
		t.Fatalf("dry run entry = %+v", e)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("dry run touched the worktree: %v", err)
	}
	rep := gcRun(t, project, true, nil)
	if rep.Removed != 1 || rep.Before != 1 || rep.After != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}
	if branchExists(project, "op/x") {
		t.Error("merged branch not deleted")
	}
}

func TestWorktreeGCArchivesUniqueCommitsThenRemoves(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-u", "op/u", false)
	tip := strings.TrimSpace(git(t, project, "rev-parse", "op/u"))
	rep := gcRun(t, project, true, nil)
	e := gcEntry(t, rep, "op-u")
	want := "refs/archive/op/u-" + tip[:7]
	if e.Archive != want || !e.Removed || rep.ArchiveNeeded != 1 {
		t.Fatalf("entry = %+v, report = %+v, want archive %s", e, rep, want)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk")
	}
	if branchExists(project, "op/u") {
		t.Error("branch kept")
	}
	// The archive ref survives and can recreate the branch with the work.
	git(t, project, "branch", "op/u-back", want)
	if got := strings.TrimSpace(git(t, project, "rev-parse", "op/u-back")); got != tip {
		t.Errorf("recreated branch at %s, want %s", got, tip)
	}
}

func TestWorktreeGCKeepsDirtyWorktree(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-d", "op/d", true)
	if err := Unlock(wt); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("wip"), 0o644)
	rep := gcRun(t, project, true, nil)
	if e := gcEntry(t, rep, "op-d"); e.Remove || e.Reason != ReasonDirty {
		t.Fatalf("entry = %+v", e)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dirty worktree removed: %v", err)
	}
}

func TestWorktreeGCKeepsRunningAndHeld(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-r", "op/r", true)
	gcWorktree(t, project, "op-h", "op/h", true)
	rep := gcRun(t, project, true, func(dir, branch string) GCFacts {
		return GCFacts{Running: branch == "op/r", Held: branch == "op/h", TaskClosed: true}
	})
	if e := gcEntry(t, rep, "op-r"); e.Remove || e.Reason != ReasonRunning {
		t.Errorf("running entry = %+v", e)
	}
	if e := gcEntry(t, rep, "op-h"); e.Remove || e.Reason != ReasonHeld {
		t.Errorf("held entry = %+v", e)
	}
	if rep.Removed != 0 {
		t.Errorf("removed %d", rep.Removed)
	}
}

func TestWorktreeGCKeepsNonOpBranchAndNeverListsMain(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "old-x", "old/x", true)
	rep := gcRun(t, project, true, nil)
	if e := gcEntry(t, rep, "old-x"); e.Remove || e.Reason != ReasonNotOp {
		t.Errorf("entry = %+v", e)
	}
	for _, e := range rep.Entries {
		if filepath.Clean(e.Path) == filepath.Clean(project) || e.Branch == "main" {
			t.Errorf("main checkout listed: %+v", e)
		}
	}
	if !branchExists(project, "old/x") {
		t.Error("non-op branch deleted")
	}
}

func TestWorktreeGCKeepsRecentOpenTask(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-n", "op/n", true)
	rep, err := GC(context.Background(), GCOptions{Repo: project, OlderThan: time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-n"); e.Remove || e.Reason != ReasonRecent {
		t.Errorf("entry = %+v", e)
	}
}

func TestWorktreeGCKeepsOriginOnlyCommitsWithoutArchive(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-o", "op/o", false)
	tip := strings.TrimSpace(git(t, project, "rev-parse", "op/o"))
	git(t, project, "update-ref", "refs/remotes/origin/op/o", tip)
	rep := gcRun(t, project, false, nil)
	if e := gcEntry(t, rep, "op-o"); !e.Remove || e.Archive != "" {
		t.Errorf("entry = %+v", e)
	}
}

func TestWorktreeGCCapsRemovals(t *testing.T) {
	project := gcRepo(t)
	for i := 0; i < 5; i++ {
		gcWorktree(t, project, fmt.Sprintf("op-c%d", i), fmt.Sprintf("op/c%d", i), true)
	}
	rep, err := GC(context.Background(), GCOptions{
		Repo: project, Apply: true, OlderThan: time.Hour, Now: time.Now().Add(48 * time.Hour), Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 3 || rep.After != 2 || rep.Kept[ReasonCapped] != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestWorktreeGCContinuesAfterGitFailure(t *testing.T) {
	project := gcRepo(t)
	bad := gcWorktree(t, project, "op-a", "op/a", true)
	gcWorktree(t, project, "op-b", "op/b", true)
	// A broken .git file makes git fail in that worktree only.
	os.Chmod(bad, 0o755)
	os.Chmod(filepath.Join(bad, ".git"), 0o644)
	os.WriteFile(filepath.Join(bad, ".git"), []byte("gitdir: /nonexistent\n"), 0o644)
	rep := gcRun(t, project, true, nil)
	if e := gcEntry(t, rep, "op-a"); e.Remove || !strings.HasPrefix(e.Reason, "git error") {
		t.Errorf("broken entry = %+v", e)
	}
	if e := gcEntry(t, rep, "op-b"); !e.Removed {
		t.Errorf("good entry not removed: %+v", e)
	}
}

func TestWorktreeGCRefusesDirtyMainCheckout(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-m", "op/m", true)
	os.WriteFile(filepath.Join(project, "a.txt"), []byte("changed"), 0o644)
	if _, err := GC(context.Background(), GCOptions{Repo: project, Apply: true}); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("err = %v, want dirty refusal", err)
	}
}

func TestWorktreeGCKeepsUniqueCommitsByDefault(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-k", "op/k", false)
	rep, err := GC(context.Background(), GCOptions{
		Repo: project, Apply: true, OlderThan: time.Hour, Now: time.Now().Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-k"); e.Remove || e.Reason != ReasonUnique || e.Archive == "" || rep.ArchiveNeeded != 1 {
		t.Fatalf("entry = %+v report = %+v", e, rep)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree removed: %v", err)
	}
}

func TestWorktreeGCHonoursKeepList(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-keepme", "op/keepme", true)
	gcWorktree(t, project, "op-other", "op/other", true)
	os.MkdirAll(filepath.Join(project, ".commander"), 0o755)
	os.WriteFile(filepath.Join(project, ".commander", "worktree-keep"), []byte("# retained\n*keepme*\n"), 0o644)
	rep, err := GC(context.Background(), GCOptions{
		Repo: project, Apply: true, OlderThan: time.Hour, Now: time.Now().Add(48 * time.Hour), Keep: mustKeep(t, project),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-keepme"); e.Remove || e.Reason != ReasonKeepList {
		t.Errorf("keep entry = %+v", e)
	}
	if e := gcEntry(t, rep, "op-other"); !e.Removed {
		t.Errorf("other entry = %+v", e)
	}
}

func gcOpts(project string) GCOptions {
	return GCOptions{Repo: project, ProjectDir: project, Archive: true, OlderThan: time.Hour, Now: time.Now().Add(48 * time.Hour)}
}

func writeKeepFile(t *testing.T, project, body string) []string {
	t.Helper()
	os.MkdirAll(filepath.Join(project, ".commander"), 0o755)
	if err := os.WriteFile(filepath.Join(project, ".commander", "worktree-keep"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return mustKeep(t, project)
}

func mustKeep(t *testing.T, project string) []string {
	t.Helper()
	keep, err := LoadKeepList(project)
	if err != nil {
		t.Fatal(err)
	}
	return keep
}

func TestWorktreeGCKeepsIgnoredScratchContent(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-scratch", "op/scratch", true)
	Unlock(wt)
	os.WriteFile(filepath.Join(project, ".git", "info", "exclude"), []byte(".worktrees/\nscratch/\n"), 0o644)
	os.MkdirAll(filepath.Join(wt, "scratch"), 0o755)
	os.WriteFile(filepath.Join(wt, "scratch", "notes.txt"), []byte("work"), 0o644)
	Lock(wt)
	rep, err := GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	e := gcEntry(t, rep, "op-scratch")
	if e.Remove || !strings.HasPrefix(e.Reason, ReasonIgnored+": scratch/") || rep.Kept[ReasonIgnored] != 1 {
		t.Fatalf("entry = %+v kept = %v", e, rep.Kept)
	}
}

func TestWorktreeGCAllowlistedCachesOnlyIsCandidate(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-cache", "op/cache", true)
	Unlock(wt)
	os.WriteFile(filepath.Join(project, ".git", "info", "exclude"), []byte(".worktrees/\nnode_modules/\n__pycache__/\n*.pyc\n.DS_Store\n"), 0o644)
	os.MkdirAll(filepath.Join(wt, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(wt, "node_modules", "x", "i.js"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(wt, "pkg", "__pycache__"), 0o755)
	os.WriteFile(filepath.Join(wt, "pkg", "__pycache__", "a.pyc"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(wt, ".DS_Store"), []byte("x"), 0o644)
	Lock(wt)
	rep, err := GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-cache"); !e.Remove {
		t.Fatalf("entry = %+v", e)
	}
}

func TestWorktreeGCKeepGlobForms(t *testing.T) {
	cases := []struct {
		name, pattern, dir, branch, task string
	}{
		{"dir name", "op-t06-*", "op-t06-1", "op/t06-1", ""},
		{"relative path", ".worktrees/op-rel", "op-rel", "op/rel", ""},
		{"double star", "**/op-deep", "op-deep", "op/deep", ""},
		{"branch with prefix", "op/br-*", "op-br", "op/br-one", ""},
		{"branch without prefix", "named-branch", "op-nb", "op/named-branch", ""},
		{"task id", "proj-1.2.3", "op-task", "op/task", "proj-1.2.3"},
		{"question and class", "op-q[0-9]?", "op-q1x", "op/q1x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := gcRepo(t)
			gcWorktree(t, project, c.dir, c.branch, true)
			gcWorktree(t, project, "op-plain", "op/plain", true)
			o := gcOpts(project)
			o.Keep = writeKeepFile(t, project, "# c\n"+c.pattern+"\n")
			o.Lookup = func(dir, branch string) GCFacts {
				if c.task != "" && branch == c.branch {
					return GCFacts{Matched: true, TaskID: c.task, TaskClosed: true}
				}
				return GCFacts{Matched: true, TaskClosed: true}
			}
			rep, err := GC(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if e := gcEntry(t, rep, c.dir); e.Remove || e.Reason != ReasonKeepList {
				t.Errorf("kept entry = %+v", e)
			}
			if e := gcEntry(t, rep, "op-plain"); !e.Remove {
				t.Errorf("plain entry = %+v", e)
			}
		})
	}
}

func TestWorktreeGCKeepFileWinsOverEveryRule(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-win", "op/win", true)
	gcWorktree(t, project, "other-win", "feature/win", true)
	o := gcOpts(project)
	o.Keep = writeKeepFile(t, project, "*win*\n")
	o.Lookup = func(dir, branch string) GCFacts { return GCFacts{Matched: true, TaskClosed: true} }
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"op-win", "other-win"} {
		if e := gcEntry(t, rep, n); e.Remove || e.Reason != ReasonKeepList {
			t.Errorf("%s = %+v", n, e)
		}
	}
	if !rep.KeepFile {
		t.Error("KeepFile false")
	}
}

func TestWorktreeGCKeepMarker(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-marked", "op/marked", true)
	Unlock(wt)
	os.WriteFile(filepath.Join(wt, ".keep"), nil, 0o644)
	Lock(wt)
	o := gcOpts(project)
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-marked"); e.Remove || e.Reason != ReasonKeepMark {
		t.Fatalf("entry = %+v", e)
	}
}

func TestWorktreeGCMissingKeepFileIsEmptyList(t *testing.T) {
	project := gcRepo(t)
	if got := mustKeep(t, project); got != nil {
		t.Fatalf("keep list = %v", got)
	}
	gcWorktree(t, project, "op-nokeep", "op/nokeep", true)
	rep, err := GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	if rep.KeepFile || !gcEntry(t, rep, "op-nokeep").Remove {
		t.Fatalf("report = %+v", rep)
	}
}

func TestWorktreeGCApplyNeedsFreshDryRun(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-fresh", "op/fresh", true)
	o := gcOpts(project)
	o.Apply = true
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "dry run") {
		t.Fatalf("no dry run: err = %v", err)
	}
	dry := o
	dry.Apply = false
	if _, err := GC(context.Background(), dry); err != nil {
		t.Fatal(err)
	}
	stale := o
	stale.Now = o.Now.Add(2 * time.Hour)
	if _, err := GC(context.Background(), stale); err == nil || !strings.Contains(err.Error(), "dry run") {
		t.Fatalf("stale dry run: err = %v", err)
	}
	rep, err := GC(context.Background(), o)
	if err != nil || rep.Removed != 1 {
		t.Fatalf("after dry run: rep = %+v err = %v", rep, err)
	}
}

func TestWorktreeGCCrowdedProjectNeedsKeepFile(t *testing.T) {
	old := CrowdedProject
	CrowdedProject = 2
	t.Cleanup(func() { CrowdedProject = old })
	project := gcRepo(t)
	gcWorktree(t, project, "op-c1", "op/c1", true)
	gcWorktree(t, project, "op-c2", "op/c2", true)
	o := gcOpts(project)
	dry := o
	if _, err := GC(context.Background(), dry); err != nil {
		t.Fatal(err)
	}
	o.Apply = true
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "keep file") {
		t.Fatalf("err = %v, want keep file refusal", err)
	}
	o.AllowNoKeepFile = true
	if rep, err := GC(context.Background(), o); err != nil || rep.Removed != 2 {
		t.Fatalf("dangerous flag: rep = %+v err = %v", rep, err)
	}
}

func TestWorktreeGCCrowdedProjectWithKeepFileApplies(t *testing.T) {
	old := CrowdedProject
	CrowdedProject = 2
	t.Cleanup(func() { CrowdedProject = old })
	project := gcRepo(t)
	gcWorktree(t, project, "op-d1", "op/d1", true)
	gcWorktree(t, project, "op-d2", "op/d2", true)
	o := gcOpts(project)
	o.Keep = writeKeepFile(t, project, "nothing-matches\n")
	if _, err := GC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Apply = true
	if rep, err := GC(context.Background(), o); err != nil || rep.Removed != 2 {
		t.Fatalf("rep = %+v err = %v", rep, err)
	}
}

// noindexLayout makes <project>/.worktrees a link to <project>/.worktrees.noindex,
// the way Claude Code lays it out, and returns the real folder.
func noindexLayout(t *testing.T, project string) string {
	t.Helper()
	real := filepath.Join(project, ".worktrees.noindex")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(project, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.WalkDir(real, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return real
}

func TestWorktreeGCWorktreesNoindexFolderIsACandidate(t *testing.T) {
	project := gcRepo(t)
	real := filepath.Join(project, ".worktrees.noindex")
	os.MkdirAll(real, 0o755)
	wt := filepath.Join(real, "op-n")
	git(t, project, "worktree", "add", "-q", "-b", "op/n", wt)
	if err := Lock(wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Unlock(wt) })
	o := gcOpts(project)
	o.Lookup = matchedClosed
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-n"); !e.Remove || e.Reason == ReasonOutside {
		t.Fatalf("entry = %+v", e)
	}
	o.Apply = true
	rep, err = GC(context.Background(), o)
	if err != nil || rep.Removed != 1 {
		t.Fatalf("rep = %+v err = %v", rep, err)
	}
}

func TestWorktreeGCSymlinkedWorktreesFolderCountsOnce(t *testing.T) {
	project := gcRepo(t)
	real := noindexLayout(t, project)
	// Added through the link; git records one path, the entry must be one either way.
	wt := gcWorktree(t, project, "op-s", "op/s", true)
	o := gcOpts(project)
	o.Lookup = matchedClosed
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Before != 1 || rep.Candidates != 1 || len(rep.Entries) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if e := gcEntry(t, rep, "op-s"); !e.Remove || !strings.HasPrefix(e.Path, realPath(real)) {
		t.Fatalf("entry = %+v (wt %s)", e, wt)
	}
	// A record naming the worktree by the link path covers the same entry.
	rec := filepath.Join(project, ".commander", dryRunFile)
	data, _ := os.ReadFile(rec)
	data = []byte(strings.ReplaceAll(string(data), ".worktrees.noindex", ".worktrees"))
	os.WriteFile(rec, data, 0o600)
	o.Apply = true
	rep, err = GC(context.Background(), o)
	if err != nil || rep.Removed != 1 {
		t.Fatalf("rep = %+v err = %v", rep, err)
	}
}

func TestWorktreeGCKeepGlobNamesEitherFolder(t *testing.T) {
	for _, glob := range []string{".worktrees/op-k", ".worktrees.noindex/op-k", "**/op-k"} {
		project := gcRepo(t)
		noindexLayout(t, project)
		gcWorktree(t, project, "op-k", "op/k", true)
		o := gcOpts(project)
		o.Keep = writeKeepFile(t, project, glob+"\n")
		rep, err := GC(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if e := gcEntry(t, rep, "op-k"); e.Remove || e.Reason != ReasonKeepList {
			t.Errorf("glob %q: entry = %+v", glob, e)
		}
	}
}

func TestWorktreeGCOutsideBothFoldersIsKept(t *testing.T) {
	project := gcRepo(t)
	noindexLayout(t, project)
	other := filepath.Join(t.TempDir(), "op-o")
	git(t, project, "worktree", "add", "-q", "-b", "op/o", other)
	rep, err := GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-o"); e.Remove || e.Reason != ReasonOutside {
		t.Fatalf("entry = %+v", e)
	}
}

func matchedClosed(dir, branch string) GCFacts { return GCFacts{Matched: true, TaskClosed: true} }

func TestWorktreeGCKeepsWorktreesOutsideProjectAndWithoutRun(t *testing.T) {
	project := gcRepo(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	git(t, project, "worktree", "add", "-q", "-b", "op/outside", outside)
	gcWorktree(t, project, "op-norun", "op/norun", true)
	o := gcOpts(project)
	o.Lookup = func(dir, branch string) GCFacts { return GCFacts{Matched: branch != "op/norun", TaskClosed: true} }
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "elsewhere"); e.Remove || e.Reason != ReasonOutside {
		t.Errorf("outside entry = %+v", e)
	}
	if e := gcEntry(t, rep, "op-norun"); e.Remove || e.Reason != ReasonNoRun {
		t.Errorf("no-run entry = %+v", e)
	}
}

func TestWorktreeGCApplyBoundToReviewedDryRun(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-b1", "op/b1", true)
	gcWorktree(t, project, "op-b2", "op/b2", false) // unique commits: only a candidate with --archive
	o := gcOpts(project)
	o.Archive = false
	o.Lookup = matchedClosed
	if _, err := GC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// Different flags: the archive set was never reviewed.
	o.Apply, o.Archive = true, true
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("archive flag mismatch: err = %v", err)
	}
	o.Archive = false
	o.OlderThan = 2 * time.Hour
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("older-than mismatch: err = %v", err)
	}
	// A new candidate since the dry run is a superset.
	o.OlderThan = time.Hour
	gcWorktree(t, project, "op-b3", "op/b3", true)
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "did not list") {
		t.Fatalf("superset: err = %v", err)
	}
	if !branchExists(project, "op/b1") || !branchExists(project, "op/b3") {
		t.Error("a refused apply removed something")
	}
}

func TestWorktreeGCApplyRefusesMovedTipAndRecordIs0600(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-m", "op/m", true)
	o := gcOpts(project)
	o.Lookup = matchedClosed
	if _, err := GC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(project, ".commander", dryRunFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record = %v, %v", info, err)
	}
	Unlock(wt)
	os.WriteFile(filepath.Join(wt, "more.txt"), []byte("x"), 0o644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "more")
	Lock(wt)
	o.Apply = true
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("moved tip: err = %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree removed: %v", err)
	}
}

func TestWorktreeGCRechecksRightBeforeRemoval(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-late", "op/late", true)
	o := gcOpts(project)
	calls := 0
	o.Lookup = func(dir, branch string) GCFacts {
		calls++
		// The Operator is respawned after the first decision.
		return GCFacts{Matched: true, TaskClosed: true, Running: calls > 1}
	}
	if _, err := GC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	calls = 0
	o.Apply = true
	rep, err := GC(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-late"); e.Removed || e.Reason != ReasonChanged || rep.Removed != 0 {
		t.Fatalf("entry = %+v", e)
	}
	if _, err := os.Stat(wt); err != nil || !branchExists(project, "op/late") {
		t.Error("respawned worktree or branch removed")
	}
}

func TestWorktreeGCBranchKeptWhenTipMovesDuringRemoval(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-tip", "op/tip", true)
	g := gitRunner{repo: project, timeout: time.Minute}
	e := &GCEntry{Path: filepath.Join(project, ".worktrees", "op-tip"), Branch: "op/tip", Tip: strings.TrimSpace(git(t, project, "rev-parse", "main~1"))}
	if err := g.remove(context.Background(), e); err == nil {
		t.Fatal("remove deleted a branch whose tip differs from the decided one")
	}
	if !branchExists(project, "op/tip") {
		t.Error("branch deleted at a tip that was never decided")
	}
}

func TestWorktreeGCDirtyFilterMatchesRootBeadsOnly(t *testing.T) {
	project := gcRepo(t)
	wt := gcWorktree(t, project, "op-beads", "op/beads", true)
	Unlock(wt)
	os.MkdirAll(filepath.Join(wt, ".beads"), 0o755)
	os.WriteFile(filepath.Join(wt, ".beads", "x"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(wt, "docs", "sub.beads"), 0o755)
	os.WriteFile(filepath.Join(wt, "docs", "sub.beads", "y"), []byte("y"), 0o644)
	Lock(wt)
	rep, err := GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-beads"); e.Remove || e.Reason != ReasonDirty {
		t.Fatalf("entry = %+v", e)
	}
	Unlock(wt)
	os.RemoveAll(filepath.Join(wt, "docs", "sub.beads"))
	Lock(wt)
	rep, err = GC(context.Background(), gcOpts(project))
	if err != nil {
		t.Fatal(err)
	}
	if e := gcEntry(t, rep, "op-beads"); !e.Remove {
		t.Fatalf("root .beads/ alone should not keep it: %+v", e)
	}
}

func TestWorktreeGCInvalidKeepPatternAndUnreadableKeepFileAreErrors(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-kp", "op/kp", true)
	o := gcOpts(project)
	o.Keep = []string{"[z-a]"}
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid pattern: err = %v", err)
	}
	path := filepath.Join(project, ".commander", "worktree-keep")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("x\n"), 0o000)
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	if os.Geteuid() != 0 {
		if _, err := LoadKeepList(project); err == nil {
			t.Fatal("unreadable keep file loaded as empty")
		}
	}
	// An empty keep file does not satisfy the crowded-project guard.
	os.Chmod(path, 0o644)
	os.WriteFile(path, []byte("# nothing\n"), 0o644)
	old := CrowdedProject
	CrowdedProject = 1
	t.Cleanup(func() { CrowdedProject = old })
	o.Keep = mustKeep(t, project)
	if _, err := GC(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Apply = true
	if _, err := GC(context.Background(), o); err == nil || !strings.Contains(err.Error(), "keep file") {
		t.Fatalf("empty keep file passed the guard: err = %v", err)
	}
}

func TestWorktreeGCIgnoredAllowlistOnlyTopLevelOrNextToManifest(t *testing.T) {
	wt := t.TempDir()
	os.MkdirAll(filepath.Join(wt, "app"), 0o755)
	os.WriteFile(filepath.Join(wt, "app", "package.json"), []byte("{}"), 0o644)
	for p, want := range map[string]bool{
		"build/":                  true,
		"target/":                 true,
		"app/dist/":               true,
		"data/build/important.db": false,
		"notes/target/x":          false,
		"pkg/__pycache__/a.pyc":   true,
		"node_modules/":           true,
	} {
		if got := ignoredRebuildable(wt, p); got != want {
			t.Errorf("ignoredRebuildable(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestWorktreeGCDryRunWritesOnlyItsListedFiles(t *testing.T) {
	project := gcRepo(t)
	gcWorktree(t, project, "op-w", "op/w", true)
	if _, err := GC(context.Background(), gcOpts(project)); err != nil {
		t.Fatal(err)
	}
	for _, f := range DryRunFiles(project) {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("listed file %s missing: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(project, ".worktrees", ".metadata_never_index")); err == nil {
		t.Error("gc wrote a Spotlight marker")
	}
}
