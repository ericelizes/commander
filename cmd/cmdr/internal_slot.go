package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/slot"
)

const acquireCmd = "internal-slot acquire"

const slotUsage = "usage: cmdr internal-slot run [--timeout D] -- <cmd...> | acquire [--timeout D] | release | status"

// holdTTL bounds an `acquire` whose release never comes.
const holdTTL = 2 * time.Hour

// runInternalSlot is the machine's build slots (internal/slot): `run` holds one
// around one command, `acquire`/`release` hold one across several.
func runInternalSlot(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		emitErr(stderr, slotUsage)
		return 2
	}
	home := commanderHomeDir()
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("internal-slot "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	timeout := fs.Duration("timeout", 0, "give up after this long (queueing included); 0 = no limit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch verb {
	case "run":
		code, err := slot.Run(context.Background(), home, fs.Args(), *timeout, stdout, stderr)
		if err != nil {
			emitErr(stderr, "internal-slot: "+err.Error())
		}
		return code
	case "status":
		fmt.Fprintln(stdout, slot.Status(home))
		return 0
	case "release":
		pid := acquiredHolder(home)
		if pid == 0 {
			fmt.Fprintln(stdout, "no acquired slot to release")
			return 0
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			emitErr(stderr, "internal-slot release: "+err.Error())
			return 1
		}
		fmt.Fprintln(stdout, "released")
		return 0
	case "acquire":
		return slotAcquire(home, *timeout, stdout, stderr)
	case "__hold":
		return slotHold(home, stdout)
	}
	emitErr(stderr, slotUsage)
	return 2
}

// slotAcquire starts a detached holder and returns once it has the slot, so
// the lock outlives this process until `release` (or holdTTL).
func slotAcquire(home string, timeout time.Duration, stdout, stderr *os.File) int {
	self, err := os.Executable()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	cmd := exec.Command(self, "internal-slot", "__hold")
	cmd.Env = append(os.Environ(), "COMMANDER_HOME="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := cmd.Start(); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	go func() {
		if timeout > 0 {
			time.Sleep(timeout)
			cmd.Process.Kill()
		}
	}()
	sc := bufio.NewScanner(pipe)
	for sc.Scan() {
		line := sc.Text()
		if line == "acquired" {
			fmt.Fprintln(stdout, "build slot acquired; free it with: cmdr internal-slot release")
			return 0
		}
		fmt.Fprintln(stderr, line)
	}
	emitErr(stderr, "internal-slot acquire: gave up waiting for the slot")
	return 1
}

// slotHold is the detached holder: it reports on stdout, then sleeps holding
// the lock until SIGTERM (release) or holdTTL.
func slotHold(home string, stdout *os.File) int {
	h, err := slot.Acquire(context.Background(), home, slot.Options{Cmd: acquireCmd, Out: stdout})
	if err != nil {
		return 1
	}
	defer h.Release()
	fmt.Fprintln(stdout, "acquired")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sig:
	case <-time.After(holdTTL):
	}
	return 0
}

// acquiredHolder is the pid of the `acquire` holder this caller started: the
// one acquired from the same directory, else the only one of its workstream.
// 0 when there is none.
func acquiredHolder(home string) int {
	dir, _ := os.Getwd()
	ws := slot.WorkstreamFromEnv()
	var sameWS []slot.Info
	for _, h := range slot.Holders(home, 0) {
		if h.Cmd != acquireCmd {
			continue
		}
		if h.Dir == dir {
			return h.PID
		}
		if h.Workstream == ws {
			sameWS = append(sameWS, h)
		}
	}
	if len(sameWS) == 1 {
		return sameWS[0].PID
	}
	return 0
}
