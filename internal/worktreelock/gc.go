package worktreelock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Why a worktree is kept (GCEntry.Reason) or removed.
const (
	ReasonRunning  = "running Operator"
	ReasonHeld     = "held (bead label)"
	ReasonNotOp    = "not an op/* branch"
	ReasonRecent   = "task open and run recent"
	ReasonDirty    = "uncommitted or untracked files"
	ReasonLocked   = "locked by something other than a closed run"
	ReasonMissing  = "directory missing (git worktree prune clears the entry)"
	ReasonCapped   = "over the per-run cap, next run"
	ReasonKeepList = "on the project keep list"
	ReasonKeepMark = "per-worktree .keep marker"
	ReasonIgnored  = "ignored content"
	ReasonUnique   = "unique unshipped commits (rerun with --archive to archive them first)"
	ReasonOutside  = "outside <project>/.worktrees (or .worktrees.noindex)"
	ReasonNoRun    = "no matching Operator run"
	ReasonChanged  = "changed since it was decided (status, tip or run)"
	DefaultGCLimit = 100

	// DryRunMaxAge is how fresh a dry run must be for --apply (per project).
	DryRunMaxAge = time.Hour
	dryRunFile   = "gc-dryrun.json"
)

// CrowdedProject is the worktree count from which --apply needs a keep file.
var CrowdedProject = 100

// ignoredAllowed are directory names (any depth) of rebuildable caches; an
// ignored path with one of them as a component does not stop a removal.
var ignoredAllowed = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".pytest_cache": true,
	".mypy_cache": true, ".ruff_cache": true, ".next": true, ".gradle": true, ".tox": true,
}

// ignoredAllowedTop are common names for real data too (dist, build, target,
// coverage, .cache): rebuildable only at the worktree's top level or next to a
// project manifest, never as data/build/x.db.
var ignoredAllowedTop = map[string]bool{"dist": true, "build": true, "target": true, "coverage": true, ".cache": true}

