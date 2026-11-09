package evals

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

// Opt in after building the renderer; the browser uses invented demo data only.
func TestQueuedMessageLayoutScenario(t *testing.T) {
	if os.Getenv("COMMANDER_APP_LAYOUT_EVAL") != "1" {
		t.Skip("set COMMANDER_APP_LAYOUT_EVAL=1 with app/dist built and headless Chrome available")
	}
	// Ctrl-C was caught to end the browser run: once the cleanups below are
	// done (this one is registered first, so it runs last), exit as Ctrl-C would.
	interrupted := false
	t.Cleanup(func() {
		if interrupted {
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		}
	})
	// The browser profiles go under this dir, so anything that outlives the run is found by it.
	tmp := t.TempDir()
	testguard.NoLeakedProcesses(t, tmp)
	ctx, signalled, cancel := layoutContext(t, evalTimeout(t, 8*time.Minute))
	out, err := runProcessGroup(ctx, filepath.Join(repoRoot(), "app"), append(os.Environ(), "TMPDIR="+tmp), "node", "scripts/queued-message-check.mjs")
	interrupted = signalled()
	cancel()
	if err != nil {
		t.Fatalf("queue layout: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// cleanupMargin is left between a layout run's deadline and the test's own:
// the group's grace and WaitDelay, the leak guard, and t.Fatalf's report.
const cleanupMargin = 30 * time.Second

// layoutContext bounds a layout run by want, capped by the test binary's
// -timeout less cleanupMargin, and ends it on Ctrl-C or SIGTERM. Ctrl-C only
// reaches the test's process group, which the run has left (Setpgid). The
// signals are diverted only until cancel, so other tests keep the defaults.
// signalled reports whether a signal ended the run; ask before cancel.
func layoutContext(t *testing.T, want time.Duration) (ctx context.Context, signalled func() bool, cancel context.CancelFunc) {
	budget := want
	if d, ok := t.Deadline(); ok {
		budget = capBudget(want, time.Until(d))
	}
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancelTimeout := context.WithTimeout(sig, budget)
	return ctx, func() bool { return sig.Err() != nil }, func() {
		cancelTimeout()
		stop()
	}
}

// capBudget keeps want inside left less cleanupMargin; with less than the
// margin left it takes half of what is left.
func capBudget(want, left time.Duration) time.Duration {
	b := left - cleanupMargin
	if b < left/2 {
		b = left / 2
	}
	return min(want, b)
}

// groupGrace is how long what is left of a run's group has, after SIGTERM, to
// run its signal handlers (smoke.mjs ending its detached browser) before SIGKILL.
var groupGrace = 3 * time.Second

// runProcessGroup runs a command in its own process group and ends the whole
// group when ctx expires and once the command returns (SIGTERM, a grace, then
// SIGKILL), so no browser or helper it started outlives the test.
func runProcessGroup(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
		endGroup(cmd.Process.Pid, groupGrace)
	}
	if ctx.Err() != nil {
		err = errors.Join(err, context.Cause(ctx))
	}
	return []byte(buf.String()), err
}

// endGroup asks the group to stop, waits up to grace for it to be gone, then
// kills what is left.
func endGroup(pgid int, grace time.Duration) {
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return // no member left
	}
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// A hung layout run fails at its deadline and leaves no process behind, even
// one that ignores SIGTERM.
func TestQueuedMessageLayoutDeadlineEndsTheGroup(t *testing.T) {
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	writeExecutable(t, filepath.Join(dir, "stubborn.sh"), "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	writeExecutable(t, filepath.Join(dir, "hang.sh"), "#!/bin/sh\n\""+dir+"/stubborn.sh\" &\n\""+dir+"/stubborn.sh\" &\nwait\n")
	budget := evalTimeout(t, 300*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_, err := runProcessGroup(ctx, dir, os.Environ(), filepath.Join(dir, "hang.sh"))
	elapsed := time.Since(start)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung run did not fail at its deadline: %v", err)
	}
	// WaitDelay (5s) for the pipes the stubborn children hold, then the grace.
	if elapsed > budget+groupGrace+evalTimeout(t, 8*time.Second) {
		t.Fatalf("hung run took %v, budget %v", elapsed, budget)
	}
	if left := testguard.ProcessesMatching(dir); len(left) != 0 {
		t.Fatalf("processes outlived the deadline: %v", left)
	}
}

// The real chain: node queued-message-check.mjs -> smoke.mjs -> a detached
// browser in its own process group (a stand-in that ignores SIGTERM, which only
// smoke.mjs's own handler can end). Stopped mid-run, smoke.mjs must end it
// before the group is killed, without the leak guard's help.
func TestQueuedMessageLayoutStopEndsSmokesBrowser(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	browser := filepath.Join(dir, "browser.sh")
	writeExecutable(t, browser, "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	env := append(os.Environ(), "TMPDIR="+dir, "CHROME_HEADLESS_SHELL="+browser)

	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 20*time.Second))
	defer cancel()
	started := make(chan struct{})
	go func() {
		for ctx.Err() == nil {
			if len(testguard.ProcessesMatching(browser)) > 0 {
				close(started)
				time.Sleep(100 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	out, err := runProcessGroup(ctx, filepath.Join(repoRoot(), "app"), env, "node", "scripts/queued-message-check.mjs", "--dist", dist)
	select {
	case <-started:
	default:
		t.Fatalf("the stand-in browser never started: %v\n%s", err, out)
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped run did not fail: %v\n%s", err, out)
	}
	if left := testguard.ProcessesMatching(dir); len(left) != 0 {
		t.Fatalf("processes outlived the stop: %v\n%s", left, out)
	}
}

// The same chain with a slow handler, made up so the race is certain: the
// parent dies at once on SIGTERM (as queued-message-check.mjs did) and only it
// holds the test's pipes, so the run returns at once, while the child needs a
// moment before it ends its detached browser (a busy event loop).
// Ending the group as soon as the parent is gone would leave the browser.
func TestQueuedMessageLayoutGraceLetsTheChildEndItsBrowser(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	browser := filepath.Join(dir, "browser.sh")
	writeExecutable(t, browser, "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	child := filepath.Join(dir, "child.mjs")
	if err := os.WriteFile(child, []byte(`import { spawn } from 'node:child_process'
const b = spawn(process.env.STAND_IN_BROWSER, [], { stdio: 'ignore', detached: true })
process.on('SIGTERM', () => setTimeout(() => { process.kill(-b.pid, 'SIGKILL'); process.exit(1) }, 300))
setInterval(() => {}, 1000)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "parent.mjs")
	if err := os.WriteFile(parent, []byte(`import { spawnSync } from 'node:child_process'
spawnSync(process.execPath, [process.argv[2]])
`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for len(testguard.ProcessesMatching(browser)) == 0 && ctx.Err() == nil {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	// The browser's path goes by env: in argv it would match the node processes.
	_, err := runProcessGroup(ctx, dir, append(os.Environ(), "STAND_IN_BROWSER="+browser), "node", parent, child)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped run did not fail: %v", err)
	}
	if left := testguard.ProcessesMatching(dir); len(left) != 0 {
		t.Fatalf("processes outlived the stop: %v", left)
	}
}

// Ctrl-C while a run is in flight ends its whole group, though the group is
// not the test's own; afterwards the signal has its default meaning again.
func TestQueuedMessageLayoutInterruptEndsTheGroup(t *testing.T) {
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	writeExecutable(t, filepath.Join(dir, "hang.sh"), "#!/bin/sh\nwhile :; do sleep 1; done\n")
	ctx, signalled, cancel := layoutContext(t, time.Minute)
	defer cancel()
	watch, stopWatch := context.WithCancel(ctx)
	wait := interruptWhenSeen(watch, ctx, func() bool { return len(testguard.ProcessesMatching(dir)) != 0 }, interruptSelf)
	defer func() { stopWatch(); wait() }() // before cancel lets SIGINT kill the binary again
	start := time.Now()
	out, err := runProcessGroup(ctx, dir, os.Environ(), filepath.Join(dir, "hang.sh"))
	stopWatch()
	wait()
	// Go 1.26+ also records "interrupt signal received" as the cause; 1.25 has
	// only context.Canceled, so ask signalled() which one ended the run.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run did not fail with the signal: %v\n%s", err, out)
	}
	if !signalled() {
		t.Fatal("signalled() = false after Ctrl-C")
	}
	if elapsed := time.Since(start); elapsed > evalTimeout(t, 15*time.Second) {
		t.Fatalf("interrupted run took %v", elapsed)
	}
	if left := testguard.ProcessesMatching(dir); len(left) != 0 {
		t.Fatalf("processes outlived the interrupt: %v", left)
	}
	cancel()
	if signal.Ignored(os.Interrupt) {
		t.Fatal("SIGINT left ignored")
	}
}

// interruptSelf is Ctrl-C at the test binary.
func interruptSelf() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) }

// interruptWhenSeen calls interrupt once seen reports true, then waits for run
// to end, so the signal is handled while it is still diverted. It gives up
// without interrupting when watch ends first (the child never started, or the
// run's budget ran out): a SIGINT after layoutContext's cancel would end the
// whole test binary. wait returns once it is done; call it before cancel.
func interruptWhenSeen(watch, run context.Context, seen func() bool, interrupt func()) (wait func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !seen() {
			if watch.Err() != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		interrupt()
		<-run.Done()
	}()
	return func() { <-done }
}

// A run whose child is never seen is not interrupted, and nothing is left
// pending that could arrive after the signals have their default meaning.
func TestQueuedMessageLayoutInterruptNeedsTheChild(t *testing.T) {
	dir := t.TempDir()
	ctx, signalled, cancel := layoutContext(t, time.Minute)
	defer cancel()
	watch, stopWatch := context.WithCancel(ctx)
	var calls atomic.Int32
	wait := interruptWhenSeen(watch, ctx, func() bool { return len(testguard.ProcessesMatching(dir)) != 0 }, func() {
		calls.Add(1)
		interruptSelf()
	})
	_, err := runProcessGroup(ctx, dir, os.Environ(), filepath.Join(dir, "missing.sh"))
	stopWatch()
	wait()
	if err == nil {
		t.Fatal("run of a missing command did not fail")
	}
	if n := calls.Load(); n != 0 || signalled() {
		t.Fatalf("interrupted %d times, signalled() = %v, with no child", n, signalled())
	}
	cancel()
	late := make(chan os.Signal, 1)
	signal.Notify(late, os.Interrupt)
	defer signal.Stop(late)
	select {
	case <-late:
		t.Fatal("SIGINT arrived after the run")
	case <-time.After(300 * time.Millisecond):
	}
}

// Once seen, the interrupt is sent and wait holds until the run has ended.
func TestQueuedMessageLayoutInterruptWaitsForTheRun(t *testing.T) {
	run, end := context.WithCancel(context.Background())
	var calls atomic.Int32
	wait := interruptWhenSeen(context.Background(), run, func() bool { return true }, func() { calls.Add(1) })
	waited := make(chan struct{})
	go func() { wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait returned before the run ended")
	case <-time.After(100 * time.Millisecond):
	}
	end()
	<-waited
	if n := calls.Load(); n != 1 {
		t.Fatalf("interrupted %d times, want 1", n)
	}
}

func TestQueuedMessageLayoutBudgetFitsTheTestDeadline(t *testing.T) {
	for _, c := range []struct{ want, left, got time.Duration }{
		{8 * time.Minute, 10 * time.Minute, 8 * time.Minute},
		{8 * time.Minute, 5 * time.Minute, 5*time.Minute - cleanupMargin},
		{8 * time.Minute, 40 * time.Second, 20 * time.Second},
		{time.Second, 10 * time.Second, time.Second},
	} {
		if got := capBudget(c.want, c.left); got != c.got {
			t.Errorf("capBudget(%v, %v) = %v, want %v", c.want, c.left, got, c.got)
		}
	}
	d, ok := t.Deadline()
	if !ok {
		t.Skip("no -timeout")
	}
	ctx, _, cancel := layoutContext(t, 24*time.Hour)
	defer cancel()
	got, _ := ctx.Deadline()
	if got.After(d) {
		t.Fatalf("run deadline %v passes the test's %v", got, d)
	}
	if limit := d.Add(-cleanupMargin + time.Second); time.Until(d) > 2*cleanupMargin && got.After(limit) {
		t.Fatalf("run deadline %v passes the test's %v less the margin", got, d)
	}
}
