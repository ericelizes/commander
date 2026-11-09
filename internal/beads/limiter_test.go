package beads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testLimiter(perDB, global int) *limiter {
	return newLimiter(func() int { return perDB }, func() int { return global })
}

// peak runs n calls on the limiter and reports the most that ran at once, per
// db and overall.
func peak(t *testing.T, l *limiter, keys []string, n int) (perKey map[string]int32, overall int32) {
	t.Helper()
	var mu sync.Mutex
	cur := map[string]int32{}
	perKey = map[string]int32{}
	var total, maxTotal atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		key := keys[i%len(keys)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.acquire(context.Background(), key, false)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			cur[key]++
			perKey[key] = max(perKey[key], cur[key])
			mu.Unlock()
			if v := total.Add(1); v > maxTotal.Load() {
				maxTotal.Store(v)
			}
			time.Sleep(10 * time.Millisecond)
			total.Add(-1)
			mu.Lock()
			cur[key]--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	return perKey, maxTotal.Load()
}

func TestBrConcurrencyCapsPerDBAndGlobal(t *testing.T) {
	l := testLimiter(2, 3)
	perKey, overall := peak(t, l, []string{"/a", "/b", "/c"}, 30)
	for k, v := range perKey {
		if v > 2 {
			t.Fatalf("%s ran %d at once; want <= 2", k, v)
		}
	}
	if overall > 3 {
		t.Fatalf("%d ran at once overall; want <= 3", overall)
	}
	if l.total != 0 || len(l.running) != 0 {
		t.Fatalf("slots leaked: total=%d running=%v", l.total, l.running)
	}
}

func TestBrConcurrencyCancelledWaiterLeavesTheQueue(t *testing.T) {
	l := testLimiter(1, 1)
	hold, err := l.acquire(context.Background(), "/a", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := l.acquire(ctx, "/a", false); done <- err }()
	for l.depth("/a") != 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled waiter got a slot")
	}
	if d := l.depth("/a"); d != 0 {
		t.Fatalf("depth after cancel = %d", d)
	}
	hold()
	release, err := l.acquire(context.Background(), "/a", false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if l.total != 0 {
		t.Fatalf("total = %d", l.total)
	}
}

func TestBrConcurrencyWritesJumpQueuedReads(t *testing.T) {
	l := testLimiter(1, 1)
	hold, _ := l.acquire(context.Background(), "/a", false)
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	start := func(name string, write bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.acquire(context.Background(), "/a", write)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			release()
		}()
	}
	for _, r := range []string{"r1", "r2", "r3"} {
		start(r, false)
		for l.depth("/a") != len(order)+int(r[1]-'0') {
			time.Sleep(time.Millisecond)
		}
	}
	start("w", true)
	for l.depth("/a") != 4 {
		time.Sleep(time.Millisecond)
	}
	hold()
	wg.Wait()
	if strings.Join(order, ",") != "w,r1,r2,r3" {
		t.Fatalf("order = %v; want the write first, reads FIFO", order)
	}
}

func TestBrConcurrencyIsWrite(t *testing.T) {
	for args, want := range map[string]bool{
		"show a --json": false, "list --json": false, "dep list a": false, "comments list a": false,
		"update a --status x": true, "close a": true, "create t": true, "dep add a b": true, "comments add a t": true,
		"--no-auto-flush update a": true,
	} {
		if got := isWrite(strings.Fields(args)); got != want {
			t.Errorf("isWrite(%q) = %v", args, got)
		}
	}
}

