package ship

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startedPid reads the pid a scratch shell wrote to file.
func startedPid(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(file)
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && perr == nil {
			t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", file)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wantGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d outlived its step", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shortGrace(t *testing.T) {
	t.Helper()
	old := pipeGrace
	pipeGrace = 200 * time.Millisecond
	t.Cleanup(func() { pipeGrace = old })
}

// The review's reproduction: the shell's child survives a kill of the shell and
// holds the output pipe. A plain CommandContext returned only when the sleep did.
func TestRunCommandDeadlineEndsASurvivingChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runCommand(ctx, t.TempDir(), "sh", "-c", "sleep 30 $(echo 0)")
	if err == nil {
		t.Fatal("a killed step reads as passed")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the deadline took %s to end the step", took)
	}
}

func TestRunCommandDeadlineKillsTheWholeGroup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := runCommand(ctx, t.TempDir(), "sh", "-c", "sleep 30 & echo $! > "+file+"; wait"); err == nil {
		t.Fatal("a killed step reads as passed")
	}
	wantGone(t, startedPid(t, file))
}

// A step that exited 0 passed, whatever it left holding the pipe; the leftover
// neither holds up the ship nor outlives the step.
func TestRunCommandPassedStepWithALeftoverIsGreenAndLeavesNothing(t *testing.T) {
	shortGrace(t)
	file := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	o, err := runCommand(context.Background(), t.TempDir(), "sh", "-c", "echo kept; sleep 30 & echo $! > "+file+"; exit 0")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(o, "kept") {
		t.Errorf("output = %q", o)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	wantGone(t, startedPid(t, file))
}

func TestRunCommandRedStepStaysRed(t *testing.T) {
	o, err := runCommand(context.Background(), t.TempDir(), "sh", "-c", "echo why; exit 3")
	if err == nil || !strings.Contains(o, "why") {
		t.Fatalf("err = %v, output = %q", err, o)
	}
}

// A child in a session of its own is out of the group's reach; it still cannot
// hold the ship past the pipe grace.
func TestRunCommandChildThatLeftTheGroupCannotHoldItUp(t *testing.T) {
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid here")
	}
	shortGrace(t)
	file := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runCommand(ctx, t.TempDir(), "sh", "-c", setsid+" sleep 30 & echo $! > "+file+"; wait")
		done <- err
	}()
	startedPid(t, file) // its cleanup ends the sleep
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a killed step reads as passed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the escaped child held the step up")
	}
}

func TestRunGitDeadlineEndsAHungHook(t *testing.T) {
	dir := newRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runGit(ctx, dir, "-c", "alias.hang=!sleep 30 $(echo 0)", "hang"); err == nil {
		t.Fatal("a killed git reads as passed")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
}

// hangRefs makes every later ref update in dir hang in a hook that survives git.
func hangRefs(t *testing.T, dir string) {
	t.Helper()
	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 30 $(echo 0)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// exclusiveStub stands in for the lock and reports when it is released.
func exclusiveStub(released *bool, then func()) func(context.Context, io.Writer) (func(), error) {
	return func(context.Context, io.Writer) (func(), error) {
		if then != nil {
			then()
		}
		return func() { *released = true }, nil
	}
}

// wantEndedInTime runs the ship and holds it to ending fast, with the lock freed
// (the deferred release runs before Run returns) and a message that says why.
func wantEndedInTime(t *testing.T, opts Options, released *bool, says ...string) {
	t.Helper()
	start := time.Now()
	err := Run(context.Background(), opts, io.Discard)
	if err == nil {
		t.Fatal("a hang under the lock reads as a landed ship")
	}
	if !errors.As(err, &Inconclusive{}) {
		t.Errorf("reads as a verdict on the branch: %v", err)
	}
	for _, want := range says {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %s", took)
	}
	if !*released {
		t.Error("the lock was not released")
	}
}

func TestHungMergeEndsTheShipAndFreesTheLock(t *testing.T) {
	shortGrace(t)
	dir := newRepo(t)
	before := git(t, dir, "rev-parse", "main")
	released := false
	run := func(context.Context, string, string, ...string) (string, error) {
		hangRefs(t, dir) // only the merge is left to update a ref
		return "", nil
	}
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: run,
		Exclusive: exclusiveStub(&released, nil), StepBudget: 500 * time.Millisecond},
		&released, "ff-only merge", "step budget ran out", "gave the lock up", "may have landed")
	if now := git(t, dir, "rev-parse", "main"); now != before {
		t.Errorf("main moved to %s", now)
	}
}

