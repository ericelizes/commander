// Package ship fast-forwards a branch into main of a local project. It never
// installs or restarts anything.
package ship

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Runner runs one command in dir (without $TMUX) and returns its combined output.
type Runner func(ctx context.Context, dir string, name string, args ...string) (string, error)

// Options configure one ship.
type Options struct {
	Root   string // the project's main checkout
	Branch string
	Base   string // defaults to main
	// Run runs the pre-ship checks; nil runs real commands.
	Run Runner
	// Hold returns an error when the branch's task is on hold (or cannot be
	// checked). It runs before anything expensive and again right before the
	// merge, since the verify between them takes minutes; nil checks nothing.
	Hold func(ctx context.Context) error
	// Exclusive waits its turn, up to queueWait(), for the build slot and then
	// the machine-wide lock that lets only one ship verify at a time, and
	// returns the release of both; nil takes neither. Both are held through the
	// merge, so no other ship's landing can undo this verify.
	Exclusive func(ctx context.Context, out io.Writer) (release func(), err error)
	// VerifyBudget bounds one verify; 0 = DefaultVerifyBudget. Waiting for
	// Exclusive is not part of it.
	VerifyBudget time.Duration
	// HoldBudget bounds everything done while holding Exclusive, however often
	// main moves; 0 = DefaultHoldBudget. StepBudget bounds each short step inside
	// it (hold check, catch-up, check slot, checkout, merge); 0 = DefaultStepBudget.
	HoldBudget time.Duration
	StepBudget time.Duration
	// Threads counts the user's threads for the pre-verify wait; nil counts
	// /proc. Sleep replaces time.Sleep in that wait (tests).
	Threads func() int
	Sleep   func(context.Context, time.Duration)
	// MaxThreads is the configured thread limit ([ship] max_threads); nil =
	// default. $COMMANDER_SHIP_MAX_THREADS overrides it.
	MaxThreads *int
	// DryRun does everything a ship does (refusals, queue, slot, lock, hold
	// checks, replays, checks) except the merge, and reports what would land.
	DryRun bool
	// LookPath finds the gitleaks binary for the secret scan; nil uses
	// exec.LookPath (the package's tests pin that default to "not found").
	// Not found = the scan is skipped with a warning.
	LookPath func(string) (string, error)
}

// Run merges opts.Branch into the base branch of opts.Root, fast-forward only,
// and reports the move to out.
func Run(ctx context.Context, opts Options, out io.Writer) error {
	err := runShip(ctx, opts, out)
	// A step cut short by a stop or an expired wait says nothing about the branch.
	if err != nil && ctx.Err() != nil {
		return Inconclusive{err}
	}
	return err
}

// Inconclusive is a ship that ended before it reached a verdict on the branch:
// stopped, or out of time waiting or under the lock. A real ship would not
// refuse the branch for this, so a dry run must not say it would.
type Inconclusive struct{ Err error }

func (e Inconclusive) Error() string { return e.Err.Error() }
func (e Inconclusive) Unwrap() error { return e.Err }

func runShip(ctx context.Context, opts Options, out io.Writer) error {
	base := opts.Base
	if base == "" {
		base = "main"
	}
	git := func(args ...string) (string, error) { return runGit(ctx, opts.Root, args...) }

	if opts.Branch == "" || opts.Branch == base {
		return fmt.Errorf("want a branch other than %s", base)
	}
	if opts.Hold != nil {
		if err := opts.Hold(ctx); err != nil {
			return err
		}
	}
	cur, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if cur != base {
		return fmt.Errorf("main checkout %s is on %q, not %s", opts.Root, cur, base)
	}
	if dirty := dirtyFiles(git); len(dirty) > 0 {
		return fmt.Errorf("main checkout is dirty beyond .beads files: %s", strings.Join(dirty, ", "))
	}
	baseSHA, err := git("rev-parse", base)
	if err != nil {
		return err
	}
	branchSHA, err := git("rev-parse", "--verify", opts.Branch+"^{commit}")
	if err != nil {
		return fmt.Errorf("branch %q not found", opts.Branch)
	}
	if branchSHA == baseSHA {
		return fmt.Errorf("%s is already at %s (%s): nothing to ship", opts.Branch, base, short(baseSHA))
	}
	if _, err := git("merge-base", "--is-ancestor", base, branchSHA); err != nil {
		return fmt.Errorf("%s is not a strict descendant of %s (rebase it onto %s first)", opts.Branch, base, base)
	}
	if err := checkBranchWorktree(ctx, opts.Root, opts.Branch); err != nil {
		return err
	}
	if opts.Exclusive != nil {
		release, err := waitFor(ctx, func(ctx context.Context) (func(), error) { return opts.Exclusive(ctx, out) })
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return Inconclusive{fmt.Errorf("ship lock: %w", err)}
			}
			return fmt.Errorf("ship lock: %w", err)
		}
		defer release()
	}
	return land(ctx, opts, base, baseSHA, branchSHA, out)
}

