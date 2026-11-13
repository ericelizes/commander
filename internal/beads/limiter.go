package beads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/config"
)

// Every br process of a project reads and writes one SQLite db, so many at once
// only fight for its lock and for CPU; each then outlives its timeout, is
// killed and retried, and the pile grows. The limiter bounds them:
//
//   - in this process: per db dir at most brPerDB() calls and brGlobal() overall,
//     FIFO per priority, writes before reads, a cancelled waiter leaves the line;
//   - across processes (cmdrd, each cmdr call, Operators): the same per-db and
//     global counts as flock(2) tokens under $COMMANDER_HOME/br-slots, freed by
//     the kernel when the holder exits or dies.
//
// Neither wait can wedge the fleet: after brWaitMax() (3 minutes) a call stops
// waiting for cross-process tokens and runs with the in-process slot only; a
// token dir that cannot be used fails open the same way. The in-process queue
// has the same ceiling for callers without a deadline: a write then runs
// without a slot (a wedged queue must not stop writes), a read fails with
// "br queue wait exceeded". Tokens are not FIFO and writes-first holds in
// process only, so reads may starve under constant writes from another process;
// the ceiling bounds that.
//
// Writes have a reserved token: per db the tokens are brPerDB() shared ones
// (reads and writes) plus one "w" token only writes take, and of the global
// tokens the last is for writes only. A write waiting for a token (or in line
// in process) also leaves a marker file; display reads (YieldToWrites) back off
// while one is fresh, so cmdrd's warming first pass cannot starve a Manager's
// write across processes.
//
// The per-call timeout starts when the call runs, never while it waits.
const (
	// PerDBEnv overrides the concurrent br processes per project db ([concurrency] br_per_db).
	PerDBEnv = "COMMANDER_BR_PER_DB"
	// GlobalEnv overrides the concurrent br processes overall ([concurrency] br_global).
	GlobalEnv = "COMMANDER_BR_GLOBAL"
	// HeldEnv marks a process tree whose ancestor holds a token: it skips the
	// cross-process tokens so a nested call cannot wait on its own parent.
	HeldEnv = "COMMANDER_BR_HELD"
	// WaitMaxEnv overrides the longest a call waits for a slot or token before it
	// runs anyway (a Go duration; tests use a tiny one).
	WaitMaxEnv = "COMMANDER_BR_WAIT_MAX"

	defaultWaitMax = 3 * time.Minute
	failOpenNotice = time.Minute

	defaultPerDB  = 2
	defaultGlobal = 6

	queueNotice  = 5 * time.Second
	noticeEvery  = 30 * time.Second
	tokenPollMin = 25 * time.Millisecond
	tokenPollMax = 50 * time.Millisecond
)

func envPositive(name string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && n > 0 {
		return n
	}
	return 0
}

func brWaitMax() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(WaitMaxEnv))); err == nil && d > 0 {
		return d
	}
	return defaultWaitMax
}

var (
	failOpenMu  sync.Mutex
	failOpenLog time.Time
)

// noteFailOpen logs why a call runs without cross-process tokens, at most once a minute.
func noteFailOpen(format string, args ...any) {
	failOpenMu.Lock()
	defer failOpenMu.Unlock()
	if time.Since(failOpenLog) < failOpenNotice {
		return
	}
	failOpenLog = time.Now()
	log.Printf("beads: "+format, args...)
}

func brHome() string {
	if h := os.Getenv("COMMANDER_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".commander")
}

var (
	cfgMu      sync.Mutex
	cfgAt      time.Time
	cfgPerDB   int
	cfgGlobal  int
	cfgHomeKey string
)

// configured reads [concurrency] br_per_db / br_global, cached for 30s.
func configured() (perDB, global int) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	home := brHome()
	if cfgHomeKey == home && time.Since(cfgAt) < 30*time.Second {
		return cfgPerDB, cfgGlobal
	}
	cfgHomeKey, cfgAt, cfgPerDB, cfgGlobal = home, time.Now(), 0, 0
	path := filepath.Join(home, "config.toml")
	if _, err := os.Stat(path); err == nil {
		if cfg, err := config.Load(path); err == nil {
			cfgPerDB, cfgGlobal = cfg.Concurrency.BrPerDB, cfg.Concurrency.BrGlobal
		}
	}
	return cfgPerDB, cfgGlobal
}

