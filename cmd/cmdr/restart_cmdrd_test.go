package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProcs replaces every process seam: procs are "alive" until signalled
// with a signal in dies (SIGTERM by default behaviour set per test).
type fakeWorld struct {
	alive    map[int]bool
	signals  []string
	ignore   map[syscall.Signal]bool // signals that do not kill
	restarts int
}

func fakeProcs(t *testing.T, procs []procInfo) *fakeWorld {
	t.Helper()
	w := &fakeWorld{alive: map[int]bool{}, ignore: map[syscall.Signal]bool{}}
	for _, p := range procs {
		w.alive[p.PID] = true
	}
	oldList, oldSig, oldAlive, oldLock, oldWait, oldPoll := listCmdrdProcsFn, signalProcFn, procAliveFn, lockPidFn, stopWait, stopPoll
	t.Cleanup(func() {
		listCmdrdProcsFn, signalProcFn, procAliveFn, lockPidFn, stopWait, stopPoll = oldList, oldSig, oldAlive, oldLock, oldWait, oldPoll
	})
	listCmdrdProcsFn = func() ([]procInfo, error) {
		var out []procInfo
		for _, p := range procs {
			if w.alive[p.PID] {
				out = append(out, p)
			}
		}
		return out, nil
	}
	signalProcFn = func(pid int, sig syscall.Signal) error {
		w.signals = append(w.signals, sig.String()+":"+itoa(pid))
		if !w.ignore[sig] {
			w.alive[pid] = false
		}
		return nil
	}
	procAliveFn = func(pid int) bool { return w.alive[pid] }
	lockPidFn = func() int { return 0 }
	stopWait, stopPoll = 20*time.Millisecond, time.Millisecond
	return w
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRestartOnly_AppSpawnedDaemonStopped(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{
		{PID: 44595, Args: []string{"/Applications/Commander.app/x/cmdrd"}},
		{PID: 50001, Args: []string{"/h/bin/cmdrd", "__chat-host", "cfg"}},
		{PID: 50002, Args: []string{"/h/bin/cmdr", "__codex-bridge"}},
		{PID: 50003, Args: []string{"/h/bin/cmdrd", "__codex-bridge"}},
	})
	restartCmdrdFn = func() error { return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil {
		t.Fatal(err)
	}
	if len(w.signals) != 1 || w.signals[0] != "terminated:44595" {
		t.Fatalf("signals = %v", w.signals)
	}
	if !strings.Contains(out.String(), "old cmdrd 44595 stopped") {
		t.Errorf("out = %q", out.String())
	}
	for _, pid := range []int{50001, 50002, 50003} {
		if !w.alive[pid] {
			t.Errorf("pid %d was touched", pid)
		}
	}
}

func TestRestartOnly_ServiceDaemonKilledByRestartIsNotSignalled(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{{PID: 100, Args: []string{"/h/bin/cmdrd"}}})
	restartCmdrdFn = func() error { w.alive[100] = false; return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil {
		t.Fatal(err)
	}
	if len(w.signals) != 0 {
		t.Fatalf("signals = %v", w.signals)
	}
}

func TestRestartOnly_NoOtherDaemonIsNoop(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, nil)
	restartCmdrdFn = func() error { return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil || len(w.signals) != 0 || out.Len() != 0 {
		t.Fatalf("err %v signals %v out %q", err, w.signals, out.String())
	}
}

func TestRestartOnly_LockHolderStopped(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{{PID: 777, Args: []string{"/old/path/cmdrd.prev"}}})
	lockPidFn = func() int { return 777 }
	restartCmdrdFn = func() error { return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil || w.alive[777] {
		t.Fatalf("err %v alive %v", err, w.alive[777])
	}
}

func TestRestartOnly_SigtermThenTimeoutKills(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{{PID: 888, Args: []string{"/x/cmdrd"}}})
	w.ignore[syscall.SIGTERM] = true
	restartCmdrdFn = func() error { return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil {
		t.Fatal(err)
	}
	if len(w.signals) != 2 || w.signals[1] != "killed:888" {
		t.Fatalf("signals = %v", w.signals)
	}
	if !strings.Contains(errs.String(), "SIGKILL") {
		t.Errorf("errs = %q", errs.String())
	}
}

func TestRestartOnly_CommandStopsAppSpawned(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{{PID: 4, Args: []string{"/app/cmdrd"}}})
	restartCmdrdFn = func() error { return nil }
	if code, out, errs := runInstallArgs("", "--yes", "--restart-only"); code != 0 || w.alive[4] {
		t.Fatalf("code %d alive %v out %q errs %q", code, w.alive[4], out, errs)
	}
}

func TestRestartOnly_ServiceStartedAgainAfterStraysStop(t *testing.T) {
	newInstallEnv(t)
	w := fakeProcs(t, []procInfo{{PID: 44595, Args: []string{"/Applications/Commander.app/x/cmdrd"}}})
	restartCmdrdFn = func() error { w.restarts++; return nil }
	var out, errs bytes.Buffer
	if err := restartAndStopStrays(&out, &errs); err != nil {
		t.Fatal(err)
	}
	// The first service start exited on the stray's lock; the second takes it.
	if w.restarts != 2 {
		t.Fatalf("restarts = %d, want 2", w.restarts)
	}
}

func TestCmdrdLockHolder(t *testing.T) {
	home := t.TempDir()
	w := fakeProcs(t, []procInfo{
		{PID: 10, Args: []string{"/h/bin/cmdrd"}},
		{PID: 11, Args: []string{"/usr/bin/vim"}},
	})
	if got := cmdrdLockHolder(home); got != 0 {
		t.Fatalf("no lock file: %d", got)
	}
	write := func(pid int) {
		if err := os.WriteFile(filepath.Join(home, "cmdrd.lock"), []byte(itoa(pid)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(10)
	if got := cmdrdLockHolder(home); got != 10 {
		t.Fatalf("live daemon: %d", got)
	}
	write(11) // pid reused by another program
	if got := cmdrdLockHolder(home); got != 0 {
		t.Fatalf("reused pid: %d", got)
	}
	write(10)
	w.alive[10] = false // crashed; the file keeps its pid
	if got := cmdrdLockHolder(home); got != 0 {
		t.Fatalf("dead daemon: %d", got)
	}
}