var manifests = []string{"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "setup.py", "pom.xml", "build.gradle", "build.gradle.kts", "requirements.txt", "Gemfile", "Makefile"}

// ignoredAllowedFile are ignored file globs that are rebuildable (bytecode, OS
// litter, Go build output).
var ignoredAllowedFile = []string{"*.pyc", ".DS_Store", "*.test", "*.exe", "*.out", "*.o", "*.a"}

// GCFacts is what the caller knows about a worktree's run and task bead.
type GCFacts struct {
	Running      bool
	Held         bool
	TaskClosed   bool
	Matched      bool      // an Operator run of this project owns the worktree; without it the worktree is kept
	TaskID       string    // the task bead id, matched by keep patterns
	LastActivity time.Time // zero = unknown (the directory's mtime is used)
}

// GCOptions configures one GC pass over a project's git repo.
type GCOptions struct {
	Repo      string        // the main checkout (git root worktrees come from)
	Apply     bool          // false = dry run: nothing is changed
	OlderThan time.Duration // a run older than this counts as finished even with an open task
	Now       time.Time
	Limit     int           // max removals per pass (0 = DefaultGCLimit)
	Timeout   time.Duration // per git command (0 = 2 minutes)
	Lookup    func(dir, branch string) GCFacts
	Archive   bool     // archive unique commits under refs/archive/op/ and remove; false = keep such worktrees
	Keep      []string // keep list globs (LoadKeepList)

	// ProjectDir is the project root (where .commander/ and .worktrees/ are).
	// When set, keep patterns also match the path relative to it, the dry run
	// is recorded there, and Apply needs a fresh dry run and, on a crowded
	// project, a keep file.
	ProjectDir      string
	AllowNoKeepFile bool // Apply on CrowdedProject+ worktrees without a keep file (the dangerous flag)
}

// GCEntry is one worktree and what GC decided or did.
type GCEntry struct {
	Path    string `json:"path"`
	Branch  string `json:"branch,omitempty"`
	Tip     string `json:"tip,omitempty"` // the branch tip it was decided at
	Remove  bool   `json:"remove"`
	Reason  string `json:"reason"`
	Archive string `json:"archive,omitempty"` // archive ref written (or to be written) first
	Removed bool   `json:"removed,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GCReport is the outcome of a pass.
type GCReport struct {
	Repo          string         `json:"repo"`
	Apply         bool           `json:"apply"`
	Warning       string         `json:"warning,omitempty"`
	Before        int            `json:"before"` // worktrees besides the main checkout
	After         int            `json:"after"`
	Candidates    int            `json:"candidates"`
	ArchiveNeeded int            `json:"archive_needed"`
	Removed       int            `json:"removed"`
	Kept          map[string]int `json:"kept"`
	Errors        int            `json:"errors"`
	KeepFile      bool           `json:"keep_file"`
	Entries       []GCEntry      `json:"entries"`
}

type wtEntry struct {
	path, branch, lockReason string
	locked, prunable         bool
}

// GC finds finished Operator worktrees and, with Apply, removes them. A
// branch with commits that are on neither main nor a remote is archived under
// refs/archive/op/ first, so the work stays recoverable from the main repo.
// Held, running, dirty, locked-by-something-else and non-op/* worktrees are
// always kept. It changes nothing unless the main checkout is clean.
func GC(ctx context.Context, o GCOptions) (*GCReport, error) {
	if o.Limit <= 0 {
		o.Limit = DefaultGCLimit
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	g := gitRunner{repo: o.Repo, timeout: o.Timeout}
	warning := ""
	if err := g.checkMainCheckout(ctx); err != nil {
		if o.Apply {
			return nil, err
		}
		warning = err.Error() + " (--apply would refuse)"
	}
	main := mainBranch(o.Repo)
	if main == "" {
		return nil, fmt.Errorf("%s has no main or master branch", o.Repo)
	}
	if err := validateKeep(o.Keep); err != nil {
		return nil, err
	}
	keepFile := false
	var recorded *dryRunRecord
	if o.ProjectDir != "" {
		_, err := os.Stat(filepath.Join(o.ProjectDir, ".commander", "worktree-keep"))
		keepFile = err == nil && len(o.Keep) > 0
		if o.Apply {
			rec, err := requireFreshDryRun(o.ProjectDir, o.Now)
			if err != nil {
				return nil, err
			}
			recorded = rec
		}
	}
	if o.Apply {
		unlock, err := lockRepo(o.Repo)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	entries, err := g.list(ctx)
	if err != nil {
		return nil, err
	}
	if o.Apply && o.ProjectDir != "" && len(entries) >= CrowdedProject && !keepFile && !o.AllowNoKeepFile {
		return nil, fmt.Errorf("%s has %d worktrees and no %s: write the keep file first (or pass --dangerous-no-keep-file)",
			o.ProjectDir, len(entries), filepath.Join(".commander", "worktree-keep"))
	}
	rep := &GCReport{Repo: o.Repo, Apply: o.Apply, Warning: warning, Before: len(entries), Kept: map[string]int{}, KeepFile: keepFile}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	byPath := map[string]wtEntry{}
	for _, e := range entries {
		byPath[e.path] = e
		rep.Entries = append(rep.Entries, g.decide(ctx, o, main, e))
	}
	for i := range rep.Entries {
		e := &rep.Entries[i]
		if !e.Remove {
			rep.Kept[keptCategory(e.Reason)]++
			if e.Reason == ReasonUnique {
				rep.ArchiveNeeded++
			}
			continue
		}
		rep.Candidates++
		if e.Archive != "" {
			rep.ArchiveNeeded++
		}
		if rep.Candidates > o.Limit {
			e.Remove = false
			e.Archive = ""
			rep.Candidates--
			rep.Kept[ReasonCapped]++
			e.Reason = ReasonCapped
			continue
		}
	}
	if o.Apply {
		if recorded != nil {
			if err := recorded.covers(o, rep.Entries); err != nil {
				return nil, err
			}
		}
		for i := range rep.Entries {
			e := &rep.Entries[i]
			if !e.Remove {
				continue
			}
			// Minutes may have passed since decide: look again right before removing.
			if now := g.decide(ctx, o, main, byPath[e.Path]); !now.Remove || now.Tip != e.Tip || now.Archive != e.Archive {
				e.Remove = false
				e.Archive = ""
				e.Reason = ReasonChanged
				rep.Candidates--
				rep.Kept[ReasonChanged]++
				continue
			}
			if err := g.remove(ctx, e); err != nil {
				e.Error = err.Error()
				rep.Errors++
				continue
			}
			e.Removed = true
			rep.Removed++
		}
		_, _ = g.run(ctx, "worktree", "prune")
		if after, err := g.list(ctx); err == nil {
			rep.After = len(after)
		} else {
			rep.After = rep.Before - rep.Removed
		}
	} else {
		rep.After = rep.Before - rep.Candidates
		if o.ProjectDir != "" {
			writeDryRun(o.ProjectDir, o, rep)
		}
	}
	return rep, nil
}

// decide classifies one worktree. It only reads.
func (g gitRunner) decide(ctx context.Context, o GCOptions, main string, e wtEntry) GCEntry {
	out := GCEntry{Path: e.path, Branch: e.branch}
	keep := func(reason string) GCEntry { out.Reason = reason; return out }
	facts := GCFacts{}
	if o.Lookup != nil {
		facts = o.Lookup(e.path, e.branch)
	}
	if o.ProjectDir != "" && !underProjectWorktrees(o.ProjectDir, e.path) {
		return keep(ReasonOutside)
	}
	// The keep list and the .keep marker win over every other rule.
	if keptByList(o.Keep, o.ProjectDir, e, facts.TaskID) {
		return keep(ReasonKeepList)
	}
	if _, err := os.Stat(filepath.Join(e.path, ".keep")); err == nil {
		return keep(ReasonKeepMark)
	}
	if !strings.HasPrefix(e.branch, "op/") {
		return keep(ReasonNotOp)
	}
	if o.Lookup != nil && !facts.Matched {
		return keep(ReasonNoRun)
	}
	if facts.Running {
		return keep(ReasonRunning)
	}
	if facts.Held {
		return keep(ReasonHeld)
	}
	info, err := os.Stat(e.path)
	if err != nil || !info.IsDir() {
		return keep(ReasonMissing)
	}
	if e.locked && e.lockReason != lockReason {
		return keep(ReasonLocked)
	}
	last := facts.LastActivity
	if at := lockedAt(e); at.After(last) {
		last = at // the run closed (froze its worktree) then
	}
	if last.IsZero() {
		last = info.ModTime()
	}
	if !facts.TaskClosed && o.Now.Sub(last) < o.OlderThan {
		return keep(ReasonRecent)
	}
	status, err := exec.CommandContext(ctx, "git", "-C", e.path, "status", "--porcelain", "--ignored=matching").Output()
	if err != nil {
		return keep("git error: " + err.Error())
	}
	var ignored []string
	for _, line := range strings.Split(strings.TrimSpace(string(status)), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "!! "):
			if p := strings.TrimPrefix(line, "!! "); !ignoredRebuildable(e.path, p) {
				ignored = append(ignored, p)
			}
		case line != "?? .beads/" && line != "?? app/node_modules" && line != "?? app/node_modules/":
			return keep(ReasonDirty)
		}
	}
	if len(ignored) > 0 {
		if len(ignored) > 3 {
			ignored = ignored[:3]
		}
		return keep(ReasonIgnored + ": " + strings.Join(ignored, ", "))
	}
	tip, err := g.run(ctx, "rev-parse", "--verify", "refs/heads/"+e.branch)
	if err != nil {
		return keep("git error: " + err.Error())
	}
	tip = strings.TrimSpace(tip)
	out.Tip = tip
	merged, err := g.reachable(ctx, tip, main)
	if err != nil {
		return keep("git error: " + err.Error())
	}
	if merged {
		out.Remove = true
		out.Reason = "finished, all commits on main or origin"
		return out
	}
	out.Archive = archiveRef(e.branch, tip)
	if !o.Archive {
		out.Reason = ReasonUnique
		return out
	}
	out.Remove = true
	out.Reason = "finished, unique commits archived first"
	return out
}

// archiveRef names the ref that keeps a branch's tip: refs/archive/op/<name>-<short sha>.
func archiveRef(branch, tip string) string {
	short := tip
	if len(short) > 7 {
		short = short[:7]
	}
	return "refs/archive/op/" + strings.TrimPrefix(branch, "op/") + "-" + short
}

// reachable reports whether sha is on main or on any remote-tracking branch.
func (g gitRunner) reachable(ctx context.Context, sha, main string) (bool, error) {
	if _, err := g.run(ctx, "merge-base", "--is-ancestor", sha, main); err == nil {
		return true, nil
	} else if !isExit(err, 1) {
		return false, err
	}
	out, err := g.run(ctx, "for-each-ref", "--contains", sha, "--count=1", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// remove archives, then removes one worktree and its branch. The branch is
// deleted only while it still points at the tip that was decided on.
func (g gitRunner) remove(ctx context.Context, e *GCEntry) error {
	if e.Archive != "" {
		if _, err := g.run(ctx, "update-ref", e.Archive, e.Tip); err != nil {
			return err
		}
		got, err := g.run(ctx, "rev-parse", "--verify", e.Archive)
		if err != nil || strings.TrimSpace(got) != e.Tip {
			return fmt.Errorf("archive ref %s not written", e.Archive)
		}
	}
	if err := Unlock(e.Path); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	if _, err := g.run(ctx, "worktree", "remove", "--force", e.Path); err != nil {
		_ = Lock(e.Path) // leave it as a closed run leaves it
		return err
	}
	// Only now that the work is on main, origin or the archive ref; update-ref
	// with the old value fails if the branch moved since decide.
	if _, err := g.run(ctx, "update-ref", "-d", "refs/heads/"+e.Branch, e.Tip); err != nil {
		return fmt.Errorf("branch %s kept (tip moved?): %w", e.Branch, err)
	}
	return nil
}

type gitRunner struct {
	repo    string
	timeout time.Duration
}

func (g gitRunner) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", g.repo}, args...)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return string(out), &gitRunError{err: ee, msg: strings.TrimSpace(string(ee.Stderr))}
		}
		return string(out), err
	}
	return string(out), nil
}

type gitRunError struct {
	err *exec.ExitError
	msg string
}

func (e *gitRunError) Error() string { return e.err.Error() + ": " + e.msg }
func (e *gitRunError) Unwrap() error { return e.err }

func isExit(err error, code int) bool {
	for err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode() == code
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// checkMainCheckout refuses a dirty or mid-operation main checkout.
func (g gitRunner) checkMainCheckout(ctx context.Context) error {
	if _, err := g.run(ctx, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s is not a git checkout: %w", g.repo, err)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		p, err := g.run(ctx, "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(g.repo, p)
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("main checkout %s is mid-%s: finish or abort it first", g.repo, name)
		}
	}
	status, err := g.run(ctx, "status", "--porcelain")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line != "" && !strings.Contains(line, ".beads/") && !strings.Contains(line, ".commander/") && !isWorktreesStatusLine(line) {
			return fmt.Errorf("main checkout %s is dirty (%s): commit or stash first", g.repo, strings.TrimSpace(line))
		}
	}
	return nil
}

// isWorktreesStatusLine is an untracked .worktrees (a link) or
// .worktrees.noindex line of git status: the Operators' own folder.
func isWorktreesStatusLine(line string) bool {
	path := strings.TrimSuffix(strings.TrimPrefix(line, "?? "), "/")
	return strings.HasPrefix(line, "?? ") && (path == ".worktrees" || path == ".worktrees.noindex")
}

// list returns the linked worktrees (never the main checkout).
func (g gitRunner) list(ctx context.Context) ([]wtEntry, error) {
	out, err := g.run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var all []wtEntry
	var cur *wtEntry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			all = append(all, wtEntry{path: strings.TrimPrefix(line, "worktree ")})
			cur = &all[len(all)-1]
		case cur == nil:
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.locked = true
			cur.lockReason = strings.TrimSpace(strings.TrimPrefix(line, "locked"))
		case strings.HasPrefix(line, "prunable"):
			cur.prunable = true
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	// Real paths, so a worktree recorded under .worktrees or .worktrees.noindex
	// is one entry and never counted twice.
	seen := map[string]bool{}
	var linked []wtEntry
	for _, e := range all[1:] { // the first entry is always the main checkout
		e.path = realPath(e.path)
		if !seen[e.path] {
			seen[e.path] = true
			linked = append(linked, e)
		}
	}
	return linked, nil
}

// lockRepo serializes Apply passes on one repo.
func lockRepo(repo string) (func(), error) {
	dir, err := exec.Command("git", "-C", repo, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return nil, err
	}
	p := strings.TrimSpace(string(dir))
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p)
	}
	f, err := os.OpenFile(filepath.Join(p, "commander-worktree-gc.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another worktrees gc is running on %s", repo)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// WorktreeCount is the number of linked worktrees git tracks for repo, read
// from .git/worktrees without running git over every one of them.
func WorktreeCount(repo string) int {
	dir := filepath.Join(repo, ".git", "worktrees")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(ents)
}

// LoadKeepList reads <project>/.commander/worktree-keep: one shell glob per
// line (** for any depth), '#' starts a comment. See keptByList for what is matched.
// A missing file is an empty list; an unreadable one is an error.
func LoadKeepList(projectDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, ".commander", "worktree-keep"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keep file unreadable (nothing is protected by it): %w", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// validateKeep refuses a pass when a keep pattern does not compile: the
// worktree it was meant to protect would otherwise be unprotected.
func validateKeep(patterns []string) error {
	for _, p := range patterns {
		if _, err := globRegexp(p); err != nil {
			return fmt.Errorf("keep pattern %q is invalid: %w", p, err)
		}
	}
	return nil
}

// keptByList reports whether a keep glob matches the worktree's directory name,
// its path relative to the project root, its branch (with or without op/) or
// its task bead id.
func keptByList(patterns []string, projectDir string, e wtEntry, taskID string) bool {
	if len(patterns) == 0 {
		return false
	}
	values := []string{filepath.Base(e.path), e.branch, strings.TrimPrefix(e.branch, "op/")}
	if taskID != "" {
		values = append(values, taskID)
	}
	if projectDir != "" {
		root, wt := projectDir, e.path
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		if r, err := filepath.EvalSymlinks(wt); err == nil {
			wt = r
		}
		if rel, err := filepath.Rel(root, wt); err == nil {
			rel = filepath.ToSlash(rel)
			values = append(values, rel)
			// A keep glob may name the folder either way: .worktrees/x or .worktrees.noindex/x.
			for _, from := range WorktreeDirNames {
				if tail, ok := strings.CutPrefix(rel, from+"/"); ok {
					for _, to := range WorktreeDirNames {
						if to != from {
							values = append(values, to+"/"+tail)
						}
					}
				}
			}
		}
	}
	for _, p := range patterns {
		re, err := globRegexp(p)
		if err != nil {
			return true // validateKeep refuses first; never leave a worktree unprotected
		}
		for _, v := range values {
			if v != "" && re.MatchString(v) {
				return true
			}
		}
	}
	return false
}

// globRegexp compiles a shell glob: * and ? stay inside one path element, **
// spans any depth, [..] is a class.
func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				break
			}
			class := glob[i : i+j+1]
			if strings.HasPrefix(class, "[!") {
				class = "[^" + class[2:]
			}
			b.WriteString(class)
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ignoredRebuildable reports whether an ignored path (as git status prints it,
// relative to the worktree wt) is a rebuildable cache.
func ignoredRebuildable(wt, p string) bool {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	p = strings.TrimSuffix(p, "/")
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if ignoredAllowed[part] {
			return true
		}
		if ignoredAllowedTop[part] && (i == 0 || hasManifest(filepath.Join(wt, filepath.Join(parts[:i]...)))) {
			return true
		}
	}
	base := filepath.Base(p)
	for _, g := range ignoredAllowedFile {
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
	}
	return false
}

func hasManifest(dir string) bool {
	for _, m := range manifests {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// WorktreeDirNames are the Operators' worktree folders: .worktrees, or its real
// name .worktrees.noindex when .worktrees is a link to it (macOS skips a
// *.noindex folder, and git records a worktree by its real path).
var WorktreeDirNames = []string{".worktrees", ".worktrees.noindex"}

// realPath resolves symlinks; a path that cannot be resolved (missing) stays as is.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// underProjectWorktrees reports whether path is inside <project>/.worktrees or
// <project>/.worktrees.noindex, comparing real paths.
func underProjectWorktrees(projectDir, path string) bool {
	project, path := realPath(projectDir), realPath(path)
	for _, name := range WorktreeDirNames {
		root := realPath(filepath.Join(project, name))
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// lockedAt is when a closed run froze its worktree (the mtime of git's lock
// file), the end of the run; zero when unlocked or unreadable.
func lockedAt(e wtEntry) time.Time {
	if !e.locked {
		return time.Time{}
	}
	data, err := os.ReadFile(filepath.Join(e.path, ".git"))
	if err != nil {
		return time.Time{}
	}
	dir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	info, err := os.Stat(filepath.Join(dir, "locked"))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// keptCategory is a Kept key: the reason up to its first colon, so "ignored
// content: a, b, c" counts as one kind.
func keptCategory(reason string) string {
	cat, _, _ := strings.Cut(reason, ":")
	return cat
}

type dryRunRemoval struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Tip     string `json:"tip"`
	Archive string `json:"archive,omitempty"`
}

// dryRunRecord is what a dry run showed; --apply may remove only that (or
// less), with the same flags.
type dryRunRecord struct {
	Time       time.Time       `json:"time"`
	Repo       string          `json:"repo"`
	Archive    bool            `json:"archive"`
	OlderThan  time.Duration   `json:"older_than"`
	Limit      int             `json:"limit"`
	Worktrees  int             `json:"worktrees"`
	Candidates int             `json:"candidates"`
	Removals   []dryRunRemoval `json:"removals"`
}

// DryRunFiles names what a dry run writes under the project, for the report.
func DryRunFiles(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, ".commander", dryRunFile),
	}
}

func writeDryRun(projectDir string, o GCOptions, rep *GCReport) {
	dir := filepath.Join(projectDir, ".commander")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	rec := dryRunRecord{Time: o.Now, Repo: o.Repo, Archive: o.Archive, OlderThan: o.OlderThan, Limit: o.Limit,
		Worktrees: rep.Before, Candidates: rep.Candidates, Removals: []dryRunRemoval{}}
	for _, e := range rep.Entries {
		if e.Remove {
			rec.Removals = append(rec.Removals, dryRunRemoval{Path: e.Path, Branch: e.Branch, Tip: e.Tip, Archive: e.Archive})
		}
	}
	data, _ := json.Marshal(rec)
	path := filepath.Join(dir, dryRunFile)
	if os.WriteFile(path, data, 0o600) == nil {
		_ = os.Chmod(path, 0o600) // an older 0644 record keeps its mode otherwise
	}
}

func requireFreshDryRun(projectDir string, now time.Time) (*dryRunRecord, error) {
	if now.IsZero() {
		now = time.Now()
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".commander", dryRunFile))
	var rec dryRunRecord
	if err == nil {
		err = json.Unmarshal(data, &rec)
	}
	if err != nil || rec.Time.IsZero() || now.Sub(rec.Time) > DryRunMaxAge || rec.Time.After(now.Add(time.Minute)) {
		return nil, fmt.Errorf("--apply needs a dry run of %s from the last hour: run without --apply first", projectDir)
	}
	return &rec, nil
}

// covers refuses an apply whose removals are not exactly what the reviewed dry
// run listed (same flags, same worktrees at the same tips, nothing extra).
func (r *dryRunRecord) covers(o GCOptions, entries []GCEntry) error {
	if r.Repo != o.Repo || r.Archive != o.Archive || r.OlderThan != o.OlderThan || r.Limit != o.Limit {
		return fmt.Errorf("--apply flags differ from the reviewed dry run (repo, --archive, --older-than, limit): run the dry run again")
	}
	listed := map[string]dryRunRemoval{}
	for _, d := range r.Removals {
		listed[realPath(d.Path)] = d
	}
	for _, e := range entries {
		if !e.Remove {
			continue
		}
		d, ok := listed[realPath(e.Path)]
		if !ok {
			return fmt.Errorf("--apply would remove %s, which the reviewed dry run did not list: run the dry run again", e.Path)
		}
		if d.Tip != e.Tip || d.Branch != e.Branch || d.Archive != e.Archive {
			return fmt.Errorf("%s changed since the reviewed dry run (tip %s -> %s): run the dry run again", e.Path, d.Tip, e.Tip)
		}
	}
	return nil
}
