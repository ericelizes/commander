// Package slot is the machine's pool of build slots: N kernel flock(2)s on
// $COMMANDER_HOME/build.lock (slot 0) and build.lock.<i> (slots 1..N-1) that
// bound how many full builds and test suites run at once across every Operator
// and `cmdr internal-ship`. The kernel drops a lock when its holder exits or
// dies, so a crashed holder never wedges the line.
//
// Waiting is fair across workstreams: while a workstream holding fewer slots
// waits, one holding more does not try, so a workstream that already holds a
// slot waits behind one that holds none.
package slot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/config"
)

// HeldEnv marks a process tree that runs inside the slot, so a nested Acquire
// (`slot run -- cmdr internal-ship`) does not wait on its own parent.
const HeldEnv = "COMMANDER_SLOT_HELD"

// CountEnv overrides the number of slots ([concurrency] build_slots).
const CountEnv = "COMMANDER_BUILD_SLOTS"

// managerRoleEnv is set on every host Operator (spawn.hostOperatorCommand):
// manager-<workstream bead>, which names the workstream a caller works for.
const managerRoleEnv = "COMMANDER_MANAGER_ROLE"

const (
	lockName   = "build.lock"
	waitingDir = "build-slots-waiting"
)

// Options tune Acquire. Zero values give the production behaviour.
type Options struct {
	Cmd        string        // what the holder runs, shown to those waiting
	Out        io.Writer     // "waiting" notices; nil discards
	Notice     time.Duration // notice interval (default 30s)
	PollWait   time.Duration // lock retry interval (default 200ms)
	Slots      int           // pool size (default Count(home))
	Workstream string        // fairness key (default WorkstreamFromEnv())
}

// Holder is a held slot.
type Holder struct {
	file *os.File
	info string
}

// Info describes one held slot, from its info file.
type Info struct {
	Slot       int    `json:"slot"`
	PID        int    `json:"pid"`
	Cmd        string `json:"cmd"`
	Workstream string `json:"workstream"`
	Dir        string `json:"dir"`
	Since      string `json:"since"`
}

func (i Info) String() string {
	s := fmt.Sprintf("%d %s since %s", i.PID, i.Cmd, i.Since)
	if i.Workstream != "" {
		s += " (" + i.Workstream + ")"
	}
	return s
}

// DefaultCount is max(2, cores/2).
func DefaultCount() int { return max(2, runtime.NumCPU()/2) }

// Count is the pool size: $COMMANDER_BUILD_SLOTS, else [concurrency]
// build_slots in <home>/config.toml, else DefaultCount. It is read at every
// Acquire, so a change applies to the next one.
func Count(home string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(CountEnv))); err == nil && n > 0 {
		return n
	}
	path := filepath.Join(home, "config.toml")
	if _, err := os.Stat(path); err == nil {
		if cfg, err := config.Load(path); err == nil && cfg.Concurrency.BuildSlots > 0 {
			return cfg.Concurrency.BuildSlots
		}
	}
	return DefaultCount()
}

// WorkstreamFromEnv is the caller's workstream bead id ("" when not an Operator).
func WorkstreamFromEnv() string {
	return strings.TrimPrefix(os.Getenv(managerRoleEnv), "manager-")
}

func lockPath(home string, i int) string {
	if i == 0 {
		return filepath.Join(home, lockName)
	}
	return filepath.Join(home, fmt.Sprintf("%s.%d", lockName, i))
}

func infoPath(home string, i int) string { return lockPath(home, i) + ".info" }

var waiterSeq atomic.Int64

// Acquire waits in line for a slot (or ctx ending) and returns the holder.
func Acquire(ctx context.Context, home string, opts Options) (*Holder, error) {
	if os.Getenv(HeldEnv) == "1" { // an ancestor already holds one: flock is not reentrant
		return &Holder{}, nil
	}
	if opts.Notice <= 0 {
		opts.Notice = 30 * time.Second
	}
	if opts.PollWait <= 0 {
		opts.PollWait = 200 * time.Millisecond
	}
	if opts.Slots <= 0 {
		opts.Slots = Count(home)
	}
	if opts.Workstream == "" {
		opts.Workstream = WorkstreamFromEnv()
	}
	if err := os.MkdirAll(filepath.Join(home, waitingDir), 0o755); err != nil {
		return nil, err
	}
	waiter := filepath.Join(home, waitingDir, fmt.Sprintf("%d-%d", os.Getpid(), waiterSeq.Add(1)))
	if err := os.WriteFile(waiter, []byte(opts.Workstream), 0o644); err != nil {
		return nil, err
	}
	defer os.Remove(waiter)

	nextNotice := time.Now().Add(opts.Notice)
	for {
		holders := Holders(home, opts.Slots)
		if !yields(home, waiter, opts.Workstream, holders) {
			h, err := tryAll(home, opts)
			if h != nil || err != nil {
				return h, err
			}
		}
		if opts.Out != nil && !time.Now().Before(nextNotice) {
			fmt.Fprintf(opts.Out, "waiting for a build slot (%d/%d held: %s)\n", len(holders), opts.Slots, describe(holders))
			nextNotice = time.Now().Add(opts.Notice)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("build slot: %w", ctx.Err())
		case <-time.After(opts.PollWait):
		}
	}
}

