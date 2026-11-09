package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat/chattest"
	"github.com/example-user/commander/internal/testguard"
)

const fakeCodexHoldVar = "CMDR_FAKE_CODEX_HOLD"

// fakeCodexHolding plays a Codex stuck in a long tool call: its turn never ends
// by itself, it runs a child as the tool, and it ignores hangups and a closed
// stdin, so only the bridge killing the group ends it.
func fakeCodexHolding(dir string) {
	signal.Ignore(syscall.SIGHUP)
	tool := exec.Command("sleep", "300")
	_ = tool.Start()
	_ = os.WriteFile(filepath.Join(dir, "pids"), []byte(fmt.Sprintf("%d %d", os.Getpid(), tool.Process.Pid)), 0o600)
	write := func(v any) {
		raw, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(raw, '\n'))
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize", "account/rateLimits/read":
			write(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			write(map[string]any{"id": m.ID, "result": map[string]any{"model": "gpt-5.5", "thread": map[string]any{"id": "th-1", "cliVersion": "0.163.0"}}})
		case "turn/start":
			write(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "tu-1"}}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "tu-1"}}})
			_ = os.WriteFile(filepath.Join(dir, "turn-started"), nil, 0o600)
		case "turn/interrupt":
			_ = os.WriteFile(filepath.Join(dir, "turn-interrupted"), nil, 0o600)
			write(map[string]any{"id": m.ID, "result": map[string]any{}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "tu-1", "status": "interrupted"}}})
		}
	}
	select {}
}

func codexStopEnv(dir string) []string {
	return append(os.Environ(), fakeCodexVar+"=1", fakeCodexHoldVar+"=1", chattest.DirVar+"="+dir, "COMMANDER_CODEX_BIN="+os.Args[0], "CODEX_HOME="+dir)
}

func codexStopArgs(dir string) []string {
	return CodexBridgeConfig{Cwd: dir, ThreadFile: filepath.Join(dir, "op.codex-thread"), SessionID: "sess-op", SkipPermissions: true, Prompt: "work"}.Args()
}

// awaitHoldingCodex waits for the held turn and returns Codex's and its tool's pids.
func awaitHoldingCodex(t *testing.T, dir string) []int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "pids"))
		_, started := os.Stat(filepath.Join(dir, "turn-started"))
		if err == nil && started == nil {
			var pids []int
			for _, f := range strings.Fields(string(raw)) {
				pid, _ := strconv.Atoi(f)
				pids = append(pids, pid)
			}
			t.Cleanup(func() {
				for _, pid := range pids {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			return pids
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake Codex never started its turn")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processRunning is true for a live process, false for a gone or zombie one.
func processRunning(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	return err == nil && stat != "" && !strings.HasPrefix(stat, "Z")
}

func assertStopped(t *testing.T, dir string, pids []int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for _, pid := range pids {
		for processRunning(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("pid %d of %v outlived the stop", pid, pids)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "turn-interrupted")); err != nil {
		t.Fatalf("the bridge never asked Codex to end its turn: %v", err)
	}
}

// A hangup (what killing the Operator's pane delivers) ends the bridge, the
// turn in flight, Codex and the tool Codex is running.
func TestCodexBridgeHangupEndsCodexAndItsTool(t *testing.T) {
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	cmd := exec.Command(os.Args[0], codexStopArgs(dir)...)
	cmd.Env = codexStopEnv(dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	pids := awaitHoldingCodex(t, dir)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatal("the bridge did not exit on a hangup")
	}
	assertStopped(t, dir, pids)
}

// The same through a real pane on a private tmux server, run the way the host
// launcher runs it: killing the pane leaves nothing behind.
func TestCodexBridgePaneKillEndsCodexAndItsTool(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	short, err := os.MkdirTemp("/tmp", "cb.")
	if err != nil {
		t.Fatal(err)
	}
	testguard.NoLeakedProcesses(t, short)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	dir, sock := t.TempDir(), filepath.Join(short, "s")
	testguard.NoLeakedProcesses(t, dir)
	quoted := []string{"exec", "env", "-u", "TMUX", "-u", "TMUX_PANE"}
	for _, kv := range []string{fakeCodexVar + "=1", fakeCodexHoldVar + "=1", chattest.DirVar + "=" + dir, "COMMANDER_CODEX_BIN=" + os.Args[0], "CODEX_HOME=" + dir} {
		quoted = append(quoted, "'"+kv+"'")
	}
	quoted = append(quoted, "'"+os.Args[0]+"'")
	for _, a := range codexStopArgs(dir) {
		quoted = append(quoted, "'"+a+"'")
	}
	launcher := filepath.Join(dir, "op.sh")
	// Not exec'd, like spawnCodeHost's launcher: the shell waits on the bridge.
	script := "#!/bin/sh\n" + strings.TrimPrefix(strings.Join(quoted, " "), "exec ") + "\necho $? > '" + filepath.Join(dir, "exit") + "'\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) error {
		c := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...)
		c.Env = os.Environ()
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tmux %v: %w: %s", args, err, out)
		}
		return nil
	}
	if err := tmux("new-session", "-d", "-s", "op", "sh "+launcher); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tmux("kill-server") })
	pids := awaitHoldingCodex(t, dir)
	if err := tmux("kill-pane", "-t", "op"); err != nil {
		t.Fatal(err)
	}
	assertStopped(t, dir, pids)
}
