package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets a test re-exec this binary as cmdrd itself.
func TestMain(m *testing.M) {
	if os.Getenv("CMDRD_TEST_RUN_MAIN") == "1" {
		os.Args = os.Args[:1]
		main()
		return
	}
	os.Exit(m.Run())
}

func holdLock(t *testing.T, home string, pid int) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "cmdrd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestSecondCmdrdExitsWhenLockHeld(t *testing.T) {
	home := t.TempDir()
	holdLock(t, home, 4242)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "CMDRD_TEST_RUN_MAIN=1", "COMMANDER_HOME="+home)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second cmdrd exited with %v, want 0; output:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("second cmdrd still running (waiting on the lock); output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "another cmdrd (pid 4242) holds the lock; exiting") {
		t.Fatalf("missing exit log line; output:\n%s", out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(home, "cmdrd.lock")); strings.TrimSpace(string(b)) != "4242" {
		t.Fatalf("lock file rewritten by the loser: %q", b)
	}
}

func TestAcquireLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmdrd.lock")
	if err := os.WriteFile(path, []byte("99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, holder, err := acquireLock(path)
	if err != nil || f == nil || holder != 0 {
		t.Fatalf("free lock: f=%v holder=%d err=%v", f, holder, err)
	}
	defer f.Close()
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file = %q, want our pid", b)
	}

	f2, holder, err := acquireLock(path)
	if err != nil || f2 != nil {
		t.Fatalf("held lock: f=%v err=%v, want nil file", f2, err)
	}
	if holder != os.Getpid() {
		t.Fatalf("holder = %d, want %d", holder, os.Getpid())
	}
}