func brPerDB() int {
	if n := envPositive(PerDBEnv); n > 0 {
		return n
	}
	if n, _ := configured(); n > 0 {
		return n
	}
	return defaultPerDB
}

func brGlobal() int {
	if n := envPositive(GlobalEnv); n > 0 {
		return n
	}
	if _, n := configured(); n > 0 {
		return n
	}
	return defaultGlobal
}

// sharedCaps are the slots reads may use: a write always has one more per db
// (the reserved token) and, with more than one global slot, one more overall,
// so a write is runnable while reads hold everything else.
func sharedCaps(perDB, global int) (readDB, readGlobal int) {
	readDB, readGlobal = perDB, global
	if global > 1 {
		readGlobal = global - 1
	}
	return readDB, readGlobal
}

// waiter is one queued call.
type waiter struct {
	key   string
	grant chan struct{}
	gone  bool
}

// limiter is the in-process scheduler.
type limiter struct {
	mu      sync.Mutex
	perDB   func() int
	global  func() int
	running map[string]int
	total   int
	reads   []*waiter
	writes  []*waiter
	noticed time.Time
}

func newLimiter(perDB, global func() int) *limiter {
	return &limiter{perDB: perDB, global: global, running: map[string]int{}}
}

// brLimiter is the process-wide limiter; tests swap it.
var brLimiter = newLimiter(brPerDB, brGlobal)

// depth is how many calls of key wait in line.
func (l *limiter) depth(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, q := range [][]*waiter{l.writes, l.reads} {
		for _, w := range q {
			if w.key == key && !w.gone {
				n++
			}
		}
	}
	return n
}

// dispatchLocked grants every waiter that fits, writes first, FIFO in each queue.
func (l *limiter) dispatchLocked() {
	perDB, global := l.perDB(), l.global()
	readDB, readGlobal := sharedCaps(perDB, global)
	for qi, q := range []*[]*waiter{&l.writes, &l.reads} {
		write := qi == 0
		rest := (*q)[:0]
		for _, w := range *q {
			fits := l.total < readGlobal && l.running[w.key] < readDB
			if write {
				fits = l.total < global && l.running[w.key] < perDB+1
			}
			switch {
			case w.gone:
			case fits:
				l.running[w.key]++
				l.total++
				close(w.grant)
			default:
				rest = append(rest, w)
			}
		}
		*q = rest
	}
}

func (l *limiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running[key]--
	if l.running[key] <= 0 {
		delete(l.running, key)
	}
	l.total--
	l.dispatchLocked()
}

// acquire waits in line for a slot of key. The returned func frees it. A
// cancelled ctx leaves the line.
func (l *limiter) acquire(ctx context.Context, key string, write bool) (func(), error) {
	w := &waiter{key: key, grant: make(chan struct{})}
	start := time.Now()
	l.mu.Lock()
	if write {
		l.writes = append(l.writes, w)
	} else {
		l.reads = append(l.reads, w)
	}
	l.dispatchLocked()
	if write {
		// Only a write that had to wait is contention worth yielding to.
		select {
		case <-w.grant:
		default:
			defer noteWriteSeen(key)
		}
	}
	l.mu.Unlock()

	notice := time.NewTimer(queueNotice)
	defer notice.Stop()
	for {
		select {
		case <-w.grant:
			return func() { l.release(key) }, nil
		case <-notice.C:
			l.noteWait(key, time.Since(start))
			notice.Reset(noticeEvery)
		case <-ctx.Done():
			l.mu.Lock()
			select {
			case <-w.grant: // granted as we gave up: hand the slot back
				l.mu.Unlock()
				l.release(key)
			default:
				w.gone = true
				l.dispatchLocked()
				l.mu.Unlock()
			}
			return nil, ctx.Err()
		}
	}
}

