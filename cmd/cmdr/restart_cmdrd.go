package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

// Seams for the restart cleanup; replaced in tests so no real process is touched.
var (
	listCmdrdProcsFn = listCmdrdProcs
	signalProcFn     = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	procAliveFn      = func(pid int) bool { return syscall.Kill(pid, 0) == nil }
	lockPidFn        = readCmdrdLockPid
	lockHolderFn     = cmdrdLockHolder
	stopWait         = 15 * time.Second
	stopPoll         = 200 * time.Millisecond
)

type procInfo struct {
	PID  int
	Args []string
}

// isCmdrdDaemon is true for the cmdrd daemon itself: executable cmdrd (or
// cmdrd.prev) with no "__" subcommand, so chat hosts and bridges never match.
func isCmdrdDaemon(p procInfo) bool {
	if len(p.Args) == 0 {
		return false
	}
	base := filepath.Base(p.Args[0])
	if base != "cmdrd" && base != "cmdrd.prev" {
		return false
	}
	for _, a := range p.Args[1:] {
		if strings.HasPrefix(a, "__") {
			return false
		}
	}
	return true
}

func listCmdrdProcs() ([]procInfo, error) {
	out, err := exec.Command("ps", "-u", strconv.Itoa(os.Getuid()), "-o", "pid=,args=").Output()
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		procs = append(procs, procInfo{PID: pid, Args: f[1:]})
	}
	return procs, nil
}

// readCmdrdLockPid returns the pid the running daemon wrote into cmdrd.lock (0 if none).
func readCmdrdLockPid() int {
	b, err := os.ReadFile(filepath.Join(agentrt.CommanderHome(), "cmdrd.lock"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// cmdrdLockHolder returns the pid of the live cmdrd daemon recorded in
// <home>/cmdrd.lock, or 0. The file keeps the last pid after its daemon exits,
// so the pid must still be a cmdrd daemon, not a reused one.
func cmdrdLockHolder(home string) int {
	b, err := os.ReadFile(filepath.Join(home, "cmdrd.lock"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 || !procAliveFn(pid) || !daemonPids()[pid] {
		return 0
	}
	return pid
}

func daemonPids() map[int]bool {
	pids := map[int]bool{}
	procs, err := listCmdrdProcsFn()
	if err != nil {
		return pids
	}
	for _, p := range procs {
		if isCmdrdDaemon(p) {
			pids[p.PID] = true
		}
	}
	return pids
}

// restartAndStopStrays restarts the service, then ends any cmdrd daemon that was
// running before the restart and survived it (one the desktop app started, which
// the service manager does not own), then starts the service again: its first
// copy exited because a stray still held the lock.
func restartAndStopStrays(stdout, stderr io.Writer) error {
	before := daemonPids()
	if pid := lockPidFn(); pid > 0 && procAliveFn(pid) {
		before[pid] = true
	}
	if err := restartCmdrdFn(); err != nil {
		return err
	}
	// Re-list: a pid that is no longer a cmdrd daemon was reused or exited.
	now := daemonPids()
	var strays []int
	for pid := range before {
		if now[pid] && procAliveFn(pid) {
			strays = append(strays, pid)
		}
	}
	for _, pid := range strays {
		fmt.Fprintf(stdout, "stopping old cmdrd %d (not owned by the service)\n", pid)
		_ = signalProcFn(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(stopWait)
	for _, pid := range strays {
		for procAliveFn(pid) && time.Now().Before(deadline) {
			time.Sleep(stopPoll)
		}
		if !procAliveFn(pid) {
			fmt.Fprintf(stdout, "old cmdrd %d stopped\n", pid)
			continue
		}
		fmt.Fprintf(stderr, "cmdr install: old cmdrd %d ignored SIGTERM for %s; sending SIGKILL\n", pid, stopWait)
		_ = signalProcFn(pid, syscall.SIGKILL)
	}
	if len(strays) > 0 {
		return restartCmdrdFn()
	}
	return nil
}
