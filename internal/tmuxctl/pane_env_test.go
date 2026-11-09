package tmuxctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPaneEnvironmentPrivateSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "pane-env-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "socket")
	ctx := context.Background()
	// Every invocation is pinned to this private socket, including client calls.
	run := func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if len(args) >= 2 && args[0] == "-L" {
			args = args[2:]
		}
		return defaultRun(ctx, name, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
	}
	c := &cmdClient{runner: run}
	t.Setenv("TMUX", "poisoned-parent")
	t.Setenv("TMUX_PANE", "%999")
	// Leave one pane alive until both new-session and respawn have been checked.
	if err := c.NewSessionCommand(ctx, "env-test", dir, "cat"); err != nil {
		t.Fatal(err)
	}
	defer c.KillPane(ctx, "env-test")
	if ok, err := c.SessionExists(ctx, "env-test"); err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	// Poison the session environment too: tmux may restore variables at respawn.
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		if _, stderr, err := run(ctx, "tmux", "set-environment", "-t", "env-test", key, "poisoned-session"); err != nil {
			t.Fatalf("set environment: %v %s", err, stderr)
		}
	}
	out := filepath.Join(dir, "env")
	if err := c.RespawnPane(ctx, "env-test", dir, "sh", "-c", "env > '"+out+"'; sleep 2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && len(b) > 0 {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "TMUX=") || strings.HasPrefix(line, "TMUX_PANE=") {
					t.Fatalf("leaked: %s", line)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pane did not write environment")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