// noteWait logs a long queue wait, at most once per noticeEvery.
func (l *limiter) noteWait(key string, waited time.Duration) {
	l.mu.Lock()
	if time.Since(l.noticed) < noticeEvery {
		l.mu.Unlock()
		return
	}
	l.noticed = time.Now()
	running, total := l.running[key], l.total
	l.mu.Unlock()
	log.Printf("beads: br call for %s queued %s (%d waiting, %d running there, %d overall)",
		key, waited.Round(time.Second), l.depth(key), running, total)
}

// QueueDepth is how many br calls of the project dir wait in this process.
func QueueDepth(dir string) int { return brLimiter.depth(limiterKey(dir)) }

// limiterKey names a project db: its dir with symlinks resolved, so /tmp and
// /private/tmp are one key (the cwd for ""; Clean when it cannot be resolved).
func limiterKey(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(dir)
}

// isWrite tells a br write (priority queue) from a read by argv.
func isWrite(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "retire", "update", "close", "create", "reopen", "defer", "undefer", "delete", "label", "init", "rename", "batch", "set-state", "state", "sync", "export":
		return true
	case "dep", "comments", "comment":
		return len(args) > 1 && (args[1] == "add" || args[1] == "remove" || args[1] == "rm")
	}
	return false
}

// tokens are the cross-process flock(2) files.
type tokens struct {
	files []*os.File
}

func (t *tokens) release() {
	for _, f := range t.files {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	t.files = nil
}

func tokenDir() string { return filepath.Join(brHome(), "br-slots") }

func tokenName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// openToken opens a token file; tests swap it to inject failures.
var openToken = func(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

// tryToken takes the first free of the named token files without blocking.
// Only a flock that would block means busy; any other error is returned.
func tryToken(names []string) (f *os.File, busy bool, err error) {
	for _, name := range names {
		f, err := openToken(filepath.Join(tokenDir(), name))
		if err != nil {
			return nil, false, err
		}
		switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err {
		case nil:
			return f, false, nil
		case syscall.EWOULDBLOCK: // EAGAIN is the same value
			f.Close()
		default:
			f.Close()
			return nil, false, err
		}
	}
	return nil, true, nil
}

func tokenNames(prefix string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("%s.%d", prefix, i))
	}
	return out
}

// dbTokenNames: reads the shared ones; a write the reserved one first, then the shared.
func dbTokenNames(key string, perDB int, write bool) []string {
	prefix := tokenName(key)
	shared := tokenNames(prefix, 0, perDB)
	if !write {
		return shared
	}
	return append([]string{prefix + ".w"}, shared...)
}

// globalTokenNames: reads all but the last; a write the last first, then the rest.
func globalTokenNames(global int, write bool) []string {
	readG := global
	if global > 1 {
		readG = global - 1
	}
	rest := tokenNames("global", 0, readG)
	if !write || global <= 1 {
		return rest
	}
	return append([]string{fmt.Sprintf("global.%d", global-1)}, rest...)
}

// waitMarker is a write's "I am waiting" file for a db, kept fresh while it waits.
type waitMarker struct{ path string }

const markerFresh = time.Second

func markerPrefix(key string) string { return tokenName(key) + ".waiting." }

func newWaitMarker(key string) *waitMarker {
	if os.Getenv(HeldEnv) == "1" || brHome() == "" {
		return nil
	}
	if os.MkdirAll(tokenDir(), 0o755) != nil {
		return nil
	}
	m := &waitMarker{path: filepath.Join(tokenDir(), fmt.Sprintf("%s%d.%d", markerPrefix(key), os.Getpid(), time.Now().UnixNano()))}
	m.touch()
	return m
}

func (m *waitMarker) touch() {
	if m == nil {
		return
	}
	now := time.Now()
	if os.Chtimes(m.path, now, now) != nil {
		if f, err := os.Create(m.path); err == nil {
			f.Close()
		}
	}
}

func (m *waitMarker) remove() {
	if m != nil {
		os.Remove(m.path)
	}
}

