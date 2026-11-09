package ship

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

const testPoll = 5 * time.Millisecond

func tickets(t *testing.T, home, root string) []string {
	t.Helper()
	ents, err := os.ReadDir(queueDir(home, root))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// enqueue starts a waiter and returns once its ticket is in line; the channel
// delivers the release when it holds the lock.
func enqueue(t *testing.T, ctx context.Context, home, root string) (<-chan func(), <-chan error) {
	t.Helper()
	before := len(tickets(t, home, root))
	got, failed := make(chan func(), 1), make(chan error, 1)
	go func() {
		release, err := holdVerifyLock(ctx, home, root, io.Discard, testPoll, nil)
		if err != nil {
			failed <- err
			return
		}
		got <- release
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(tickets(t, home, root)) <= before {
		if time.Now().After(deadline) {
			t.Fatal("no ticket appeared")
		}
		time.Sleep(time.Millisecond)
	}
	return got, failed
}

func held(t *testing.T, got <-chan func()) func() {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("did not get the lock")
		return nil
	}
}

func notHeld(t *testing.T, got <-chan func()) {
	t.Helper()
	select {
	case <-got:
		t.Fatal("got the lock out of turn")
	case <-time.After(20 * testPoll):
	}
}

func TestQueueIsFirstComeFirstServed(t *testing.T) {
	home := t.TempDir()
	dir := queueDir(home, "/p")
	os.MkdirAll(dir, 0o700)
	// The first ship holds the lock; its ticket is gone once it does.
	r0, err := holdVerifyLock(context.Background(), home, "/p", io.Discard, testPoll, nil)
	if err != nil {
		t.Fatal(err)
	}
	release := r0
	var waiters []<-chan func()
	for i := 0; i < 4; i++ {
		got, _ := enqueue(t, context.Background(), home, "/p")
		waiters = append(waiters, got)
	}
	for i, got := range waiters {
		release()
		release = held(t, got)
		for _, later := range waiters[i+1:] {
			notHeld(t, later)
		}
	}
	release()
	if left := tickets(t, home, "/p"); len(left) != 0 {
		t.Errorf("tickets left behind: %v", left)
	}
}

func TestQueueLateArrivalWaitsBehindALiveTicketEvenWithTheLockFree(t *testing.T) {
	home := t.TempDir()
	dir := queueDir(home, "/p")
	os.MkdirAll(dir, 0o700)
	ahead, name, err := takeTicket(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := enqueue(t, context.Background(), home, "/p")
	notHeld(t, got)
	ahead.Close() // its owner dies: the flock goes with it
	r := held(t, got)
	r()
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Errorf("stale ticket was not removed: %v", err)
	}
}

func TestQueueSkipsAStaleTicketOfADeadPid(t *testing.T) {
	home := t.TempDir()
	dir := queueDir(home, "/p")
	os.MkdirAll(dir, 0o700)
	// A ticket left by a dead ship, however old the name and whatever its pid.
	stale := filepath.Join(dir, ticketPrefix+"00000000000000000001-999999-1")
	os.WriteFile(stale, []byte("999999\n"), 0o600)
	abandoned := filepath.Join(dir, newTicket+"old")
	os.WriteFile(abandoned, nil, 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(abandoned, old, old)

	r, err := holdVerifyLock(context.Background(), home, "/p", io.Discard, testPoll, nil)
	if err != nil {
		t.Fatal(err)
	}
	r()
	if left := tickets(t, home, "/p"); len(left) != 0 {
		t.Errorf("left: %v", left)
	}
}

func TestQueueCancelLeavesTheLine(t *testing.T) {
	home := t.TempDir()
	release, err := holdVerifyLock(context.Background(), home, "/p", io.Discard, testPoll, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	_, failed := enqueue(t, ctx, home, "/p")
	cancel()
	select {
	case err := <-failed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
	if left := tickets(t, home, "/p"); len(left) != 0 {
		t.Errorf("a cancelled waiter stays in line: %v", left)
	}
}

func TestQueueOtherProjectHasItsOwnLineButSharesTheLock(t *testing.T) {
	home := t.TempDir()
	dir := queueDir(home, "/a")
	os.MkdirAll(dir, 0o700)
	waiting, _, err := takeTicket(dir) // project a has a ship waiting in line
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := holdVerifyLock(ctx, home, "/b", io.Discard, testPoll, nil) // not behind a's line
	if err != nil {
		t.Fatal(err)
	}
	c, _ := enqueue(t, context.Background(), home, "/c")
	notHeld(t, c) // one verify per machine still
	r()
	r = held(t, c)
	r()
}

// TestShipQueueHelper is the crashing ship of the next test, not a test itself.
func TestShipQueueHelper(t *testing.T) {
	home := os.Getenv("SHIP_QUEUE_HELPER_HOME")
	if home == "" {
		t.Skip("helper")
	}
	if _, err := holdVerifyLock(context.Background(), home, "/p", io.Discard, testPoll, nil); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("holding\n")
	time.Sleep(time.Minute)
}

// A ship killed outright, one holding the lock and one waiting in line, leaves
// nothing that holds up the next.
func TestQueueSurvivesKilledShips(t *testing.T) {
	home := t.TempDir()
	testguard.NoLeakedProcesses(t, home)
	start := func(waitForHold bool) *exec.Cmd {
		before := len(tickets(t, home, "/p"))
		cmd := exec.Command(os.Args[0], "-test.run=^TestShipQueueHelper$", "-test.v", "--", home)
		cmd.Env = append(os.Environ(), "SHIP_QUEUE_HELPER_HOME="+home)
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		if waitForHold {
			scan := bufio.NewScanner(stdout)
			for scan.Scan() {
				if strings.Contains(scan.Text(), "holding") {
					return cmd
				}
			}
			t.Fatal("helper never held the lock")
		}
		deadline := time.Now().Add(20 * time.Second)
		for len(tickets(t, home, "/p")) <= before {
			if time.Now().After(deadline) {
				t.Fatal("helper never queued")
			}
			time.Sleep(time.Millisecond)
		}
		return cmd
	}
	holder := start(true)
	waiter := start(false)
	got, _ := enqueue(t, context.Background(), home, "/p")
	notHeld(t, got)
	waiter.Process.Kill()
	waiter.Wait()
	notHeld(t, got) // the holder still has the lock
	holder.Process.Kill()
	holder.Wait()
	r := held(t, got)
	r()
}

func buildCounter(on func(), n *int) Runner {
	return func(_ context.Context, d, name string, args ...string) (string, error) {
		if name == "go" && len(args) > 0 && args[0] == "build" {
			*n++
			if on != nil {
				on()
			}
		}
		return "", nil
	}
}

// Main moving during the queue wait costs no verify: the one verify runs on the
// replayed tip and that tip lands.
func TestShipCatchesUpAfterTheQueueWaitBeforeVerifying(t *testing.T) {
	dir := newRepo(t)
	exclusive := func(context.Context, io.Writer) (func(), error) {
		commit(t, dir, "c")
		return func() {}, nil
	}
	var checks int
	var out bytes.Buffer
	opts := Options{Root: dir, Branch: "feat", Exclusive: exclusive, Run: buildCounter(nil, &checks)}
	if err := Run(context.Background(), opts, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if checks != 1 {
		t.Errorf("checks = %d, want 1\n%s", checks, out.String())
	}
	for _, f := range []string{"b", "c"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("main lacks %s: %v", f, err)
		}
	}
	if !strings.Contains(out.String(), "while it waited in the ship queue") {
		t.Errorf("output:\n%s", out.String())
	}
}

// The lock spans verify, replay and merge: one acquisition however often main
// moves, released only after main holds the ship.
func TestShipHoldsTheLockThroughReplayAndMerge(t *testing.T) {
	dir := newRepo(t)
	var mainAtRelease string
	locks := 0
	exclusive := func(context.Context, io.Writer) (func(), error) {
		locks++
		return func() { mainAtRelease = git(t, dir, "rev-parse", "main") }, nil
	}
	checks, moved := 0, false
	run := buildCounter(func() {
		if !moved { // someone merges outside ship
			moved = true
			commit(t, dir, "c")
		}
	}, &checks)
	var out bytes.Buffer
	if err := Run(context.Background(), Options{Root: dir, Branch: "feat", Exclusive: exclusive, Run: run}, &out); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if locks != 1 || checks != 2 {
		t.Errorf("locks = %d, checks = %d; want 1 and 2", locks, checks)
	}
	if now := git(t, dir, "rev-parse", "main"); mainAtRelease != now {
		t.Errorf("lock released at %s before the merge landed %s", mainAtRelease, now)
	}
}

func TestShipGivesUpWhenMainKeepsMoving(t *testing.T) {
	dir := newRepo(t)
	checks, n := 0, 0
	run := buildCounter(func() { n++; commit(t, dir, "m"+string(rune('a'+n))) }, &checks)
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", Run: run}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "moved 4 times") {
		t.Fatalf("err = %v", err)
	}
	if checks != maxReplays+1 {
		t.Errorf("checks = %d", checks)
	}
}

// A hold set while a ship waited in line stops it before its verify.
func TestShipHoldSetDuringTheQueueWaitSkipsTheVerify(t *testing.T) {
	dir := newRepo(t)
	onHold := false
	exclusive := func(context.Context, io.Writer) (func(), error) {
		onHold = true
		return func() {}, nil
	}
	checks := 0
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", Exclusive: exclusive, Run: buildCounter(nil, &checks),
		Hold: func(context.Context) error {
			if onHold {
				return errors.New("held by manager-x: stop")
			}
			return nil
		}}, io.Discard)
	if err == nil || checks != 0 {
		t.Fatalf("err = %v, checks = %d", err, checks)
	}
}

// Waiting longer than the verify budget for the lock and the slot leaves the
// whole budget to the verify.
func TestQueueWaitDoesNotCountAgainstTheBudget(t *testing.T) {
	dir := newRepo(t)
	budget := 100 * time.Millisecond
	wait := func(context.Context, io.Writer) (func(), error) {
		time.Sleep(2 * budget)
		return func() {}, nil
	}
	var verifyErr error
	run := func(ctx context.Context, d, name string, args ...string) (string, error) {
		if err := ctx.Err(); err != nil {
			verifyErr = err
		}
		return "", nil
	}
	opts := Options{Root: dir, Branch: "feat", Exclusive: wait, VerifyBudget: budget, Run: run}
	if err := Run(context.Background(), opts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if verifyErr != nil {
		t.Errorf("the verify started with its budget gone: %v", verifyErr)
	}
}

func TestVerifyOverBudgetSaysSoAndIsNotCalledRed(t *testing.T) {
	dir := newRepo(t)
	run := func(ctx context.Context, d, name string, args ...string) (string, error) {
		<-ctx.Done()
		return "", errors.New("signal: killed")
	}
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", VerifyBudget: 20 * time.Millisecond, Run: run}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "verify budget ran out") {
		t.Fatalf("err = %v", err)
	}
	if !errors.As(err, &Inconclusive{}) {
		t.Errorf("reads as a verdict on the branch: %v", err)
	}
	if git(t, dir, "rev-parse", "main") == git(t, dir, "rev-parse", "feat") {
		t.Error("main moved")
	}
}

func TestQueueWaitHasItsOwnBound(t *testing.T) {
	t.Setenv(QueueWaitEnv, "nonsense")
	if queueWait() != defaultQueueWait {
		t.Fatalf("queueWait = %s", queueWait())
	}
	t.Setenv(QueueWaitEnv, "20ms")
	if queueWait() != 20*time.Millisecond {
		t.Fatalf("queueWait = %s", queueWait())
	}
	dir := newRepo(t)
	forever := func(ctx context.Context, _ io.Writer) (func(), error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	viaSlot := func(ctx context.Context, out io.Writer) (func(), error) {
		return holdVerifyLock(ctx, t.TempDir(), dir, out, testPoll, forever)
	}
	for name, exclusive := range map[string]func(context.Context, io.Writer) (func(), error){
		"forever":  forever,
		"via slot": viaSlot,
	} {
		err := Run(context.Background(), Options{Root: dir, Branch: "feat", Exclusive: exclusive, Run: okRun}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "ship lock") || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
