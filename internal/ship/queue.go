package ship

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// QueueWaitEnv overrides, as a Go duration, how long a ship waits for its turn
// (the line, the build slot and the lock together) before it gives up.
const QueueWaitEnv = "COMMANDER_SHIP_QUEUE_WAIT"

const (
	// defaultQueueWait is 8-18 ships at the 13-33 minutes a ship took on main
	// (reflog, 2026-10-30). Past it something is stuck.
	defaultQueueWait = 4 * time.Hour
	// DefaultVerifyBudget bounds one verify. Queue waits do not count.
	DefaultVerifyBudget = 90 * time.Minute
	// DefaultHoldBudget bounds a ship's whole hold of the machine lock: a verify
	// that used its full budget after the full thread wait still has room to
	// merge, and a replay runs on what is left.
	DefaultHoldBudget = 2 * time.Hour
	// DefaultStepBudget bounds each short step under the lock. A hold check under
	// load takes minutes (3 reads, each queueing for a br slot); nothing else near.
	DefaultStepBudget = 15 * time.Minute

	queuePoll      = 2 * time.Second
	queueNoteEvery = 30 * time.Second
	ticketPrefix   = "t-"
	newTicket      = "new-"
)

func queueWait() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(QueueWaitEnv)); err == nil && d > 0 {
		return d
	}
	return defaultQueueWait
}

// HoldVerifyLock lets one ship verify run on the machine at a time, first come,
// first served within a project: the flock on $COMMANDER_HOME/ship-verify.lock
// decides who holds it, and a ship tries that flock only once no live ticket of
// its project is ahead of it. Another project's ships have their own line and
// meet this one only at the flock. A flock is freed when its process ends, so a
// crash can never wedge the queue.
//
// A ship at the head of its line takes the build slot (takeSlot; nil takes none)
// before it tries the flock, so nothing ever waits for a slot under the lock:
// one stuck slot holder cannot stop other projects' ships. The ticket stays
// until the flock is held, so the wait keeps its place. The cost is one slot per
// project with ships waiting, idle until the lock comes free. The release frees
// both.
func HoldVerifyLock(ctx context.Context, home, root string, out io.Writer, takeSlot func(context.Context, io.Writer) (func(), error)) (func(), error) {
	return holdVerifyLock(ctx, home, root, out, queuePoll, takeSlot)
}

func holdVerifyLock(ctx context.Context, home, root string, out io.Writer, poll time.Duration, takeSlot func(context.Context, io.Writer) (func(), error)) (func(), error) {
	dir := queueDir(home, root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, "ship-verify.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ticket, name, err := takeTicket(dir)
	if err != nil {
		f.Close()
		return nil, err
	}
	defer func() {
		os.Remove(filepath.Join(dir, name))
		ticket.Close()
	}()
	releaseSlot := func() {}
	start, lastNote, haveSlot := time.Now(), time.Time{}, takeSlot == nil
	for {
		ahead := ticketsAhead(dir, name)
		if ahead == 0 && !haveSlot {
			release, err := takeSlot(ctx, out)
			if err != nil {
				f.Close()
				return nil, err
			}
			releaseSlot, haveSlot = release, true
		}
		if ahead == 0 && syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			f.Truncate(0)
			f.WriteAt([]byte(fmt.Sprintf("%d %d\n", os.Getpid(), time.Now().Unix())), 0)
			return func() { f.Close(); releaseSlot() }, nil
		}
		if lastNote.IsZero() || time.Since(lastNote) >= queueNoteEvery {
			lastNote = time.Now()
			fmt.Fprintf(out, "ship check: in the ship queue, %d ahead (verifying: %s); %s so far\n", ahead, lockHolder(f.Name()), time.Since(start).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			f.Close()
			releaseSlot()
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// queueDir is the line of one project's ships.
func queueDir(home, root string) string {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(home, "ship-queue", fmt.Sprintf("%s-%x", filepath.Base(root), sum[:4]))
}

var ticketSeq atomic.Int64

// takeTicket joins the line: a file named by arrival, flocked by its owner for
// as long as the owner lives. The flock goes on before the name appears, so a
// ticket in the line without one is always stale. The pid in the name and the
// file is for people; a reused pid cannot keep a dead ship's place.
func takeTicket(dir string) (*os.File, string, error) {
	f, err := os.CreateTemp(dir, newTicket+"*")
	if err != nil {
		return nil, "", err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		os.Remove(f.Name())
		f.Close()
		return nil, "", err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	name := fmt.Sprintf("%s%020d-%d-%d", ticketPrefix, time.Now().UnixNano(), os.Getpid(), ticketSeq.Add(1))
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(f.Name())
		f.Close()
		return nil, "", err
	}
	return f, name, nil
}

// ticketsAhead counts the live tickets ahead of mine and removes stale ones. A
// line it cannot read counts as empty: the flock still holds the verify to one
// at a time, and a broken queue must not stop ships.
func ticketsAhead(dir, mine string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents { // sorted by name, so by arrival
		path := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), newTicket) {
			// A crash between create and rename; a live one is gone in microseconds.
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Minute {
				os.Remove(path)
			}
			continue
		}
		if e.Name() >= mine {
			break
		}
		if ticketLive(path) {
			n++
		}
	}
	return n
}

// ticketLive reports whether the ticket's owner still holds its flock, and
// removes the ticket when it does not. A shared probe never disturbs another
// ship's probe.
func ticketLive(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err {
	case nil:
		os.Remove(path)
		return false
	case syscall.EWOULDBLOCK:
		return true
	default:
		return false
	}
}

// lockHolder describes the holder recorded in the lock file ("pid N, running
// 3m"); the file is informational, the flock is the truth.
func lockHolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "holder unknown"
	}
	var pid int
	var since int64
	if n, _ := fmt.Sscanf(string(b), "%d %d", &pid, &since); n != 2 {
		return "holder unknown"
	}
	return fmt.Sprintf("pid %d, running %s", pid, time.Since(time.Unix(since, 0)).Round(time.Second))
}
