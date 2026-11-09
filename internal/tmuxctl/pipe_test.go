package tmuxctl

import (
	"context"
	"os"
	osx "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/testguard"
)

// fileHolders lists the pids that have path open.
func fileHolders(t *testing.T, path string) string {
	t.Helper()
	out, _ := osx.Command("lsof", "-t", path).Output()
	return strings.TrimSpace(string(out))
}

// The pipe-pane writer must end with its pane. It once blocked opening a fifo
// nobody read, outlived every finished Operator and piled up under launchd.
func TestPipePaneWriterEndsWithPane(t *testing.T) {
	for _, bin := range []string{"tmux", "lsof"} {
		if _, err := osx.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir, err := os.MkdirTemp("/tmp", "tp.")
	if err != nil {
		t.Fatal(err)
	}
	testguard.NoLeakedProcesses(t, dir)
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("COMMANDER_TMUX_SOCKET", "pipe-test")
	t.Cleanup(func() {
		_ = osx.Command("tmux", "-L", "pipe-test", "kill-server").Run()
		os.RemoveAll(dir)
	})

	out := filepath.Join(dir, "op.fifo")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := New()
	// A second session keeps the server up, as the keeper does for Operators.
	if err := c.NewSessionCommand(ctx, "keep", dir, "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	if err := c.NewSessionCommand(ctx, "op", dir, "sh", "-c", "echo ready; sleep 30"); err != nil {
		t.Fatal(err)
	}
	if err := c.PipePane(ctx, "op", out); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fileHolders(t, out) != "" })

	if err := c.KillPane(ctx, "op"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fileHolders(t, out) == "" })
}