func TestHungCatchUpEndsTheShipAndFreesTheLock(t *testing.T) {
	shortGrace(t)
	dir := newRepo(t)
	released := false
	moved := func() { commit(t, dir, "c"); hangRefs(t, dir) } // main moves; its replay then hangs
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: okRun,
		Exclusive: exclusiveStub(&released, moved), StepBudget: 500 * time.Millisecond},
		&released, "catch-up", "step budget ran out", "gave the lock up")
}

// A hold read that ignores its context is left behind, not waited for.
func TestHungHoldCheckEndsTheShipAndFreesTheLock(t *testing.T) {
	dir := newRepo(t)
	released, locked := false, false
	never := make(chan struct{})
	defer close(never)
	hold := func(context.Context) error {
		if locked {
			<-never
		}
		return nil
	}
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: okRun, Hold: hold,
		Exclusive: exclusiveStub(&released, func() { locked = true }), StepBudget: 100 * time.Millisecond},
		&released, "hold check", "step budget ran out")
}

func TestHungCheckSlotEndsTheShipAndFreesTheLock(t *testing.T) {
	dir := newRepo(t)
	t.Setenv(ChecksEnv, "1")
	busy, _, err := lockVerifySlot(context.Background(), dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	released := false
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: okRun,
		Exclusive: exclusiveStub(&released, nil), StepBudget: 100 * time.Millisecond},
		&released, "check slot", "step budget ran out")
}

// However much budget a verify has, the hold ends it.
func TestHoldBudgetEndsTheVerifyAndIsNotCalledRed(t *testing.T) {
	dir := newRepo(t)
	released := false
	run := func(ctx context.Context, _, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: run,
		Exclusive: exclusiveStub(&released, nil), HoldBudget: time.Second, VerifyBudget: time.Hour},
		&released, "ship lock budget ran out", "not known red")
}

// However often main moves, the hold ends the replays.
func TestHoldBudgetCapsReplays(t *testing.T) {
	dir := newRepo(t)
	released, n := false, 0
	run := func(context.Context, string, string, ...string) (string, error) {
		n++
		commit(t, dir, "m"+strconv.Itoa(n))
		time.Sleep(400 * time.Millisecond)
		return "", nil
	}
	wantEndedInTime(t, Options{Root: dir, Branch: "feat", Run: run,
		Exclusive: exclusiveStub(&released, nil), HoldBudget: 200 * time.Millisecond}, &released)
	if git(t, dir, "rev-parse", "main") == git(t, dir, "rev-parse", "feat") {
		t.Error("the branch landed")
	}
}

// The default hold fits a full thread wait plus a full verify, with room left
// for the steps around them; it is the one number that bounds a ship's hold.
func TestDefaultHoldFitsAFullVerify(t *testing.T) {
	if room := DefaultHoldBudget - DefaultVerifyBudget - defaultThreadsWait; room < 5*time.Minute {
		t.Fatalf("only %s left around a full verify", room)
	}
	if DefaultHoldBudget > 2*time.Hour {
		t.Fatalf("worst case hold is %s", DefaultHoldBudget)
	}
}

