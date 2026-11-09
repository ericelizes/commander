package testguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The guard may re-exec the whole binary into a bounded scope and exit with its
// result, which is only legal before m.Run: os.Exit(0) inside a test panics. This
// package's tests call the guard themselves, so bound the binary here, as every
// other package's TestMain does through Isolate.
func TestMain(m *testing.M) {
	if err := boundProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestIsolateSetsTempHomeWhenUnset(t *testing.T) {
	t.Setenv("COMMANDER_HOME", "")
	cleanup := Isolate()
	defer cleanup()
	got := os.Getenv("COMMANDER_HOME")
	if got == "" {
		t.Fatal("COMMANDER_HOME not set")
	}
	if home, _ := os.UserHomeDir(); got == filepath.Join(home, ".commander") {
		t.Fatalf("COMMANDER_HOME = real home %s", got)
	}
}

func TestIsolateTmuxPrivateDir(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	cleanup := IsolateTmux()
	defer cleanup()
	got := os.Getenv("TMUX_TMPDIR")
	if got == "/tmp" || got == "" {
		t.Fatalf("TMUX_TMPDIR = %q, want a private dir", got)
	}
	if os.Getenv("TMUX") != "" {
		t.Fatal("TMUX still set")
	}
}

// The private dir holds tmux's socket directory from the start, so an rmdir
// glob over /tmp/cl.* cannot remove it from under a binary with no server yet.
func TestIsolateTmuxDirSurvivesRmdir(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "")
	cleanup := IsolateTmux()
	defer cleanup()
	dir := os.Getenv("TMUX_TMPDIR")
	sockets := filepath.Dir(TmuxSocket(dir, "default"))
	st, err := os.Stat(sockets)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("%s mode %v, tmux refuses anything but 0700", sockets, st.Mode().Perm())
	}
	if err := os.Remove(dir); err == nil {
		t.Fatalf("rmdir removed %s", dir)
	}
}

// With the private dir removed mid-run, cleanup must still name only absolute
// private sockets: a bare or -L kill-server would fall back to /tmp and end the
// real server in the invented missing-directory regression exercise.
func TestIsolateTmuxCleanupNeverFallsBack(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "")
	cleanup := IsolateTmux()
	dir := os.Getenv("TMUX_TMPDIR")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	cleanup()
	out, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	want := "-S " + TmuxSocket(dir, "default") + " kill-server\n" +
		"-S " + TmuxSocket(dir, "commander-ops") + " kill-server\n"
	if string(out) != want {
		t.Fatalf("tmux calls:\n%swant:\n%s", out, want)
	}
}

// The absolute sockets are the ones tmux really uses under TMUX_TMPDIR: cleanup
// ends servers a test started the ordinary way, on both names.
func TestIsolateTmuxCleanupEndsRealServers(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "")
	cleanup := IsolateTmux()
	defer cleanup()
	var pids []int
	for _, flags := range [][]string{nil, {"-L", "commander-ops"}} {
		tmux := func(args ...string) string {
			out, err := exec.Command("tmux", append(flags, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("tmux %v %v: %v: %s", flags, args, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		tmux("-f", "/dev/null", "new-session", "-d", "-s", "t", "sleep 60")
		pid, err := strconv.Atoi(tmux("display-message", "-p", "#{pid}"))
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	cleanup()
	for _, pid := range pids {
		deadline := time.Now().Add(5 * time.Second)
		for unix.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("server %d still running after cleanup", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// A test binary started by code under test (os.Executable() is the test
// binary) must stop at its guard instead of running the suite again: a
// recursive child would otherwise spawn another copy at each level.
func TestGuardStopsReexecutedTestBinary(t *testing.T) {
	if os.Getenv("TESTGUARD_CHILD") == "1" {
		IsolateTmux()
		fmt.Println("suite ran")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGuardStopsReexecutedTestBinary$")
	cmd.Env = append(os.Environ(), "TESTGUARD_CHILD=1", envTestBinary+"=1")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || strings.Contains(string(out), "suite ran") {
		t.Fatalf("re-executed test binary was not stopped: err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "not running the suite again") {
		t.Errorf("output: %s", out)
	}
}

func TestGuardCapsProcesses(t *testing.T) {
	var before unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &before); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = unix.Setrlimit(unix.RLIMIT_NPROC, &before) })
	capProcesses()
	var after unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &after); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" && after.Cur != before.Cur {
		t.Fatalf("Linux per-UID limit changed: %d -> %d", before.Cur, after.Cur)
	}
	if runtime.GOOS != "linux" && before.Cur != unix.RLIM_INFINITY && before.Cur > 2*1000 && after.Cur != before.Cur-1000 {
		t.Errorf("RLIMIT_NPROC soft = %d, want %d", after.Cur, before.Cur-1000)
	}
}

func TestProcessesMatching(t *testing.T) {
	marker := t.TempDir()
	cmd := exec.Command("sh", "-c", "sleep 30; : "+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if got := ProcessesMatching(marker); len(got) != 1 || got[0] != cmd.Process.Pid {
		t.Errorf("ProcessesMatching = %v, want [%d]", got, cmd.Process.Pid)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if got := ProcessesMatching(marker); len(got) != 0 {
		t.Errorf("after kill: %v", got)
	}
}

func TestIsolateTmuxDropsTheCallersRole(t *testing.T) {
	t.Setenv("COMMANDER_ROLE", "operator-x")
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-x")
	cleanup := IsolateTmux()
	defer cleanup()
	for _, k := range []string{"COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s = %q survived", k, v)
		}
	}
}
