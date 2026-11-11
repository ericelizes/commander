// Command cmdrd is the commander always-on daemon (U20 + U21).
//
// It runs as a long-lived background process (started by launchd on macOS or
// systemd on Linux) and hosts the watchdog, scheduler, and inbox poller as
// in-memory goroutines. The TUI attaches over a Unix socket at
// ~/.commander/cmdrd.sock for live state.
//
// Lifecycle:
//
//	cmdrd starts            → daemon.New + Run; subsystems come up
//	SIGINT / SIGTERM lands  → ctx cancels; Run unwinds in reverse order
//	process exits 0         → launchd / systemd may restart per unit policy
//
// Flags:
//
//	--notifications=true    Native OS notifications for ceiling-hit /
//	                        urgent mail (default true). Disable for headless
//	                        runs (CI, ssh-only servers) where popping a
//	                        Notification Center alert is noise. Default-on
//	                        because the whole point of cmdrd is to wake up
//	                        the human at 2am when something needs them.
//
// All non-flag logic lives in internal/daemon. main is intentionally thin —
// startup failures and graceful shutdown both flow through daemon.Run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/daemon"
)

func main() {
	withToolPath()
	chat.MaybeRunHost()        // `cmdrd __chat-host <cfg>`: a detached chat host, not the daemon
	chat.MaybeRunCodexBridge() // `cmdrd __codex-bridge ...`: a Codex chat's stream-json bridge
	notifications := flag.Bool("notifications", true, "emit native OS notifications on ceiling-hit / urgent mail")
	flag.Parse()
	singleInstance()
	startProfiler()
	// The daemon allocates heavily per board rebuild but keeps little live
	// (about 100 MB). Collect less often: a few hundred MB for much less GC CPU.
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(400)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := daemon.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cmdrd:", err)
		os.Exit(1)
	}
	d.SetNotificationsEnabled(*notifications)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "cmdrd:", err)
		os.Exit(1)
	}
}

// withToolPath adds the usual tool folders to PATH when missing. cmdrd shells
// out to br, tmux, claude, codex and cmdr; started by launchd it gets them from
// the service file, but started any other way (a GUI app's bare system PATH)
// it found none and every state read failed ("br: executable file not found").
func withToolPath() {
	home, _ := os.UserHomeDir()
	path := os.Getenv("PATH")
	have := map[string]bool{}
	for _, d := range filepath.SplitList(path) {
		have[d] = true
	}
	var add []string
	for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		if !have[d] {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				add = append(add, d)
			}
		}
	}
	if len(add) > 0 {
		_ = os.Setenv("PATH", strings.Join(append(add, path), string(os.PathListSeparator)))
	}
}

// singleInstance makes this the only cmdrd: it holds an exclusive lock for its
// whole life. A second copy exits at once instead of waiting: a waiter kept the
// binary it was started from, so after an install it took over with the old
// code, and an app retrying a slow API after sleep piled up several of them.
// The service manager restarts its own unit when the lock comes free.
func singleInstance() {
	f, holder, err := acquireLock(filepath.Join(agentrt.CommanderHome(), "cmdrd.lock"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cmdrd: lock:", err)
		os.Exit(1)
	}
	if f == nil {
		log.Printf("cmdrd: another cmdrd (pid %d) holds the lock; exiting", holder)
		os.Exit(0)
	}
	lockFile = f // stays open (and locked) until this process exits
}

// acquireLock takes the lock without blocking and writes our pid into it. When
// another process holds it, it returns a nil file and the pid recorded there.
func acquireLock(path string) (*os.File, int, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := io.ReadAll(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return nil, pid, nil
		}
		return nil, 0, err
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return f, 0, nil
}

var lockFile *os.File
