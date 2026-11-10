package ship

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Hermetic: a real gitleaks on PATH would add a call to every check list.
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-m", file)
}

// newRepo is a repo on main with one commit and a branch feat one commit ahead.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	addWorkflowPolicy(t, dir)
	commit(t, dir, "a")
	git(t, dir, "branch", "feat")
	git(t, dir, "checkout", "feat")
	commit(t, dir, "b")
	git(t, dir, "checkout", "main")
	return dir
}

func TestShipFastForwards(t *testing.T) {
	dir := newRepo(t)
	var out bytes.Buffer
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: okRun}, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if git(t, dir, "rev-parse", "main") != git(t, dir, "rev-parse", "feat") {
		t.Error("main did not move to feat")
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Errorf("report should say nothing was installed:\n%s", out.String())
	}
}

func TestShipIgnoresBeadsDirt(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: okRun}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestShipIgnoresBrBackupDirs(t *testing.T) {
	dir := newRepo(t)
	backup := filepath.Join(dir, ".beads-br-20261026-150157")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "beads.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup+".gitignore", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: okRun}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestShipIgnoresWorktreesDirAndSymlink(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".worktrees.noindex", "op-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".worktrees.noindex", "op-x", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: okRun}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestShipRefusals(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		branch string
		want   string
	}{
		{"dirty", func(t *testing.T, dir string) { _ = os.WriteFile(filepath.Join(dir, "a"), []byte("changed"), 0o644) }, "feat", "dirty"},
		{"untracked", func(t *testing.T, dir string) { _ = os.WriteFile(filepath.Join(dir, "new"), nil, 0o644) }, "feat", "dirty"},
		{"not on main", func(t *testing.T, dir string) { git(t, dir, "checkout", "feat") }, "feat", "not main"},
		{"diverged", func(t *testing.T, dir string) { commit(t, dir, "c") }, "feat", "strict descendant"},
		{"already merged", func(t *testing.T, dir string) { git(t, dir, "branch", "old") }, "old", "nothing to ship"},
		{"missing branch", func(t *testing.T, dir string) {}, "nope", "not found"},
		{"main itself", func(t *testing.T, dir string) {}, "main", "other than main"},
	}
	for _, tc := range tests {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s dry=%v", tc.name, dry), func(t *testing.T) {
				dir := newRepo(t)
				tc.setup(t, dir)
				head := git(t, dir, "rev-parse", "main")
				err := runFakeShip(context.Background(), Options{Root: dir, Branch: tc.branch, Run: okRun, DryRun: dry}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want containing %q", err, tc.want)
				}
				if git(t, dir, "rev-parse", "main") != head {
					t.Error("main moved on a refused ship")
				}
			})
		}
	}
}

// A dry run is a whole ship up to the merge: the same turn in the queue, the
// same hold checks and the same checks, on the branch's exact tip.
func TestDryRunChecksEverythingAndMovesNothing(t *testing.T) {
	dir := newRepo(t)
	main, tip := git(t, dir, "rev-parse", "main"), git(t, dir, "rev-parse", "feat")
	var exclusive, released, holds int
	var checked []string
	run := func(_ context.Context, d, name string, args ...string) (string, error) {
		checked = append(checked, strings.Join(append([]string{name}, args...), " "))
		if got := gitOut(d, "rev-parse", "HEAD"); got != tip {
			t.Errorf("checked %s, want the branch tip %s", got, tip)
		}
		return "", nil
	}
	var out bytes.Buffer
	err := runFakeShip(context.Background(), Options{
		Root: dir, Branch: "feat", Run: run, DryRun: true,
		Exclusive: func(context.Context, io.Writer) (func(), error) { exclusive++; return func() { released++ }, nil },
		Hold:      func(context.Context) error { holds++; return nil },
	}, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if git(t, dir, "rev-parse", "main") != main {
		t.Error("main moved on a dry run")
	}
	if git(t, dir, "rev-parse", "feat") != tip {
		t.Error("the branch moved on a dry run")
	}
	if exclusive != 1 || released != 1 {
		t.Errorf("queue, slot and lock taken %d times, released %d", exclusive, released)
	}
	if holds != 3 { // before anything, under the lock, right before the merge
		t.Errorf("hold checked %d times, want 3 as on a real ship", holds)
	}
	var realChecks []string
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: func(_ context.Context, _, name string, args ...string) (string, error) {
		realChecks = append(realChecks, strings.Join(append([]string{name}, args...), " "))
		return "", nil
	}}, &bytes.Buffer{}); err != nil {
		t.Fatalf("real ship: %v", err)
	}
	if strings.Join(checked, "\n") != strings.Join(realChecks, "\n") || len(checked) == 0 {
		t.Errorf("dry run checks:\n%v\nreal ship checks:\n%v", checked, realChecks)
	}
	report := out.String()
	for _, want := range []string{"dry run", "would move main", short(main) + " -> " + short(tip), "nothing merged"} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
}

