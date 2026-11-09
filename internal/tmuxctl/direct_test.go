package tmuxctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

// A shell startup file that prompts must not run, nor eat what the launcher
// needs: the pane gets the command by exec, never through an interactive shell.
func TestRespawnPane_SkipsPromptingShellStartup(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	tmp, err := os.MkdirTemp("/tmp", "cl.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	t.Setenv("TMUX_TMPDIR", tmp)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")

	zdot := filepath.Join(tmp, "zdot")
	if err := os.MkdirAll(zdot, 0o755); err != nil {
		t.Fatal(err)
	}
	rcRan := filepath.Join(tmp, "rc-ran")
	rc := "echo ran > " + rcRan + "\nread -k 1 'x?Run /evolve to research? [y/N/skip] '\n"
	if err := os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZDOTDIR", zdot)
	t.Setenv("SHELL", "/bin/zsh")
	t.Cleanup(func() { testguard.KillTmuxServer(tmp, "default") })

	out := filepath.Join(tmp, "out")
	launcher := filepath.Join(tmp, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho started > "+out+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	c := New()
	if err := c.NewSessionCommand(ctx, "t-direct", tmp, "cat"); err != nil {
		t.Fatalf("NewSessionCommand: %v", err)
	}
	if err := c.RespawnPane(ctx, "t-direct", tmp, "sh", launcher); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("launcher did not run: %v", err)
	}
	if _, err := os.Stat(rcRan); err == nil {
		t.Error("shell startup file ran; the pane must not start an interactive shell")
	}
	// The command exited, so the session ends: gone means done.
	for time.Now().Before(deadline) {
		if ok, _ := c.SessionExists(ctx, "t-direct"); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("session still exists after its command exited")
}

func TestNewSessionCommand_RespawnPane_Argv(t *testing.T) {
	r := &recorder{}
	c := &cmdClient{runner: r.fn()}
	if err := c.NewSessionCommand(context.Background(), "n", "/c", "cat"); err != nil {
		t.Fatal(err)
	}
	if r.args[len(r.args)-1] != "cat" || !argvHasFlag(r.args, "-c", "/c") {
		t.Errorf("new-session argv: %v", r.args)
	}
	if err := c.RespawnPane(context.Background(), "n", "/c", "sh", "/l.sh"); err != nil {
		t.Fatal(err)
	}
	if !argvHasToken(r.args, "respawn-pane") || !argvHasToken(r.args, "-k") ||
		r.args[len(r.args)-2] != "sh" || r.args[len(r.args)-1] != "/l.sh" {
		t.Errorf("respawn-pane argv: %v", r.args)
	}
}