func slotStub(taken chan<- struct{}, free <-chan struct{}) func(context.Context, io.Writer) (func(), error) {
	return func(ctx context.Context, _ io.Writer) (func(), error) {
		taken <- struct{}{}
		select {
		case <-free:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// One stuck slot holder used to keep every project's ships behind the lock: the
// wait now runs before the lock, so another project's ship goes straight on.
func TestStuckSlotDoesNotHoldTheMachineLock(t *testing.T) {
	home := t.TempDir()
	taken, free := make(chan struct{}, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan error, 1)
	go func() {
		_, err := holdVerifyLock(ctx, home, "/a", io.Discard, testPoll, slotStub(taken, free))
		failed <- err
	}()
	<-taken // project a is waiting for its slot

	other, err := holdVerifyLock(ctx, home, "/b", io.Discard, testPoll, nil)
	if err != nil {
		t.Fatalf("the other project waited: %v", err)
	}
	other()
	if left := tickets(t, home, "/a"); len(left) != 1 {
		t.Errorf("the slot wait lost its place in line: %v", left)
	}
	cancel()
	if err := <-failed; err == nil {
		t.Fatal("cancel did not end the slot wait")
	}
	if left := tickets(t, home, "/a"); len(left) != 0 {
		t.Errorf("a cancelled waiter stays in line: %v", left)
	}
}

// Only the head of a line takes a slot, so a long line idles one slot, and the
// wait for it keeps the place: nobody behind passes it.
func TestOnlyTheHeadOfTheLineTakesASlot(t *testing.T) {
	home := t.TempDir()
	dir := queueDir(home, "/p")
	os.MkdirAll(dir, 0o700)
	ahead, _, err := takeTicket(dir)
	if err != nil {
		t.Fatal(err)
	}
	taken, free := make(chan struct{}, 2), make(chan struct{})
	got := make(chan func(), 1)
	go func() {
		r, err := holdVerifyLock(context.Background(), home, "/p", io.Discard, testPoll, slotStub(taken, free))
		if err == nil {
			got <- r
		}
	}()
	select {
	case <-taken:
		t.Fatal("a ship behind a live ticket took a slot")
	case <-time.After(20 * testPoll):
	}
	ahead.Close()
	<-taken // now it is the head

	behind, _ := enqueue(t, context.Background(), home, "/p")
	notHeld(t, behind)
	close(free)
	r := held(t, got)
	notHeld(t, behind)
	r()
	held(t, behind)()
}

// A slot taken while waiting for the lock goes back when the wait ends, and is
// held until the lock is released otherwise.
func TestSlotIsReleasedWithTheLockAndWhenTheWaitEnds(t *testing.T) {
	home := t.TempDir()
	busy, err := holdVerifyLock(context.Background(), home, "/a", io.Discard, testPoll, nil)
	if err != nil {
		t.Fatal(err)
	}
	slots := 0
	slot := func(context.Context, io.Writer) (func(), error) {
		slots++
		return func() { slots-- }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := holdVerifyLock(ctx, home, "/b", io.Discard, testPoll, slot); err == nil {
		t.Fatal("got the lock while it was held")
	}
	if slots != 0 {
		t.Errorf("slots held after the wait ended: %d", slots)
	}
	busy()
	r, err := holdVerifyLock(context.Background(), home, "/b", io.Discard, testPoll, slot)
	if err != nil {
		t.Fatal(err)
	}
	if slots != 1 {
		t.Errorf("slots held with the lock: %d", slots)
	}
	r()
	if slots != 0 {
		t.Errorf("slots held after the release: %d", slots)
	}
}

// A replay keeps the slot it has and asks for no other.
func TestReplayKeepsItsSlot(t *testing.T) {
	dir := newRepo(t)
	slots := 0
	slot := func(context.Context, io.Writer) (func(), error) { slots++; return func() {}, nil }
	home := t.TempDir()
	exclusive := func(ctx context.Context, out io.Writer) (func(), error) {
		return holdVerifyLock(ctx, home, dir, out, testPoll, slot)
	}
	checks, moved := 0, false
	run := buildCounter(func() {
		if !moved {
			moved = true
			commit(t, dir, "c")
		}
	}, &checks)
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Root: dir, Branch: "feat", Run: run, Exclusive: exclusive}, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if checks != 2 || slots != 1 {
		t.Errorf("checks = %d, slots = %d; want 2 and 1", checks, slots)
	}
}