// The per-call timeout starts when the call runs: a call that waited longer than
// the timeout in the queue still gets its whole budget.
func TestBrConcurrencyTimeoutStartsAtExecution(t *testing.T) {
	old := brLimiter
	brLimiter = testLimiter(1, 1)
	defer func() { brLimiter = old }()
	t.Setenv(HeldEnv, "1")
	script := filepath.Join(t.TempDir(), "br")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.3\necho ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The caller's own deadline is not the per-call one, so it must not expire the queued call.
	adir := t.TempDir()
	hold, _ := brLimiter.acquire(context.Background(), limiterKey(adir), false)
	go func() { time.Sleep(200 * time.Millisecond); hold() }()
	out, _, err := defaultRunInDir(context.Background(), adir, script)
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBrConcurrencyRealProcessesStayUnderCap(t *testing.T) {
	old := brLimiter
	brLimiter = testLimiter(2, 6)
	defer func() { brLimiter = old }()
	t.Setenv(HeldEnv, "1")
	dir := t.TempDir()
	script := filepath.Join(dir, "br")
	body := "#!/bin/sh\nd=" + dir + "\nmkdir \"$d/run.$$\"\nn=$(ls \"$d\" | grep -c '^run[.]')\necho $n >> \"$d/peaks\"\nsleep 0.1\nrmdir \"$d/run.$$\" 2>> \"$d/err\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := defaultRunInDir(context.Background(), dir, script, "show", "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(filepath.Join(dir, "peaks"))
	for _, f := range strings.Fields(string(raw)) {
		if f > "2" {
			e, _ := os.ReadFile(filepath.Join(dir, "err"))
			t.Fatalf("saw %s br processes at once; peaks %q err %q", f, raw, e)
		}
	}
}

// Two holders (as separate processes would be) share the flock tokens: with one
// token per db the second waits for the first to release.
func TestBrConcurrencyCrossProcessTokens(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "1")
	t.Setenv(GlobalEnv, "4")
	first, err := acquireTokens(context.Background(), "/db1")
	if err != nil || len(first.files) != 2 {
		t.Fatalf("first: %v %v", first, err)
	}
	// another db is not held up
	other, err := acquireTokens(context.Background(), "/db2")
	if err != nil || len(other.files) != 2 {
		t.Fatalf("other db: %v %v", other, err)
	}
	other.release()
	got := make(chan *tokens, 1)
	go func() { tk, _ := acquireTokens(context.Background(), "/db1"); got <- tk }()
	select {
	case <-got:
		t.Fatal("second holder got a token while the first holds it")
	case <-time.After(150 * time.Millisecond):
	}
	first.release()
	select {
	case tk := <-got:
		tk.release()
	case <-time.After(2 * time.Second):
		t.Fatal("second holder never got the freed token")
	}
	// a cancelled waiter leaves
	hold, _ := acquireTokens(context.Background(), "/db1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := acquireTokens(ctx, "/db1"); err == nil {
		t.Fatal("cancelled token wait succeeded")
	}
	hold.release()
}

func TestBrConcurrencyGlobalTokensCapAcrossDBs(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "2")
	t.Setenv(GlobalEnv, "2")
	// At global=2, one token is shared and the other is reserved for writes.
	// Keep every acquisition bounded, including those expected to succeed.
	acquire := func(key string, write bool) *tokens {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		tk, err := acquireTokens(ctx, key, write)
		if err != nil {
			t.Fatalf("acquire %s (write=%v): %v", key, write, err)
		}
		t.Cleanup(tk.release)
		if len(tk.files) != 2 {
			t.Fatalf("acquire %s failed open: want two locked files, got %d", key, len(tk.files))
		}
		return tk
	}
	blocked := func(key string, write bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		tk, err := acquireTokens(ctx, key, write)
		if tk != nil {
			tk.release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("acquire %s (write=%v): want deadline exceeded, got %v", key, write, err)
		}
	}
	a := acquire("/a", false)
	blocked("/b", false) // A reader cannot take the write reservation.
	w := acquire("/b", true)
	blocked("/c", false)
	blocked("/c", true) // Even writes must obey the total global cap of two.
	a.release()
	b := acquire("/c", false) // Releasing a reader restores shared capacity.
	blocked("/d", true)
	w.release()
	acquire("/d", true) // Cancellation did not leak the reserved capacity.
	b.release()
}

