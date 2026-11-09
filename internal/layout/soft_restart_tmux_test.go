package layout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/testguard"
)

// socketRunner is tmux on one private socket.
type socketRunner struct {
	socket string
	env    []string
}

func (r socketRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", r.socket}, args...)...)
	cmd.Env = r.env
	var stderr []byte
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		stderr = ee.Stderr
	}
	return out, stderr, err
}

// A real tmux server started under a CLAUDE_CONFIG_DIR that cmdr itself does not
// have: the pane's transcript lies there, a restart carries it to ~/.claude and
// the fake claude comes back resuming it with the variable cleared.
func TestSoftRestartOnARealTmuxServerResumesTheCarriedSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "cl.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	testguard.NoLeakedProcesses(t, dir)
	socket := testguard.TmuxSocket(dir, "private")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testguard.KillTmuxServer(dir, "private") })

	claudeDir := useClaudeRuntime(t)
	home := filepath.Join(dir, "home")
	inherited := filepath.Join(dir, "inherited")
	bin := filepath.Join(dir, "bin")
	log := filepath.Join(dir, "claude.log")
	for _, d := range []string{home, inherited, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", dir)
	if err := os.WriteFile(filepath.Join(home, claudeCommanderSessionFileName), []byte(carriedSession+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, inherited, "inherited\n", time.Hour)
	for name, script := range map[string]string{
		"claude": "#!/bin/sh\necho \"[$CLAUDE_CONFIG_DIR] $*\" >> " + log + "\nexec sleep 300\n",
		"cmdr":   "#!/bin/sh\nexec sleep 300\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	r := socketRunner{socket: socket, env: []string{
		"HOME=" + os.Getenv("HOME"), "PATH=" + bin + ":/usr/bin:/bin:/opt/homebrew/bin", "CLAUDE_CONFIG_DIR=" + inherited, "TERM=xterm",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, stderr, err := r.Run(ctx, "new-session", "-d", "-P", "-F", "#{pane_id}", "-x", "200", "-y", "50", "-s", CommanderSessionName, "-n", FleetWindowName,
		"DISABLE_AUTOUPDATER=1 claude -n commander --resume "+carriedSession)
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, stderr)
	}
	pane := strings.TrimSpace(string(stdout))
	if _, stderr, err := r.Run(ctx, "set-option", "-p", "-t", pane, ChatPaneOption, "1"); err != nil {
		t.Fatalf("set-option: %v: %s", err, stderr)
	}

	for _, column := range []string{"cmdr-objectives", "inspector"} {
		if _, stderr, err := r.Run(ctx, "split-window", "-d", "-h", "-l", "20", "-t", pane, "cmdr __column "+column); err != nil {
			t.Fatalf("split-window: %v: %s", err, stderr)
		}
	}

	if err := NewDirectorWith(r).SoftRestart(ctx, home, false); err != nil {
		t.Fatal(err)
	}

	got := ""
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && got == ""; time.Sleep(50 * time.Millisecond) {
		raw, _ := os.ReadFile(log)
		if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) >= 2 {
			got = lines[len(lines)-1]
		}
	}
	if !strings.HasPrefix(got, "[] ") || !strings.HasSuffix(got, "--resume "+carriedSession) {
		t.Fatalf("restarted claude ran as %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(claudeDir, "projects", "-work", carriedSession+".jsonl"))
	if err != nil || string(raw) != "inherited\n" {
		t.Fatalf("~/.claude holds %q, %v", raw, err)
	}
}