func TestDryRunRefusesARedBranch(t *testing.T) {
	dir := newRepo(t)
	main := git(t, dir, "rev-parse", "main")
	red := func(_ context.Context, _, name string, args ...string) (string, error) {
		if name == "go" && args[0] == "build" {
			return "x.go:1: undefined: y", errors.New("exit status 1")
		}
		return "", nil
	}
	var out bytes.Buffer
	err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: red, DryRun: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "is red") || !strings.Contains(err.Error(), "undefined: y") {
		t.Fatalf("err = %v, want the red check", err)
	}
	if git(t, dir, "rev-parse", "main") != main {
		t.Error("main moved")
	}
	if strings.Contains(out.String(), "would move") {
		t.Errorf("a refused dry run reports a move:\n%s", out.String())
	}
}

func TestDryRunHoldRefuses(t *testing.T) {
	dir := newRepo(t)
	calls := 0
	held := func(context.Context) error {
		calls++
		if calls == 3 {
			return errors.New("held by manager-x")
		}
		return nil
	}
	err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: okRun, DryRun: true, Hold: held}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "held by manager-x") {
		t.Fatalf("err = %v, want the hold set during the verify", err)
	}
}

// Main moved while the dry run waited: it replays and checks what a real ship
// would land, and neither main nor the branch takes the replayed commits.
func TestDryRunReplayMovesNoRef(t *testing.T) {
	dir := newRepo(t)
	tip := git(t, dir, "rev-parse", "feat")
	var moved string
	var out bytes.Buffer
	err := runFakeShip(context.Background(), Options{
		Root: dir, Branch: "feat", Run: okRun, DryRun: true,
		Exclusive: func(context.Context, io.Writer) (func(), error) {
			commit(t, dir, "landed-meanwhile")
			moved = git(t, dir, "rev-parse", "main")
			return func() {}, nil
		},
	}, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if git(t, dir, "rev-parse", "main") != moved {
		t.Error("main moved past the commit that landed meanwhile")
	}
	if git(t, dir, "rev-parse", "feat") != tip {
		t.Error("the branch moved")
	}
	if report := out.String(); !strings.Contains(report, "replaying feat") || !strings.Contains(report, "new hashes") {
		t.Errorf("report should say the checked tree was a replay:\n%s", report)
	}
}

func okRun(context.Context, string, string, ...string) (string, error) { return "", nil }

// gitOut is git for a callback off the test's goroutine path: "" on failure.
func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestShipEvalEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$COMMANDER_EVAL_TIMEOUT_SCALE\" \"${TMUX-unset}\" \"$*\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "do-not-use")
	for _, tc := range []struct{ scale, want string }{{"", "3"}, {"2", "2"}} {
		t.Run("scale="+tc.scale, func(t *testing.T) {
			t.Setenv("COMMANDER_EVAL_TIMEOUT_SCALE", tc.scale)
			out, err := runCommand(context.Background(), dir, "go", "test", "-p", "1", "./...")
			if err != nil || out != tc.want+"|unset|test -p 1 ./..." {
				t.Fatalf("ship environment: %q, %v", out, err)
			}
		})
	}
}

func TestShipRequiresTheLayoutCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte("#!/bin/sh\nprintf '%s' \"$COMMANDER_LAYOUT_CHECK_REQUIRED\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMMANDER_LAYOUT_CHECK_REQUIRED", "")
	if out, err := runCommand(context.Background(), dir, "npm", "test"); err != nil || out != "1" {
		t.Fatalf("npm test environment: %q, %v", out, err)
	}
}

type call struct{ dir, cmd string }

func recorder(failOn string, calls *[]call) Runner {
	return func(_ context.Context, dir, name string, args ...string) (string, error) {
		cmd := strings.Join(append([]string{name}, args...), " ")
		*calls = append(*calls, call{dir, cmd})
		if cmd == failOn {
			return "boom output", os.ErrInvalid
		}
		return "", nil
	}
}

// newAppRepo is newRepo with the feature commit touching app/.
func newAppRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	addWorkflowPolicy(t, dir)
	commit(t, dir, "a")
	git(t, dir, "checkout", "-b", "feat")
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "app/x.ts")
	git(t, dir, "checkout", "main")
	return dir
}

func TestShipRunsChecksInTheReusedVerifyTree(t *testing.T) {
	dir := newRepo(t)
	var calls []call
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder("", &calls)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(calls) != 3 || calls[0].cmd != "go build ./..." || calls[1].cmd != "sh -c go test -p 1 $(go list ./... | grep -v /tests/evals)" || calls[2].cmd != "go test -p 1 ./tests/evals/..." {
		t.Fatalf("calls = %+v, want go build + go test only (app untouched)", calls)
	}
	want, err := verifyTreePath(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(want)
	if strings.Contains(want, string(filepath.Separator)+".git"+string(filepath.Separator)) || strings.HasPrefix(want, dir) {
		t.Errorf("verify tree %s is inside the repo or a .git dir", want)
	}
	if calls[0].dir != want {
		t.Errorf("checks ran in %s, want the reused tree %s (one path keeps Go's caches warm)", calls[0].dir, want)
	}
	if out := git(t, dir, "status", "--porcelain"); out != "" {
		t.Errorf("the verify tree shows in the main checkout: %q", out)
	}
	// A second ship reuses the same tree at the new tip.
	git(t, dir, "checkout", "-q", "feat")
	os.WriteFile(filepath.Join(dir, "more.txt"), []byte("x"), 0o600)
	git(t, dir, "add", "more.txt")
	git(t, dir, "commit", "-qm", "more")
	git(t, dir, "checkout", "-q", "main")
	calls = nil
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder("", &calls)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(calls) == 0 || calls[0].dir != want {
		t.Fatalf("second ship: calls = %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(want, "more.txt")); err != nil {
		t.Errorf("verify tree not moved to the new tip: %v", err)
	}
}

func TestShipRefusesRedBranch(t *testing.T) {
	for _, failOn := range []string{"go build ./...", "sh -c go test -p 1 $(go list ./... | grep -v /tests/evals)", "go test -p 1 ./tests/evals/..."} {
		dir := newRepo(t)
		head := git(t, dir, "rev-parse", "main")
		var calls []call
		err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder(failOn, &calls)}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "is red: "+failOn) || !strings.Contains(err.Error(), "boom output") {
			t.Fatalf("%s: err = %v", failOn, err)
		}
		if git(t, dir, "rev-parse", "main") != head {
			t.Errorf("%s: main moved on a red ship", failOn)
		}
	}
}

func TestShipChecksAppWhenItChanged(t *testing.T) {
	dir := newAppRepo(t)
	var calls []call
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder("", &calls)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	for _, c := range calls {
		got = append(got, c.cmd)
	}
	want := "go build ./...|sh -c go test -p 1 $(go list ./... | grep -v /tests/evals)|go test -p 1 ./tests/evals/...|npm run typecheck|npm test"
	if strings.Join(got, "|") != want {
		t.Fatalf("calls = %v, want %s", got, want)
	}
	if !strings.HasSuffix(calls[3].dir, "app") {
		t.Errorf("npm ran in %s, want the tree's app/", calls[3].dir)
	}
}