func TestBrConcurrencyDisplayReadBacksOffWhenQueued(t *testing.T) {
	old := queueDepthFn
	defer func() { queueDepthFn = old }()
	var depth atomic.Int32
	queueDepthFn = func(string) int { return int(depth.Load()) }
	var cur, peakSeen atomic.Int32
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		v := cur.Add(1)
		if v > peakSeen.Load() {
			peakSeen.Store(v)
		}
		time.Sleep(5 * time.Millisecond)
		cur.Add(-1)
		return []byte("[]"), nil, nil
	}
	c := &cmdClient{run: run}
	ids := idsN(showBatch * 6)
	c.details(context.Background(), t.TempDir(), ids, true, true)
	if peakSeen.Load() < 2 {
		t.Fatalf("idle queue: peak %d; want parallel batches", peakSeen.Load())
	}
	peakSeen.Store(0)
	depth.Store(3)
	c.details(context.Background(), t.TempDir(), ids, true, true)
	if peakSeen.Load() != 1 {
		t.Fatalf("busy queue: peak %d; want 1", peakSeen.Load())
	}
}

func TestBrConcurrencyUnwritableSlotDirFailsOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv(HeldEnv, "")
	dir := filepath.Join(home, "br-slots")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	done := make(chan *tokens, 1)
	go func() { tk, _ := acquireTokens(context.Background(), "/db"); done <- tk }()
	select {
	case tk := <-done:
		if tk == nil || len(tk.files) != 0 {
			t.Fatalf("want empty tokens, got %v", tk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unwritable token dir blocked the call")
	}
}

func TestBrConcurrencyOpenErrorFailsOpen(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(HeldEnv, "")
	old := openToken
	openToken = func(string) (*os.File, error) { return nil, syscall.EMFILE }
	defer func() { openToken = old }()
	done := make(chan error, 1)
	go func() { _, err := acquireTokens(context.Background(), "/db"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EMFILE blocked the call")
	}
}

func TestBrConcurrencyWaitCapRunsTheCall(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(HeldEnv, "")
	t.Setenv(PerDBEnv, "1")
	t.Setenv(WaitMaxEnv, "200ms")
	hold, err := acquireTokens(context.Background(), "/db")
	if err != nil || len(hold.files) != 2 {
		t.Fatalf("hold: %v %v", hold, err)
	}
	defer hold.release()
	start := time.Now()
	tk, err := acquireTokens(context.Background(), "/db")
	if err != nil || len(tk.files) != 0 {
		t.Fatalf("want fail-open tokens, got %v %v", tk, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("waited %s", time.Since(start))
	}
}

func TestBrConcurrencyQueueWaitCap(t *testing.T) {
	old := brLimiter
	brLimiter = testLimiter(1, 1)
	defer func() { brLimiter = old }()
	t.Setenv(HeldEnv, "1")
	t.Setenv(WaitMaxEnv, "100ms")
	adir := t.TempDir()
	hold, _ := brLimiter.acquire(context.Background(), limiterKey(adir), false)
	defer hold()
	ran := false
	err := limitedRun(context.Background(), adir, []string{"show", "x"}, func() error { ran = true; return nil })
	if err == nil || ran || !strings.Contains(err.Error(), "br queue wait exceeded") {
		t.Fatalf("read: ran=%v err=%v", ran, err)
	}
	err = limitedRun(context.Background(), adir, []string{"update", "x"}, func() error { ran = true; return nil })
	if err != nil || !ran {
		t.Fatalf("write: ran=%v err=%v", ran, err)
	}
}

func TestBrConcurrencyHeldEnvPassedToChild(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(HeldEnv, "")
	script := filepath.Join(t.TempDir(), "br")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"held=$"+HeldEnv+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, _, err := defaultRunInDir(context.Background(), t.TempDir(), script, "show", "x")
	if err != nil || strings.TrimSpace(string(out)) != "held=1" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBrConcurrencySymlinkedDirSharesKey(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if limiterKey(link) != limiterKey(real) {
		t.Fatalf("%q != %q", limiterKey(link), limiterKey(real))
	}
}

// holdTokens takes n shared tokens of db as readers do.
func holdShared(t *testing.T, key string, n int) []*tokens {
	t.Helper()
	var out []*tokens
	for i := 0; i < n; i++ {
		tk, err := acquireTokens(context.Background(), key, false)
		if err != nil || len(tk.files) != 2 {
			t.Fatalf("reader %d: %v %v", i, tk, err)
		}
		out = append(out, tk)
	}
	return out
}

func TestReservedTokenWriteRunsWhileReadersHoldTheSharedOnes(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "2")
	t.Setenv(GlobalEnv, "6")
	readers := holdShared(t, "/db", 2)
	defer func() {
		for _, r := range readers {
			r.release()
		}
	}()
	// a third reader is refused, a write is not
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if r, err := acquireTokens(ctx, "/db", false); err == nil {
		r.release()
		t.Fatal("a third reader took the reserved token")
	}
	start := time.Now()
	w, err := acquireTokens(context.Background(), "/db", true)
	if err != nil || len(w.files) != 2 || time.Since(start) > time.Second {
		t.Fatalf("write: %v %v after %s", w, err, time.Since(start))
	}
	// only one write at a time on that reserved token (both shared are held)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if w2, err := acquireTokens(ctx2, "/db", true); err == nil {
		w2.release()
		t.Fatal("a second write passed with every db token held")
	}
	w.release()
}

func TestReservedTokenReadsNeverTakeIt(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "1")
	t.Setenv(GlobalEnv, "3")
	for _, n := range dbTokenNames("/db", 1, false) {
		if strings.HasSuffix(n, ".w") {
			t.Fatalf("a read may try %s", n)
		}
	}
	if got := dbTokenNames("/db", 1, true); !strings.HasSuffix(got[0], ".w") {
		t.Fatalf("a write tries the reserved token first: %v", got)
	}
	if g := globalTokenNames(3, false); len(g) != 2 || g[1] != "global.1" {
		t.Fatalf("reads keep the last global token free: %v", g)
	}
	if g := globalTokenNames(3, true); g[0] != "global.2" {
		t.Fatalf("a write tries the last global token first: %v", g)
	}
}

func TestReservedTokenWriteUsesTheLastGlobalWhenReadersFillTheRest(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "4")
	t.Setenv(GlobalEnv, "3")
	a, _ := acquireTokens(context.Background(), "/a", false)
	b, _ := acquireTokens(context.Background(), "/b", false)
	defer a.release()
	defer b.release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if c, err := acquireTokens(ctx, "/c", false); err == nil {
		c.release()
		t.Fatal("a read took the write-reserved global token")
	}
	w, err := acquireTokens(context.Background(), "/c", true)
	if err != nil || len(w.files) != 2 {
		t.Fatalf("write: %v %v", w, err)
	}
	w.release()
}