// land does all the work under the lock, every part of it bounded, so a hang
// ends this ship with a message and frees the lock for the next.
//
// What lands is always what was checked: when main is not where the check
// started the branch's commits are replayed onto it and checked again. Only a
// real conflict stops it. Other ships land before this one has the lock, so it
// catches up first and spends no verify on a tree that cannot land; after that
// only a merge outside ship can move main, and this ship keeps its turn while
// it replays.
func land(ctx context.Context, opts Options, base, baseSHA, branchSHA string, out io.Writer) error {
	holdBudget, stepBudget := opts.HoldBudget, opts.StepBudget
	if holdBudget <= 0 {
		holdBudget = DefaultHoldBudget
	}
	if stepBudget <= 0 {
		stepBudget = DefaultStepBudget
	}
	ctx, cancel := context.WithTimeoutCause(ctx, holdBudget, fmt.Errorf("the %s ship lock budget ran out", holdBudget))
	defer cancel()
	step := func(what string, f func(context.Context) error) error {
		ctx, cancel := context.WithTimeoutCause(ctx, stepBudget, fmt.Errorf("the %s step budget ran out", stepBudget))
		defer cancel()
		err := f(ctx)
		if err != nil && ctx.Err() != nil {
			return Inconclusive{fmt.Errorf("%s did not finish under the ship lock (%v), so the ship gave the lock up and %s is not known red: %w", what, context.Cause(ctx), opts.Branch, err)}
		}
		return err
	}
	checkHold := func() error {
		if opts.Hold == nil {
			return nil
		}
		return step("the hold check", func(ctx context.Context) error {
			// The read is not ours to kill, so a hung one is left behind: the
			// process ends soon after.
			done := make(chan error, 1)
			go func() { done <- opts.Hold(ctx) }()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	shipSHA, onto, replays := branchSHA, baseSHA, 0
	catchUp := func(when string) (moved bool, err error) {
		err = step("the catch-up with "+base, func(ctx context.Context) error {
			now, err := runGit(ctx, opts.Root, "rev-parse", base)
			if err != nil || now == onto {
				return err
			}
			moved = true
			if replays >= maxReplays {
				return fmt.Errorf("%s moved %d times while %s was being checked; ship it again", base, replays+1, opts.Branch)
			}
			replays++
			fmt.Fprintf(out, "ship: %s moved (%s -> %s) %s; replaying %s onto it\n", base, short(onto), short(now), when, opts.Branch)
			replayed, err := replay(ctx, opts.Root, onto, now, shipSHA)
			if err != nil {
				return fmt.Errorf("%s no longer applies on %s: %w (rebase it and ship again)", opts.Branch, base, err)
			}
			shipSHA, onto = replayed, now
			return nil
		})
		return moved, err
	}
	if err := checkHold(); err != nil {
		return err
	}
	if _, err := catchUp("while it waited in the ship queue"); err != nil {
		return err
	}
	replays = 0
	for {
		if err := verifySettled(ctx, opts, base, shipSHA, step, out); err != nil {
			return err
		}
		if err := checkHold(); err != nil {
			return err
		}
		moved, err := catchUp("during the check")
		if err != nil {
			return err
		}
		if !moved {
			break
		}
	}
	if opts.DryRun {
		fmt.Fprintf(out, "dry run: %s passed every check; a real ship would move %s %s -> %s\n", opts.Branch, base, short(onto), short(shipSHA))
		if shipSHA != branchSHA {
			fmt.Fprintf(out, "dry run: that is a replay of %s, so a real ship's commits get new hashes\n", short(branchSHA))
		}
		fmt.Fprintf(out, "dry run: nothing merged, %s is still at %s\n", base, short(onto))
		return nil
	}
	err := step("the ff-only merge (check "+base+" first: it may have landed)", func(ctx context.Context) error {
		_, err := runGit(ctx, opts.Root, "merge", "--ff-only", shipSHA)
		return err
	})
	if err != nil {
		return fmt.Errorf("ff-only merge of %s failed: %w", opts.Branch, err)
	}
	fmt.Fprintf(out, "%s: %s -> %s (%s)\nnot installed: installing needs the user's OK via Commander\n",
		base, short(onto), short(shipSHA), opts.Branch)
	return nil
}

// maxReplays bounds how often a ship replays onto a moved main before giving up.
const maxReplays = 3

// replay cherry-picks oldBase..tip onto newBase in a throwaway worktree and
// returns the new tip. The branch itself is left as it is (it may be checked
// out by its Operator); main receives the replayed commits.
func replay(ctx context.Context, root, oldBase, newBase, tip string) (string, error) {
	dir, err := os.MkdirTemp("", "cmdr-ship-replay-")
	if err != nil {
		return "", err
	}
	_ = os.Remove(dir)
	if _, err := runGit(ctx, root, "worktree", "add", "--detach", dir, newBase); err != nil {
		return "", err
	}
	// Cleanup runs after ctx may have ended, under a bound of its own.
	cleanup := func(dir string, args ...string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = runGit(ctx, dir, args...)
	}
	defer cleanup(root, "worktree", "remove", "--force", dir)
	if _, err := runGit(ctx, dir, "cherry-pick", "--allow-empty", oldBase+".."+tip); err != nil {
		cleanup(dir, "cherry-pick", "--abort")
		return "", err
	}
	return runGit(ctx, dir, "rev-parse", "HEAD")
}

// checkBranchWorktree refuses when a worktree that has the branch checked out
// holds uncommitted or untracked files: they would be missing from main.
func checkBranchWorktree(ctx context.Context, root, branch string) error {
	list, err := runGit(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	var dir string
	for _, line := range strings.Split(list, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			dir = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			git := func(args ...string) (string, error) { return runGit(ctx, dir, args...) }
			if dirty := dirtyFiles(git); len(dirty) > 0 {
				return fmt.Errorf("worktree %s of %s has uncommitted or untracked files: %s", dir, branch, strings.Join(dirty, ", "))
			}
		}
	}
	return nil
}

// waitFor takes a lock under the queue's own bound, separate from any verify's
// budget; the lock it returns outlives that bound.
func waitFor(ctx context.Context, take func(context.Context) (func(), error)) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, queueWait())
	defer cancel()
	return take(ctx)
}

// lockedStep runs one short piece of lock-held work under its own deadline.
type lockedStep func(what string, f func(context.Context) error) error

// verifySettled runs verifyTree once the machine's thread count allows it.
func verifySettled(ctx context.Context, opts Options, base, branchSHA string, step lockedStep, out io.Writer) error {
	count, sleep := opts.Threads, opts.Sleep
	if count == nil {
		count = func() int { return CountThreads("/proc", os.Getuid()) }
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	if err := waitForThreads(ctx, out, count, maxThreads(opts.MaxThreads), threadsWait(), threadsPollEvery, sleep); err != nil {
		return Inconclusive{fmt.Errorf("the wait for threads ended (%v), so %s is not known red: ship it again", context.Cause(ctx), short(branchSHA))}
	}
	return verifyTree(ctx, opts, base, branchSHA, step, out)
}

// verifyTree checks out the branch tip (the exact tree that would become main)
// and requires it to build and pass its tests. The app is checked too when the
// branch changes anything under app/.
//
// Checks run in a small pool of fixed worktrees, one check per worktree at a
// time (a lock each under .git): Go keys its build and test caches on the
// source path, so a fresh temp dir per check recompiled everything. Several
// checks may run at once since a ship replays onto a main that moved during
// its check (Run), so none can land unchecked against the main it lands on.
func verifyTree(ctx context.Context, opts Options, base, branchSHA string, step lockedStep, out io.Writer) error {
	run := opts.Run
	if run == nil {
		run = runCommand
	}
	var unlock func()
	var slot int
	err := step("the wait for a check slot", func(ctx context.Context) (err error) {
		unlock, slot, err = lockVerifySlot(ctx, opts.Root, out)
		return err
	})
	if err != nil {
		return err
	}
	defer unlock()
	budget := opts.VerifyBudget
	if budget <= 0 {
		budget = DefaultVerifyBudget
	}
	tree, err := verifyTreePath(opts.Root, slot)
	if err != nil {
		return err
	}
	var appChanged string
	err = step("the checkout of the verify tree", func(ctx context.Context) (err error) {
		if err := checkoutVerifyTree(ctx, opts.Root, tree, branchSHA); err != nil {
			return err
		}
		appChanged, err = runGit(ctx, opts.Root, "diff", "--name-only", base, branchSHA, "--", "app")
		return err
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeoutCause(ctx, budget, fmt.Errorf("the %s verify budget ran out", budget))
	defer cancel()
	appDir := filepath.Join(tree, "app")
	checks := []struct {
		dir  string
		argv []string
	}{
		{tree, []string{"go", "build", "./..."}},
		// Unit packages run in parallel; the end-to-end evals run on their own
		// after them, serially, because they time out on a loaded host.
		{tree, []string{"sh", "-c", "go test -p 1 $(go list ./... | grep -v /tests/evals)"}},
		{tree, []string{"go", "test", "-p", "1", "./tests/evals/..."}},
	}
	if appChanged != "" {
		// node_modules is untracked: borrow the main checkout's.
		os.Symlink(filepath.Join(opts.Root, "app", "node_modules"), filepath.Join(appDir, "node_modules"))
		checks = append(checks,
			struct {
				dir  string
				argv []string
			}{appDir, []string{"npm", "run", "typecheck"}},
			struct {
				dir  string
				argv []string
			}{appDir, []string{"npm", "test"}})
	}
	if err := scanSecrets(ctx, opts, run, base, branchSHA, out); err != nil {
		return err
	}
	for _, check := range checks {
		fmt.Fprintf(out, "ship check: %s\n", strings.Join(check.argv, " "))
		o, err := run(ctx, check.dir, check.argv[0], check.argv[1:]...)
		if err != nil {
			o, err = retryExhausted(ctx, run, check.dir, check.argv, o, err, out)
		}
		if err != nil && ctx.Err() != nil {
			return Inconclusive{fmt.Errorf("%v during %s, so %s is not known red: ship it again", context.Cause(ctx), strings.Join(check.argv, " "), short(branchSHA))}
		}
		if err != nil {
			return errors.New(describeRed(branchSHA, check.argv, err, o))
		}
	}
	return nil
}

// lookPath finds gitleaks when Options.LookPath is nil (a variable so the
// package's tests can pin it: a gitleaks on the tester's PATH must not change
// what a ship test sees).
var lookPath = exec.LookPath

// defaultGitleaksConfig is tool-owned: upstream v8.21.2 rules with no
// global allowlist or path allowlists. Never inherit a repository's policy.
//
//go:embed gitleaks.toml
var defaultGitleaksConfig string

// Include historical changes and one synthetic commit representing the entire
// base..tip change. No rename or binary heuristics may hide added content.
func gitleaksLogOpts(revs string) string {
	return "--text --no-textconv --no-ext-diff --no-renames --diff-merges=first-parent " + revs
}

// gitleaksLevel matches one of gitleaks' own log lines ("12:57AM ERR ...",
// colours stripped), never a line of scanned content.
var (
	gitleaksLevel = regexp.MustCompile(`^\d{1,2}:\d{2}[AP]M (TRC|DBG|INF|WRN|ERR|FTL) (.*)$`)
	ansiEscape    = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	gitleaksFound = regexp.MustCompile(`^(leaks found: [1-9]|[1-9][0-9]* leaks found)`)
)

// readGitleaksLog reads gitleaks' own log lines: leaks is whether it
// reported findings, incomplete why it did not scan the whole range ("" when
// it did). gitleaks v8.21.2 logs "failed to scan Git repository" when its git
// child writes anything to stderr (an ambiguous ref, an option an older git
// does not know), then still prints "scan completed" and "no leaks found" and
// exits 0; so any ERR or FTL line, or a "partial scan", means the scan did
// not complete, and so does a run that never says "scan completed".
func readGitleaksLog(output string) (leaks bool, incomplete string) {
	completed := false
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(output, ""), "\n") {
		m := gitleaksLevel.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if m[1] == "WRN" && gitleaksFound.MatchString(m[2]) {
			leaks = true
		}
		switch {
		case incomplete != "":
		case m[1] == "ERR" || m[1] == "FTL" || strings.Contains(m[2], "partial scan"):
			incomplete = m[1] + " " + m[2]
		case strings.HasPrefix(m[2], "scan completed"):
			completed = true
		}
	}
	if incomplete == "" && !completed {
		incomplete = `no "scan completed" line`
	}
	return leaks, incomplete
}

// scanSecrets runs gitleaks over the commits the branch adds to base and
// refuses on any finding. Without gitleaks it warns and goes on (the GitHub
// Action still scans every push).
//
// Policy is embedded in the tool. The source directory is private and empty
// except for a gitdir pointer: repository ignore files cannot suppress findings.
func scanSecrets(ctx context.Context, opts Options, run Runner, base, branchSHA string, out io.Writer) error {
	look := opts.LookPath
	if look == nil {
		look = lookPath
	}
	if _, err := look("gitleaks"); err != nil {
		fmt.Fprintln(out, "warning: gitleaks not found on PATH, secret scan skipped (install gitleaks to scan before shipping)")
		return nil
	}
	// The range is built from commits, never from the base's name: a tag (or
	// any ref) named like the base makes the name ambiguous, and git's
	// warning about it fails the scan (or, read as the tag, scans the wrong
	// range).
	baseRef := base
	if !strings.HasPrefix(baseRef, "refs/") {
		baseRef = "refs/heads/" + base
	}
	baseSHA, err := runGit(ctx, opts.Root, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("secret scan: cannot resolve %s: %v", baseRef, err)
	}
	cfgDir, err := os.MkdirTemp("", "cmdr-ship-gitleaks-")
	if err != nil {
		return fmt.Errorf("secret scan: %v", err)
	}
	defer os.RemoveAll(cfgDir)
	config := filepath.Join(cfgDir, "gitleaks.toml")
	ignore := filepath.Join(cfgDir, ".gitleaksignore")
	for path, body := range map[string]string{config: defaultGitleaksConfig, ignore: ""} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("secret scan: %v", err)
		}
	}
	gitDir, err := runGit(ctx, opts.Root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("secret scan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
		return fmt.Errorf("secret scan: %v", err)
	}
	// No ref or checkout changes. Scan base-to-each-introduced-tree patches:
	// a multi-line secret can be assembled, then deleted before the final tip.
	// rev-list includes side branches introduced by merges, not just first parents.
	commits, err := runGit(ctx, opts.Root, "rev-list", baseSHA+".."+branchSHA)
	if err != nil {
		return fmt.Errorf("secret scan: cannot enumerate intermediate commits: %v", err)
	}
	revs := baseSHA + ".." + branchSHA
	// Retain the explicit base-to-tip check even when the range is empty.
	for _, commit := range append([]string{branchSHA}, strings.Fields(commits)...) {
		net, err := runGit(ctx, opts.Root, "-c", "user.name=Commander scan", "-c", "user.email=scan@example.invalid",
			"commit-tree", commit+"^{tree}", "-p", baseSHA, "-m", "Cumulative secret scan")
		if err != nil {
			return fmt.Errorf("secret scan: cannot construct cumulative diff: %v", err)
		}
		revs += " " + net + "^.." + net
	}
	// --ignore-gitleaks-allow: an inline "gitleaks:allow" comment is the
	// branch's own word about its own line, so it silences nothing.
	argv := []string{"gitleaks", "detect", "--source", cfgDir, "--log-opts", gitleaksLogOpts(revs),
		"--config", config, "--gitleaks-ignore-path", ignore, "--ignore-gitleaks-allow", "--log-level", "info",
		"--redact", "--no-banner", "--exit-code", "1"}
	fmt.Fprintf(out, "ship check: gitleaks detect (history and cumulative diffs for all introduced commits %s..%s; tool-owned rules; inline allows ignored)\n", short(baseSHA), short(branchSHA))
	o, err := run(ctx, opts.Root, argv[0], argv[1:]...)
	if err != nil && ctx.Err() != nil {
		return Inconclusive{fmt.Errorf("%v during gitleaks, so %s is not known red: ship it again", context.Cause(ctx), short(branchSHA))}
	}
	leaks, incomplete := readGitleaksLog(o)
	if !leaks && incomplete != "" {
		return fmt.Errorf("secret scan of %s did not complete (%s): refusing to ship (gitleaks exit: %v)\n%s", short(branchSHA), incomplete, err, tail(o))
	}
	if err != nil && !leaks {
		return fmt.Errorf("secret scan of %s failed: refusing to ship (gitleaks: %v)\n%s", short(branchSHA), err, tail(o))
	}
	if err != nil || leaks {
		return fmt.Errorf("%s has secret findings: refusing to ship (gitleaks: %v)\n%s\nRemove the secret from the branch history (rewrite the commits) and rotate it; false positives require a reviewed change to the tool-owned rules", short(branchSHA), err, tail(o))
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// retryExhausted reruns, alone and once, each package of a red `go test` step
// that died of resource exhaustion. When nothing else failed and every rerun
// passes, the step is green; otherwise the original error stands (with the
// rerun's output appended for a package that is still red).
func retryExhausted(ctx context.Context, run Runner, dir string, argv []string, o string, err error, out io.Writer) (string, error) {
	if len(argv) < 2 || !(argv[0] == "go" || argv[0] == "sh") || !strings.Contains(strings.Join(argv, " "), "go test") {
		return o, err
	}
	pkgs, other := exhaustedPackages(o)
	if len(pkgs) == 0 || other {
		return o, err
	}
	// The FAIL lines (all exhausted) fully explain the non-zero exit; keep the
	// first run's output so a retried package stays visible in the report.
	fmt.Fprintf(out, "ship check: first run output (kept):\n%s\n", strings.TrimSpace(o))
	for _, pkg := range pkgs {
		fmt.Fprintf(out, "ship check: %s died of resource exhaustion; retrying it alone once\n", pkg)
		if ro, rerr := run(ctx, dir, "go", "test", "-p", "1", pkg); rerr != nil {
			return o + "\n--- retry of " + pkg + " alone:\n" + ro, err
		}
		fmt.Fprintf(out, "ship check: retried package %s: passed alone\n", pkg)
	}
	return "", nil
}

func runCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	goflags := "-trimpath" // the same cache key as Operators' worktrees (see spawn)
	for _, e := range os.Environ() {
		switch {
		case strings.HasPrefix(e, "TMUX="):
		case strings.HasPrefix(e, "COMMANDER_ROLE="): // the shipper's role is not the checks' identity
		case strings.HasPrefix(e, "GOFLAGS="):
			if v := strings.TrimPrefix(e, "GOFLAGS="); !strings.Contains(v, "-trimpath") {
				goflags += " " + v
			} else {
				goflags = v
			}
		default:
			cmd.Env = append(cmd.Env, e)
		}
	}
	// Real tmux scenarios share a host with other slot holders. Give their
	// bounded deadlines room for scheduling delays, without adding sleeps.
	if name == "go" && len(args) > 0 && args[0] == "test" && os.Getenv("COMMANDER_EVAL_TIMEOUT_SCALE") == "" {
		cmd.Env = append(cmd.Env, "COMMANDER_EVAL_TIMEOUT_SCALE=3")
	}
	// A ship must not pass because the app's layout check found no browser.
	if name == "npm" {
		cmd.Env = append(cmd.Env, "COMMANDER_LAYOUT_CHECK_REQUIRED=1")
	}
	if os.Getenv("GOMAXPROCS") == "" {
		cmd.Env = append(cmd.Env, "GOMAXPROCS="+maxProcs)
	}
	cmd.Env = append(cmd.Env, "GOFLAGS="+goflags)
	var o bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &o
	err := runBounded(cmd)
	return o.String(), err
}

// pipeGrace is how long a finished or killed command's output pipes may stay
// open before they are closed on whatever still holds them.
var pipeGrace = 10 * time.Second

// runBounded runs cmd in a process group of its own and ends the whole group
// with it. exec.CommandContext alone kills only the direct child, and then waits
// for every descendant holding the output pipe, so a surviving `sleep` under
// `sh -c` outlived any deadline. A descendant that left the group (setsid) keeps
// running, but cannot hold this up past pipeGrace.
func runBounded(cmd *exec.Cmd) error {
	killGroup := func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := killGroup(); err != syscall.ESRCH {
			return err
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = pipeGrace
	err := cmd.Run()
	if cmd.Process != nil {
		_ = killGroup()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil // the command itself exited 0; only a leftover held the pipe
	}
	return err
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return strings.Join(lines, "\n")
}

// dirtyFiles lists changed paths other than .beads files, the
// .beads-br-<timestamp> backups br leaves beside the checkout and the
// .worktrees / .worktrees.noindex entries (a directory or a symlink to one).
func dirtyFiles(git func(...string) (string, error)) []string {
	out, err := git("status", "--porcelain")
	if err != nil {
		return []string{"git status: " + err.Error()}
	}
	var dirty []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.Trim(line[3:], `"`)
		if path == ".beads" || strings.HasPrefix(path, ".beads/") || strings.HasPrefix(path, ".beads-br-") ||
			path == ".worktrees" || strings.HasPrefix(path, ".worktrees/") ||
			path == ".worktrees.noindex" || strings.HasPrefix(path, ".worktrees.noindex/") {
			continue
		}
		dirty = append(dirty, path)
	}
	return dirty
}

// verifyTreePath is the reused verify worktree for root: outside the repo (no
// status noise in any clone) and outside .git (Vite cannot resolve files under
// a .git directory, so the app checks failed there), one per repo.
func verifyTreePath(root string, slot int) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	name := fmt.Sprintf("%s-%x", filepath.Base(root), sum[:4])
	if slot > 0 {
		name += fmt.Sprintf("-%d", slot) // slot 0 keeps the original tree and its warm caches
	}
	return filepath.Join(cache, "commander", "ship-verify", name), nil
}

// ChecksEnv overrides how many ship checks of one repo may run at once.
const ChecksEnv = "COMMANDER_SHIP_CHECKS"

const defaultChecks = 3

func shipChecks() int {
	if n, err := strconv.Atoi(os.Getenv(ChecksEnv)); err == nil && n > 0 {
		return n
	}
	return defaultChecks
}

// lockVerifySlot takes a free check slot of root (waiting while all are busy)
// and returns its release and number.
func lockVerifySlot(ctx context.Context, root string, out io.Writer) (func(), int, error) {
	n := shipChecks()
	files := make([]*os.File, n)
	for i := range files {
		name := "ship-verify.lock"
		if i > 0 {
			name = fmt.Sprintf("ship-verify-%d.lock", i)
		}
		f, err := os.OpenFile(filepath.Join(root, ".git", name), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			for _, g := range files[:i] {
				g.Close()
			}
			return nil, 0, err
		}
		files[i] = f
	}
	release := func(keep int) {
		for i, f := range files {
			if i != keep {
				f.Close()
			}
		}
	}
	for waited := false; ; waited = true {
		for i, f := range files {
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				release(i)
				return func() { files[i].Close() }, i, nil
			}
		}
		if !waited {
			fmt.Fprintf(out, "ship check: all %d check slots busy; waiting for one\n", n)
		}
		select {
		case <-ctx.Done():
			release(-1)
			return nil, 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// lockVerify takes the machine-wide ship-check lock, waiting for a running check.
func lockVerify(path string, out io.Writer) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(out, "ship check: waiting for another ship check to finish")
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, err
		}
	}
	return func() { f.Close() }, nil
}

// checkoutVerifyTree points the reused worktree at sha with nothing left over
// from the last check; a missing or broken worktree is made afresh.
func checkoutVerifyTree(ctx context.Context, root, tree, sha string) error {
	if _, err := os.Stat(tree); err == nil {
		if _, err := runGit(ctx, tree, "checkout", "--detach", "--force", sha); err == nil {
			if _, err := runGit(ctx, tree, "clean", "-ffdx"); err == nil {
				return nil
			}
		}
		runGit(ctx, root, "worktree", "remove", "--force", tree)
		os.RemoveAll(tree)
	}
	runGit(ctx, root, "worktree", "prune")
	_, err := runGit(ctx, root, "worktree", "add", "--detach", tree, sha)
	return err
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runBounded(cmd); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func short(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}