// WritesWaiting is how many writes of the project db wait right now, in this
// process or another (fresh marker files); stale markers of a dead waiter are
// ignored and removed after a minute.
func WritesWaiting(dir string) int {
	key := limiterKey(dir)
	n := brLimiter.writesWaiting(key)
	if brHome() == "" {
		return n
	}
	matches, _ := filepath.Glob(filepath.Join(tokenDir(), markerPrefix(key)+"*"))
	for _, p := range matches {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		age := time.Since(st.ModTime())
		switch {
		case age < markerFresh:
			n++
		case age > time.Minute:
			os.Remove(p)
		}
	}
	return n
}

func (l *limiter) writesWaiting(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, w := range l.writes {
		if w.key == key && !w.gone {
			n++
		}
	}
	return n
}

// writeLastSeen remembers when a write last waited per db, so reads keep
// yielding for a moment after the write got its slot.
var (
	writeSeenMu sync.Mutex
	writeSeen   = map[string]time.Time{}
)

func noteWriteSeen(key string) {
	writeSeenMu.Lock()
	writeSeen[key] = time.Now()
	writeSeenMu.Unlock()
}

// yieldPause is the display reads' back-off between batches while writes wait.
var yieldPause = func() time.Duration {
	return 200*time.Millisecond + time.Duration(time.Now().UnixNano()%int64(300*time.Millisecond))
}

// yieldSleep waits d or until ctx ends (a seam for tests).
var yieldSleep = func(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// YieldBudget is the most one display read (a Details call) yields in total, so
// steady writes can never throttle a long batch.
const YieldBudget = 1500 * time.Millisecond

type yieldBudgetKey struct{}

type yieldBudget struct {
	mu   sync.Mutex
	left time.Duration
}

// WithYieldBudget gives the display read under ctx a total yield allowance.
func WithYieldBudget(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, yieldBudgetKey{}, &yieldBudget{left: d})
}

// take reserves up to d of the allowance and returns what it got.
func (b *yieldBudget) take(d time.Duration) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d > b.left {
		d = b.left
	}
	b.left -= d
	return d
}