func TestReservedTokenInProcessWriteJumpsFullReaders(t *testing.T) {
	l := testLimiter(1, 6)
	r1, _ := l.acquire(context.Background(), "/db", false)
	defer r1()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if rel, err := l.acquire(ctx, "/db", false); err == nil {
		rel()
		t.Fatal("second read got past per-db 1")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	w, err := l.acquire(ctx2, "/db", true)
	if err != nil {
		t.Fatalf("write has the reserved slot: %v", err)
	}
	w()
}

func TestWritePriorityDisplayReadsYieldWhileAWriteWaits(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(PerDBEnv, "1")
	t.Setenv(GlobalEnv, "1")
	old := yieldPause
	yieldPause = func() time.Duration { return 10 * time.Millisecond }
	defer func() { yieldPause = old }()

	hold, _ := acquireTokens(context.Background(), "/db", true) // takes the only tokens
	wctx, wcancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = acquireTokens(wctx, "/db", true); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for WritesWaiting("/db") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if WritesWaiting("/db") == 0 {
		t.Fatal("a waiting write left no marker")
	}
	yielded := make(chan struct{})
	go func() { YieldToWrites(context.Background(), "/db"); close(yielded) }()
	select {
	case <-yielded:
		t.Fatal("a display read went on while a write waited")
	case <-time.After(150 * time.Millisecond):
	}
	wcancel()
	<-done
	hold.release()
	select {
	case <-yielded:
	case <-time.After(3 * time.Second):
		t.Fatal("the display read never resumed once the write left")
	}
}

func TestWritePriorityMarkerOfADeadWaiterGoesStale(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	m := newWaitMarker("/db")
	m.touch()
	if WritesWaiting("/db") != 1 {
		t.Fatal("fresh marker not seen")
	}
	old := time.Now().Add(-5 * time.Second)
	_ = os.Chtimes(m.path, old, old)
	if WritesWaiting("/db") != 0 {
		t.Fatal("a stale marker still counts")
	}
}

func TestWritePriorityBusyErrorIsFriendly(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	old := brLimiter
	brLimiter = testLimiter(1, 2)
	defer func() { brLimiter = old }()
	adir := t.TempDir()
	hold, _ := brLimiter.acquire(context.Background(), limiterKey(adir), true)
	defer hold()
	// a write needs both reserved and shared taken, so hold the second slot too
	hold2, _ := brLimiter.acquire(context.Background(), limiterKey(adir), true)
	defer hold2()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := limitedRun(ctx, adir, []string{"create", "x"}, func() error { return nil })
	if err == nil || !IsBusy(err) || !strings.Contains(err.Error(), "retry in a minute") || strings.Contains(err.Error(), "br queue:") {
		t.Fatalf("err = %v", err)
	}
}

func resetWriteSeen(key string) {
	writeSeenMu.Lock()
	delete(writeSeen, key)
	writeSeenMu.Unlock()
}

// fakeYield records the sleeps instead of taking them.
func fakeYield(t *testing.T) *time.Duration {
	t.Helper()
	var slept time.Duration
	oldP, oldS := yieldPause, yieldSleep
	yieldPause = func() time.Duration { return 300 * time.Millisecond }
	yieldSleep = func(ctx context.Context, d time.Duration) { slept += d }
	t.Cleanup(func() { yieldPause, yieldSleep = oldP, oldS })
	return &slept
}

func TestYieldIdleWriteDoesNotMakeReadsYield(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	slept := fakeYield(t)
	old := brLimiter
	brLimiter = testLimiter(2, 4)
	defer func() { brLimiter = old }()
	resetWriteSeen(limiterKey("/idle"))
	rel, err := brLimiter.acquire(context.Background(), limiterKey("/idle"), true)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	YieldToWrites(WithYieldBudget(context.Background(), YieldBudget), "/idle")
	if *slept != 0 {
		t.Fatalf("a write that never waited made a read yield %s", *slept)
	}
}

func TestYieldContendedWriteIsCappedPerCall(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	slept := fakeYield(t)
	key := limiterKey("/busy")
	noteWriteSeen(key)
	t.Cleanup(func() { resetWriteSeen(key) })
	ctx := WithYieldBudget(context.Background(), YieldBudget)
	YieldToWrites(ctx, "/busy")
	YieldToWrites(ctx, "/busy") // later batch of the same Details call
	if *slept == 0 || *slept > YieldBudget {
		t.Fatalf("yielded %s; want 0 < t <= %s", *slept, YieldBudget)
	}
}

func TestYieldSteadyWritesStayBounded(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	slept := fakeYield(t)
	key := limiterKey("/steady")
	t.Cleanup(func() { resetWriteSeen(key) })
	ctx := WithYieldBudget(context.Background(), YieldBudget)
	for batch := 0; batch < 7; batch++ {
		noteWriteSeen(key) // a fresh contended write before every batch
		YieldToWrites(ctx, "/steady")
	}
	if *slept > YieldBudget {
		t.Fatalf("steady writes yielded %s over 7 batches; cap %s", *slept, YieldBudget)
	}
}

func TestWritePriorityMarkerRemovedAfterAMinute(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	m := newWaitMarker("/db")
	m.touch()
	old := time.Now().Add(-2 * time.Minute)
	_ = os.Chtimes(m.path, old, old)
	WritesWaiting("/db")
	if _, err := os.Stat(m.path); err == nil {
		t.Fatal("a marker older than a minute was not removed")
	}
	m2 := newWaitMarker("/db")
	m2.touch()
	st := time.Now().Add(-5 * time.Second)
	_ = os.Chtimes(m2.path, st, st)
	WritesWaiting("/db")
	if _, err := os.Stat(m2.path); err != nil {
		t.Fatal("a 5s-old marker must stay until a minute passes")
	}
}