func TestShipRefusesRedAppBranch(t *testing.T) {
	for _, failOn := range []string{"npm run typecheck", "npm test"} {
		dir := newAppRepo(t)
		head := git(t, dir, "rev-parse", "main")
		var calls []call
		err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder(failOn, &calls)}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "is red: "+failOn) {
			t.Fatalf("%s: err = %v", failOn, err)
		}
		if git(t, dir, "rev-parse", "main") != head {
			t.Errorf("%s: main moved on a red ship", failOn)
		}
	}
}

func TestShipRefusesDirtyBranchWorktree(t *testing.T) {
	for name, setup := range map[string]func(wt string){
		"modified":  func(wt string) { _ = os.WriteFile(filepath.Join(wt, "b"), []byte("changed"), 0o644) },
		"untracked": func(wt string) { _ = os.WriteFile(filepath.Join(wt, "new.go"), nil, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			wt := filepath.Join(t.TempDir(), "wt")
			git(t, dir, "worktree", "add", wt, "feat")
			setup(wt)
			head := git(t, dir, "rev-parse", "main")
			var calls []call
			err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: recorder("", &calls)}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "uncommitted or untracked") {
				t.Fatalf("err = %v", err)
			}
			if len(calls) != 0 || git(t, dir, "rev-parse", "main") != head {
				t.Error("checks ran or main moved despite a dirty branch worktree")
			}
		})
	}
}

func TestShipHoldsSlotForWholeVerify(t *testing.T) {
	dir := newRepo(t)
	held := false
	var steps []bool
	run := func(ctx context.Context, d, name string, args ...string) (string, error) {
		steps = append(steps, held)
		return "", nil
	}
	slot := func(ctx context.Context, out io.Writer) (func(), error) {
		held = true
		return func() { held = false }, nil
	}
	exclusive := func(ctx context.Context, out io.Writer) (func(), error) {
		return holdVerifyLock(ctx, t.TempDir(), dir, out, testPoll, slot)
	}
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: run, Exclusive: exclusive}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("no checks ran")
	}
	for i, h := range steps {
		if !h {
			t.Errorf("check %d ran outside the slot", i)
		}
	}
	if held {
		t.Error("slot not released after ship")
	}
}

func TestShipSlotFailureStopsBeforeChecks(t *testing.T) {
	dir := newRepo(t)
	ran := false
	run := func(ctx context.Context, d, name string, args ...string) (string, error) { ran = true; return "", nil }
	slot := func(ctx context.Context, out io.Writer) (func(), error) { return nil, errors.New("queue timeout") }
	exclusive := func(ctx context.Context, out io.Writer) (func(), error) {
		return holdVerifyLock(ctx, t.TempDir(), dir, out, testPoll, slot)
	}
	err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: run, Exclusive: exclusive}, &bytes.Buffer{})
	if err == nil || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	if git(t, dir, "rev-parse", "main") == git(t, dir, "rev-parse", "feat") {
		t.Error("main moved without verify")
	}
}

func TestShipHoldRefusedAtStart(t *testing.T) {
	dir := newRepo(t)
	var calls []call
	opts := Options{Root: dir, Branch: "feat", Run: recorder("", &calls),
		Hold: func(context.Context) error { return errors.New("held by manager-x: wait") }}
	err := runFakeShip(context.Background(), opts, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "held by manager-x") {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("verify ran despite the hold: %v", calls)
	}
}

func TestShipHoldSetDuringVerifyRefusesFinalMerge(t *testing.T) {
	dir := newRepo(t)
	held := false
	opts := Options{Root: dir, Branch: "feat",
		Run: func(context.Context, string, string, ...string) (string, error) { held = true; return "", nil },
		Hold: func(context.Context) error {
			if held {
				return errors.New("held by manager-x: stop")
			}
			return nil
		}}
	err := runFakeShip(context.Background(), opts, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "held by") {
		t.Fatalf("err = %v", err)
	}
	if git(t, dir, "rev-parse", "main") == git(t, dir, "rev-parse", "feat") {
		t.Error("main moved despite the hold")
	}
}

