package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// OwnedError means another process already owns the session id. Two processes
// on one Claude session can fork its history: in an invented handoff exercise,
// a terminal and a headless child compete for the same session, so
// the engine refuses to start instead.
type OwnedError struct {
	SessionID string
	// By is "terminal" (an interactive claude, e.g. the tmux TUI), "headless"
	// (another claude -p, e.g. an orphan of a previous cmdrd), "lock" (the flock
	// is held, holder in Detail) or "engine" (this cmdrd, under Role).
	By     string
	PID    int
	Role   string
	Detail string
}

func (e *OwnedError) Error() string {
	switch e.By {
	case "terminal":
		return fmt.Sprintf("session %s is owned by a terminal (pid %d): hand it over first", e.SessionID, e.PID)
	case "headless":
		return fmt.Sprintf("session %s is owned by another headless claude (pid %d)", e.SessionID, e.PID)
	case "engine":
		return fmt.Sprintf("session %s is already running in this engine as %s", e.SessionID, e.Role)
	}
	return fmt.Sprintf("session %s is locked by another owner (%s)", e.SessionID, strings.TrimSpace(e.Detail))
}

// sessionLock is an exclusive flock on <home>/chat-locks/<session-id>.lock.
// The claude child inherits the descriptor, so the lock outlives a crashed
// cmdrd for as long as its orphaned child keeps running.
type sessionLock struct{ f *os.File }

func acquireSessionLock(dir, sessionID, role string) (*sessionLock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("chat: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("chat: open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(f.Name())
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &OwnedError{SessionID: sessionID, By: "lock", Detail: string(holder)}
		}
		return nil, fmt.Errorf("chat: flock: %w", err)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("cmdrd pid=%d role=%s since=%s\n", os.Getpid(), role, time.Now().Format(time.RFC3339))), 0)
	return &sessionLock{f: f}, nil
}

func (l *sessionLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// handoff closes this descriptor without unlocking: the lock lives on in the
// processes that inherited the open file (the chat host and its claude).
func (l *sessionLock) handoff() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
}

// Proc is a running process that references a session id on its command line.
type Proc struct {
	PID     int
	Command string
}

// ProcScanner lists processes whose command line opens sessionID with
// --session-id or --resume. Tests inject a fake.
type ProcScanner func(ctx context.Context, sessionID string) ([]Proc, error)

// psTTL is how long one process listing answers every session's check.
// In an invented poll of 30 roles, a separate ps for every role would scan
// the same process table 30 times. One cached listing answers the whole poll.
const psTTL = 2 * time.Second

var psCache struct {
	mu  sync.Mutex
	at  time.Time
	out string
}

// PSScanner uses `ps -e -ww -o pid=,args=`, which behaves the same on macOS
// (BSD ps) and Linux (procps), shared by every check within psTTL.
func PSScanner(ctx context.Context, sessionID string) ([]Proc, error) {
	psCache.mu.Lock()
	defer psCache.mu.Unlock()
	if time.Since(psCache.at) >= psTTL {
		out, err := exec.CommandContext(ctx, "ps", "-e", "-ww", "-o", "pid=,args=").Output()
		if err != nil {
			return nil, fmt.Errorf("chat: ps: %w", err)
		}
		psCache.out, psCache.at = string(out), time.Now()
	}
	return parsePS(psCache.out, sessionID), nil
}

func parsePS(out, sessionID string) []Proc {
	re := regexp.MustCompile(`(^|\s)--(session-id|resume)(\s+|=)` + regexp.QuoteMeta(sessionID) + `(\s|'|"|$)`)
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		pidStr, cmd, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		// Cheap test first: the regex backtracks across long argv (an Operator's
		// whole prompt), and it ran over every process for every chat check.
		if !strings.Contains(cmd, sessionID) {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || !re.MatchString(cmd) {
			continue
		}
		// The tmux server keeps the argv of the `tmux new-session ... claude --resume <id>`
		// that spawned it long after that claude exits, so it is never an owner.
		if fields := strings.Fields(cmd); filepath.Base(fields[0]) == "tmux" {
			continue
		}
		procs = append(procs, Proc{PID: pid, Command: strings.TrimSpace(cmd)})
	}
	return procs
}

// isHeadless reports whether a claude command line is a -p (print) run, or a
// Codex bridge (always headless).
func isHeadless(cmd string) bool {
	for _, f := range strings.Fields(cmd) {
		if f == "-p" || f == "--print" || f == CodexBridgeArg {
			return true
		}
	}
	return false
}

// foreignOwner returns the first process in procs that is not one of ours.
func foreignOwner(sessionID string, procs []Proc, ours map[int]bool) *OwnedError {
	for _, p := range procs {
		if ours[p.PID] || p.PID == os.Getpid() {
			continue
		}
		by := "terminal"
		if isHeadless(p.Command) {
			by = "headless"
		}
		return &OwnedError{SessionID: sessionID, By: by, PID: p.PID, Detail: p.Command}
	}
	return nil
}