// tryAll takes the first free slot, or returns (nil, nil) when all are held.
func tryAll(home string, opts Options) (*Holder, error) {
	for i := 0; i < opts.Slots; i++ {
		f, err := os.OpenFile(lockPath(home, i), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			cmd := opts.Cmd
			if cmd == "" {
				cmd = "?"
			}
			dir, _ := os.Getwd()
			info := Info{Slot: i, PID: os.Getpid(), Cmd: cmd, Workstream: opts.Workstream, Dir: dir, Since: time.Now().Format("15:04:05")}
			raw, _ := json.Marshal(info)
			// Line 1 is what an older build shows to those waiting on build.lock.
			_ = os.WriteFile(infoPath(home, i), []byte(info.String()+"\n"+string(raw)+"\n"), 0o644)
			return &Holder{file: f, info: infoPath(home, i)}, nil
		}
		f.Close()
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			return nil, err
		}
	}
	return nil, nil
}

// yields reports whether a waiter of workstream ws should let another live
// waiter go first: one whose workstream holds fewer slots than ws does.
func yields(home, self, ws string, holders []Info) bool {
	held := map[string]int{}
	for _, h := range holders {
		held[h.Workstream]++
	}
	if held[ws] == 0 {
		return false
	}
	entries, _ := os.ReadDir(filepath.Join(home, waitingDir))
	for _, e := range entries {
		path := filepath.Join(home, waitingDir, e.Name())
		if path == self {
			continue
		}
		var pid int
		fmt.Sscanf(e.Name(), "%d-", &pid)
		if !alive(pid) {
			_ = os.Remove(path)
			continue
		}
		other, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if held[string(other)] < held[ws] {
			return true
		}
	}
	return false
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Release frees the slot. Safe to call twice.
func (h *Holder) Release() {
	if h == nil || h.file == nil {
		return
	}
	_ = os.Remove(h.info)
	_ = syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	h.file.Close()
	h.file = nil
}

// Holders lists the live holders of slots 0..n-1 (n <= 0: Count(home)). A
// holder is live while its slot's lock is actually taken, so info a crashed
// holder left behind is ignored.
func Holders(home string, n int) []Info {
	if n <= 0 {
		n = Count(home)
	}
	var out []Info
	for i := 0; i < n; i++ {
		raw, err := os.ReadFile(infoPath(home, i))
		if err != nil {
			continue
		}
		info := parseInfo(i, string(raw))
		if alive(info.PID) && locked(home, i) {
			out = append(out, info)
		}
	}
	return out
}

// parseInfo reads an info file: a display line, then (from this build on) JSON.
func parseInfo(i int, raw string) Info {
	lines := strings.SplitN(strings.TrimSpace(raw), "\n", 2)
	var info Info
	if len(lines) == 2 && json.Unmarshal([]byte(lines[1]), &info) == nil {
		info.Slot = i
		return info
	}
	info = Info{Slot: i, Cmd: lines[0]}
	fmt.Sscanf(lines[0], "%d", &info.PID)
	if f := strings.SplitN(lines[0], " ", 2); len(f) == 2 {
		info.Cmd = f[1]
	}
	return info
}

// locked reports whether slot i's lock is held, by probing it.
func locked(home string, i int) bool {
	f, err := os.OpenFile(lockPath(home, i), os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return true
}

func describe(holders []Info) string {
	if len(holders) == 0 {
		return "none"
	}
	parts := make([]string, len(holders))
	for i, h := range holders {
		parts[i] = h.String()
	}
	return strings.Join(parts, "; ")
}

// Run takes a slot, runs argv in the current directory with stdio passed
// through, and releases the slot when the child exits. timeout (0 = none)
// bounds the whole thing, queueing included. It returns the child's exit code.
func Run(ctx context.Context, home string, argv []string, timeout time.Duration, out, errOut io.Writer) (int, error) {
	return RunEnv(ctx, home, argv, timeout, out, errOut, nil)
}

// RunEnv is Run with the child's environment; nil = this process's.
func RunEnv(ctx context.Context, home string, argv []string, timeout time.Duration, out, errOut io.Writer, env []string) (int, error) {
	if len(argv) == 0 {
		return 2, fmt.Errorf("no command")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	h, err := Acquire(ctx, home, Options{Cmd: strings.Join(argv, " "), Out: errOut})
	if err != nil {
		return 1, err
	}
	defer h.Release()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env, HeldEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, errOut
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
			return ee.ExitCode(), nil
		}
		if ctx.Err() != nil {
			return 124, fmt.Errorf("%s timed out after %s", argv[0], timeout)
		}
		return 127, err
	}
	return 0, nil
}

// Status describes the pool: "free" when nobody holds a slot.
func Status(home string) string {
	n := Count(home)
	holders := Holders(home, n)
	if len(holders) == 0 {
		return fmt.Sprintf("free (0/%d slots held)", n)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d/%d slots held", len(holders), n)
	for _, h := range holders {
		fmt.Fprintf(&b, "\nslot %d: held by %s", h.Slot, h)
	}
	return b.String()
}