// YieldToWrites blocks a display read for 200-500ms (a few rounds at most)
// while a write of the project waits or waited (had to wait for a token) in the
// last second. A ctx from WithYieldBudget caps the total across calls.
func YieldToWrites(ctx context.Context, dir string) {
	key := limiterKey(dir)
	budget, _ := ctx.Value(yieldBudgetKey{}).(*yieldBudget)
	for i := 0; i < 20; i++ {
		writeSeenMu.Lock()
		recent := time.Since(writeSeen[key]) < markerFresh
		writeSeenMu.Unlock()
		if !recent && WritesWaiting(dir) == 0 {
			return
		}
		d := yieldPause()
		if budget != nil {
			if d = budget.take(d); d <= 0 {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		yieldSleep(ctx, d)
	}
}

// acquireTokens takes one per-db and one global token together: a holder of one
// never waits on the other, so processes cannot deadlock each other. A write may
// also take the reserved tokens. A token dir or file that cannot be used, or a
// wait past brWaitMax(), lets the call run with the in-process limit only.
func acquireTokens(ctx context.Context, key string, write ...bool) (*tokens, error) {
	if os.Getenv(HeldEnv) == "1" || brHome() == "" {
		return &tokens{}, nil
	}
	if err := os.MkdirAll(tokenDir(), 0o755); err != nil {
		noteFailOpen("br tokens unusable (%v); running with the in-process limit only", err)
		return &tokens{}, nil
	}
	isW := len(write) > 0 && write[0]
	perDB, global := brPerDB(), brGlobal()
	deadline := time.Now().Add(brWaitMax())
	var marker *waitMarker
	waitStart := time.Now()
	logged := false
	defer func() { marker.remove() }()
	for {
		db, _, err := tryToken(dbTokenNames(key, perDB, isW))
		if err == nil && db != nil {
			g, _, gerr := tryToken(globalTokenNames(global, isW))
			if gerr == nil && g != nil {
				stampHolder(db)
				return &tokens{files: []*os.File{db, g}}, nil
			}
			(&tokens{files: []*os.File{db}}).release()
			err = gerr
		}
		if err != nil {
			noteFailOpen("br tokens unusable (%v); running with the in-process limit only", err)
			return &tokens{}, nil
		}
		if isW {
			if marker == nil {
				marker = newWaitMarker(key)
			}
			marker.touch()
			noteWriteSeen(key)
			if !logged && time.Since(waitStart) > queueNotice {
				logged = true
				noteWait("br write for %s waited %s for a token (%s)", key, time.Since(waitStart).Round(time.Second), holderHint(key))
			}
		}
		if !time.Now().Before(deadline) {
			noteFailOpen("br token wait for %s exceeded %s; running with the in-process limit only", key, brWaitMax())
			return &tokens{}, nil
		}
		wait := tokenPollMin + time.Duration(time.Now().UnixNano()%int64(tokenPollMax-tokenPollMin))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

var (
	noteWaitMu  sync.Mutex
	noteWaitLog time.Time
)

// noteWait logs a long write wait, at most once per noticeEvery.
func noteWait(format string, args ...any) {
	noteWaitMu.Lock()
	defer noteWaitMu.Unlock()
	if time.Since(noteWaitLog) < noticeEvery {
		return
	}
	noteWaitLog = time.Now()
	log.Printf("beads: "+format, args...)
}

// stampHolder records pid and command in a token file just taken, so a
// waiter can say who holds the others.
func stampHolder(f *os.File) {
	exe, _ := os.Executable()
	args := strings.Join(os.Args[1:min(len(os.Args), 4)], " ")
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d %s %s\n", os.Getpid(), filepath.Base(exe), args)), 0)
}

// holderHint names who holds the db's tokens, read from the stamps in the
// token files (a stale stamp of a released token is skipped by trying the lock).
func holderHint(key string) string {
	prefix := tokenName(key)
	var out []string
	for _, name := range append(tokenNames(prefix, 0, brPerDB()), prefix+".w") {
		path := filepath.Join(tokenDir(), name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
			f.Close() // free: the stamp is old
			continue
		}
		b := make([]byte, 200)
		n, _ := f.Read(b)
		f.Close()
		if t := strings.TrimSpace(string(b[:n])); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return "holder unknown"
	}
	return "held by " + strings.Join(out, "; ")
}

// limitedRun runs fn once a slot (in process, then across processes) is free.
// fn gets the context to run with; its deadline must be set by fn so that
// queueing time is not counted.
func limitedRun(ctx context.Context, dir string, args []string, fn func() error) error {
	key := limiterKey(dir)
	write := isWrite(args)
	qctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		qctx, cancel = context.WithTimeout(ctx, brWaitMax())
		defer cancel()
	}
	release, err := brLimiter.acquire(qctx, key, write)
	switch {
	case err == nil:
		defer release()
	case ctx.Err() == nil && qctx.Err() != nil && write:
		noteFailOpen("br queue wait for %s exceeded %s; running the write without a slot", key, brWaitMax())
	case ctx.Err() == nil && qctx.Err() != nil:
		return fmt.Errorf("br queue wait exceeded (%s): %w", brWaitMax(), ErrBusy)
	default:
		return busyError(key, write, err)
	}
	toks, err := acquireTokens(ctx, key, write)
	if err != nil {
		return busyError(key, write, err)
	}
	defer toks.release()
	return fn()
}

// ErrBusy marks a br call that never ran because the queue or tokens stayed
// taken: nothing was done, so repeating it is safe.
var ErrBusy = errors.New("br is busy")

// busyError turns a cancelled or timed-out queue wait into a message that says
// what happened and what to do; other errors keep their text.
func busyError(key string, write bool, err error) error {
	if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("br queue: %w", err)
	}
	waiting := brLimiter.depth(key)
	kind := "read"
	if write {
		kind = "write"
	}
	return fmt.Errorf("br is busy (%d waiting, %s not started); retry in a minute: %w", waiting, kind, errors.Join(ErrBusy, err))
}

// IsBusy reports a call that was never started because br stayed busy.
func IsBusy(err error) bool { return errors.Is(err, ErrBusy) }