func TestShipHoldUnheldShips(t *testing.T) {
	dir := newRepo(t)
	opts := Options{Root: dir, Branch: "feat", Run: okRun, Hold: func(context.Context) error { return nil }}
	if err := runFakeShip(context.Background(), opts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if git(t, dir, "rev-parse", "main") != git(t, dir, "rev-parse", "feat") {
		t.Error("main did not move")
	}
}

func TestShipReplaysOntoAMainThatMovedDuringTheCheck(t *testing.T) {
	dir := newRepo(t)
	moved := false
	var checks int
	run := func(ctx context.Context, d, name string, args ...string) (string, error) {
		if name == "go" && len(args) > 0 && args[0] == "build" {
			checks++
			if !moved {
				// Another ship lands on main while this one is being checked.
				moved = true
				commit(t, dir, "c")
			}
		}
		return "", nil
	}
	var out bytes.Buffer
	if err := runFakeShip(context.Background(), Options{Root: dir, Branch: "feat", Run: run}, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if checks != 2 {
		t.Fatalf("checks = %d, want 2 (the replay is checked again)\n%s", checks, out.String())
	}
	for _, f := range []string{"b", "c"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("main lacks %s after the ship: %v", f, err)
		}
	}
	if !strings.Contains(out.String(), "replaying feat onto it") {
		t.Errorf("output does not say it replayed:\n%s", out.String())
	}
}

func TestChecksTakeSeparateSlots(t *testing.T) {
	dir := newRepo(t)
	t.Setenv(ChecksEnv, "2")
	var out bytes.Buffer
	r0, s0, err := lockVerifySlot(context.Background(), dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	r1, s1, err := lockVerifySlot(context.Background(), dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if s0 == s1 {
		t.Fatalf("two concurrent checks share slot %d", s0)
	}
	got := make(chan int, 1)
	go func() {
		r, s, _ := lockVerifySlot(context.Background(), dir, &out)
		got <- s
		r()
	}()
	select {
	case s := <-got:
		t.Fatalf("a third check took slot %d while both were busy", s)
	case <-time.After(300 * time.Millisecond):
	}
	r0()
	select {
	case s := <-got:
		if s != s0 {
			t.Errorf("waiting check took slot %d, want the freed %d", s, s0)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting check never got the freed slot")
	}
	r1()
}

// The checks run as themselves: an Operator shipping must not hand its role to
// `go test`, or every test judges its calls as that agent.
func TestRunCommandDropsTheShippersRole(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-x")
	out, err := runCommand(context.Background(), t.TempDir(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "COMMANDER_ROLE=") {
		t.Fatalf("the check inherited the role:\n%s", out)
	}
}

func TestShipRefusesOnGitleaksFindings(t *testing.T) {
	dir := newRepo(t)
	var calls []call
	gl := func(string) (string, error) { return "/bin/gitleaks", nil }
	var out bytes.Buffer
	clean := func(ctx context.Context, dir, name string, args ...string) (string, error) {
		recorder("", &calls)(ctx, dir, name, args...)
		if name == "gitleaks" {
			return gitleaksClean, nil
		}
		return "", nil
	}
	mainSHA := git(t, dir, "rev-parse", "main")
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: gl, Run: clean}, &out)
	if err != nil || len(calls) != 4 || (!strings.HasPrefix(calls[0].cmd, "gitleaks detect --source ") || !strings.Contains(calls[0].cmd, "--no-renames --diff-merges=first-parent "+mainSHA+"..")) || calls[0].dir != dir {
		t.Fatalf("clean scan: err=%v calls=%+v", err, calls)
	}
	if !strings.Contains(calls[0].cmd, " --ignore-gitleaks-allow ") {
		t.Fatalf("inline gitleaks:allow comments are honoured: %s", calls[0].cmd)
	}
	failing := func(_ context.Context, _ string, name string, _ ...string) (string, error) {
		if name == "gitleaks" {
			return "Finding: aws-key\n" + gitleaksLeak, os.ErrInvalid
		}
		t.Fatalf("ran %s after a red scan", name)
		return "", nil
	}
	err = Run(context.Background(), Options{Root: newRepo(t), Branch: "feat", LookPath: gl, Run: failing}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "secret findings") || !strings.Contains(err.Error(), "aws-key") {
		t.Fatalf("want refusal, got %v", err)
	}
}

// Neither main nor the branch supplies rules or ignores; the tool owns policy
// and the detector uses a private source directory.
func TestShipGitleaksUsesToolConfigNeverRepositoryPolicy(t *testing.T) {
	gl := func(string) (string, error) { return "/bin/gitleaks", nil }
	scan := func(t *testing.T, dir string) (config, ignore, source string) {
		t.Helper()
		run := func(_ context.Context, rdir, name string, args ...string) (string, error) {
			if name != "gitleaks" {
				return "", nil
			}
			flag := func(f string) string {
				for i, a := range args {
					if a == f && i+1 < len(args) {
						return args[i+1]
					}
				}
				t.Fatalf("gitleaks without %s: %v", f, args)
				return ""
			}
			c, err := os.ReadFile(flag("--config"))
			if err != nil {
				t.Fatal(err)
			}
			i, err := os.ReadFile(flag("--gitleaks-ignore-path"))
			if err != nil {
				t.Fatal(err)
			}
			if rdir == flag("--source") {
				t.Errorf("gitleaks must use a private source, ran in %s, source %s", rdir, flag("--source"))
			}
			config, ignore, source = string(c), string(i), flag("--source")
			return gitleaksClean, nil
		}
		if err := Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: gl, Run: run}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	branchAllowsItself := func(t *testing.T, dir string) {
		git(t, dir, "checkout", "-q", "feat")
		os.WriteFile(filepath.Join(dir, ".gitleaksignore"), []byte("branch-fingerprint\n"), 0o644)
		os.WriteFile(filepath.Join(dir, ".gitleaks.toml"), []byte("[allowlist]\npaths = [\".*\"]\n"), 0o644)
		git(t, dir, "add", ".gitleaksignore", ".gitleaks.toml")
		git(t, dir, "commit", "-qm", "allow everything")
		git(t, dir, "checkout", "-q", "main")
	}

	t.Run("main has none", func(t *testing.T) {
		dir := newRepo(t)
		branchAllowsItself(t, dir)
		config, ignore, source := scan(t, dir)
		if config != defaultGitleaksConfig || ignore != "" || source == dir {
			t.Fatalf("config=%q ignore=%q source=%s, want the default rules, an empty list, the main checkout", config, ignore, source)
		}
	})
	t.Run("main has its own", func(t *testing.T) {
		dir := newRepo(t)
		os.WriteFile(filepath.Join(dir, ".gitleaksignore"), []byte("main-fingerprint\n"), 0o644)
		os.WriteFile(filepath.Join(dir, ".gitleaks.toml"), []byte("title = \"main\"\n"), 0o644)
		git(t, dir, "add", ".gitleaksignore", ".gitleaks.toml")
		git(t, dir, "commit", "-qm", "main rules")
		git(t, dir, "checkout", "-q", "feat")
		git(t, dir, "rebase", "-q", "main")
		git(t, dir, "checkout", "-q", "main")
		branchAllowsItself(t, dir)
		config, ignore, _ := scan(t, dir)
		if config != defaultGitleaksConfig || ignore != "" {
			t.Fatalf("config=%q ignore=%q, want main's", config, ignore)
		}
	})
}

// A gitleaks on the tester's PATH must not reach a test that injects no
// LookPath: TestMain pins the package default to "not found".
func TestShipTestsIgnoreAGitleaksOnPATH(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gitleaks"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var calls []call
	if err := Run(context.Background(), Options{Root: newRepo(t), Branch: "feat", Run: recorder("", &calls)}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.HasPrefix(c.cmd, "gitleaks") {
			t.Fatalf("the PATH's gitleaks ran: %+v", calls)
		}
	}
}

func TestShipSkipsGitleaksWithAWarningWhenAbsent(t *testing.T) {
	dir := newRepo(t)
	var calls []call
	var out bytes.Buffer
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	if err := Run(context.Background(), Options{Root: dir, Branch: "feat", LookPath: missing, Run: recorder("", &calls)}, &out); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.Contains(out.String(), "gitleaks not found") {
		t.Fatalf("calls=%d out=%q", len(calls), out.String())
	}
}
