// Package testguard keeps go test away from the live ~/.commander. Packages
// that open the store call Isolate from TestMain.
package testguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// envTestBinary carries the pid of the guarded test binary to everything it
// starts, so a copy of it started by code under test can tell.
const envTestBinary = "COMMANDER_TESTGUARD_PID"

// guardProcesses ends a test binary that another test binary started: code
// under test that execs os.Executable() (cmdr subcommands, __relayout) gets
// the test binary, which would run the whole suite again, and again in each
// copy. It also caps the processes the binary and its children may create.
func guardProcesses() {
	self := strconv.Itoa(os.Getpid())
	if parent := os.Getenv(envTestBinary); parent != "" && parent != self {
		fmt.Fprintf(os.Stderr, "testguard: %s was started by test binary %s (code under test ran os.Executable()); not running the suite again\n", filepath.Base(os.Args[0]), parent)
		os.Exit(2)
	}
	if err := boundProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		os.Exit(2)
	}
	os.Setenv(envTestBinary, self)
}

// NoLeakedProcesses fails t when a process whose command line contains marker
// (a t.TempDir path, a private socket dir) outlives the test, and kills it.
// Register it before the cleanups that end the test's processes: cleanups run
// last in, first out, so this one checks after them.
func NoLeakedProcesses(t testing.TB, marker string) {
	t.Helper()
	t.Cleanup(func() {
		var left []int
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			left = ProcessesMatching(marker)
			if len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		if len(left) == 0 {
			return
		}
		out, _ := exec.Command("ps", "-o", "pid,ppid,command", "-p", joinPids(left)).CombinedOutput()
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("%d process(es) outlived the test (killed):\n%s", len(left), out)
	})
}

// ProcessesMatching lists the pids (other than this process) whose command
// line contains marker.
func ProcessesMatching(marker string) []int {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(line, marker) {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}

func joinPids(pids []int) string {
	s := make([]string, len(pids))
	for i, p := range pids {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// IsolateTmux gives the whole test binary a private default tmux server: it
// always points TMUX_TMPDIR at a fresh temp dir and unsets TMUX (which would
// otherwise win over TMUX_TMPDIR), so a stray kill-server or kill-session
// can never reach the real server. The dir is short: socket paths over ~104
// chars fail. It also applies guardProcesses and drops the caller's agent role.
// It returns a cleanup func (call
// it before os.Exit).
//
// tmux searches $TMUX_TMPDIR, then /tmp, for its socket directory: with the dir
// gone, a -L or bare call can reach the real server. In an invented cleanup
// exercise, removing an empty private directory exposes that fallback and risks
// unrelated Operators. So the socket directory exists from the start, which
// keeps the dir from being empty, and cleanup names its sockets absolutely.
func IsolateTmux() (cleanup func()) {
	guardProcesses()
	// Tests run from an Operator or a Manager inherit its role: the code under
	// test would judge every call as that agent. A test that wants a role sets it.
	os.Unsetenv("COMMANDER_ROLE")
	os.Unsetenv("COMMANDER_MANAGER_ROLE")
	dir, err := os.MkdirTemp("/tmp", "cl.")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		os.Exit(2)
	}
	if err := os.Mkdir(filepath.Dir(TmuxSocket(dir, "default")), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		os.Exit(2)
	}
	os.Setenv("TMUX_TMPDIR", dir)
	os.Unsetenv("TMUX")
	return func() {
		// Best effort: end any server a test left on the private sockets, the
		// default one and the Operators' (tmuxctl.OpsSocket, not imported here).
		KillTmuxServer(dir, "default")
		KillTmuxServer(dir, "commander-ops")
		os.RemoveAll(dir)
	}
}

// TmuxSocket is the absolute path of socket name under the TMUX_TMPDIR dir.
func TmuxSocket(dir, name string) string {
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}

// KillTmuxServer ends the server on socket name under the TMUX_TMPDIR dir, if
// any. It goes by -S, which has no fallback: when the dir is gone it reaches
// nothing, where -L or a bare kill-server would reach the real server in /tmp.
func KillTmuxServer(dir, name string) {
	_ = exec.Command("tmux", "-S", TmuxSocket(dir, name), "kill-server").Run()
}

// Isolate points COMMANDER_HOME at a private temp dir when it is unset, and
// exits the test binary when it names the real ~/.commander. It also gives
// the binary a private tmux server (IsolateTmux). It returns a cleanup func
// for the temp dirs (call it before os.Exit).
func Isolate() (cleanup func()) {
	// A test binary is nobody's agent: the role of whoever runs the suite (an
	// Operator's COMMANDER_MANAGER_ROLE) must not change what the code under
	// test decides (agentrt.AgentRole). Tests that need a role set it.
	os.Unsetenv("COMMANDER_ROLE")
	os.Unsetenv("COMMANDER_MANAGER_ROLE")
	tmuxCleanup := IsolateTmux()
	cleanup = tmuxCleanup
	if os.Getenv("COMMANDER_HOME") == "" {
		dir, err := os.MkdirTemp("", "cmdr-test-home-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "testguard:", err)
			os.Exit(2)
		}
		os.Setenv("COMMANDER_HOME", dir)
		cleanup = func() { tmuxCleanup(); os.RemoveAll(dir) }
	}
	if home, err := os.UserHomeDir(); err == nil {
		if resolve(os.Getenv("COMMANDER_HOME")) == resolve(filepath.Join(home, ".commander")) {
			fmt.Fprintln(os.Stderr, "testguard: COMMANDER_HOME is the real ~/.commander; refusing to run tests against the live state")
			os.Exit(2)
		}
	}
	return cleanup
}

func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
