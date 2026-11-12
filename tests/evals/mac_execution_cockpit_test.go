package evals

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Render the actual result produced by the Mac scenario, with fake models and
// a separate tmux socket, HOME and COMMANDER_HOME. Never use a visible workspace.
func verifyMacResultCockpit(t *testing.T, bin, dsn string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "macop-ui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	iso, socket := filepath.Join(root, "iso"), filepath.Join(root, "sock")
	if err = os.MkdirAll(filepath.Join(iso, "ch"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{filepath.Join(iso, ".cockpit-isolated"): "private Mac fixture", filepath.Join(iso, "ch", "state.postgres"): dsn} {
		if err = os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fake := filepath.Join(root, "opencode")
	mustBuild(t, os.Environ(), fake, "./tests/evals/fakeopencode")
	script := func(verb string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
		defer cancel()
		c := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(), "scripts/cockpit-test.sh"), verb)
		c.Env = []string{"HOME=" + filepath.Join(root, "home"), "PATH=/usr/bin:/bin", "COCKPIT_HOME=" + iso, "COCKPIT_TMUX_TMPDIR=" + socket, "COCKPIT_CMDR=" + bin, "COCKPIT_FAKE_OPENCODE=" + fake}
		c.WaitDelay = 3 * time.Second
		out, e := c.CombinedOutput()
		return string(out), e
	}
	t.Cleanup(func() {
		if out, e := script("down"); e != nil {
			t.Errorf("Mac cockpit teardown: %v %s", e, out)
		}
	})
	if out, e := script("up"); e != nil {
		t.Fatalf("Mac cockpit startup: %v %s", e, out)
	}
	tm := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "tmux", append([]string{"-S", filepath.Join(socket, "cockpit.sock")}, args...)...)
		out, e := c.CombinedOutput()
		return string(out), e
	}
	panes, err := tm("list-panes", "-t", "commander", "-F", "#{pane_id}|#{pane_start_command}")
	if err != nil {
		t.Fatal(err, panes)
	}
	pane := ""
	for _, line := range strings.Split(panes, "\n") {
		if strings.Contains(line, "__column cmdr-objectives") {
			pane = strings.SplitN(line, "|", 2)[0]
			break
		}
	}
	if pane == "" {
		t.Fatal("Mac cockpit objectives pane missing")
	}
	var text string
	deadline := time.Now().Add(evalTimeout(t, 20*time.Second))
	for time.Now().Before(deadline) {
		flag, e := tm("display-message", "-p", "-t", pane, "#{window_zoomed_flag}")
		if e != nil {
			t.Fatal(e, flag)
		}
		if strings.TrimSpace(flag) != "1" {
			if out, e := tm("resize-pane", "-Z", "-t", pane); e != nil {
				t.Fatal(e, out)
			}
		}
		text, err = script("text")
		if err == nil && strings.Contains(text, "Mac execution result") && strings.Contains(text, "Commander to review the finished result") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !strings.Contains(text, "Mac execution result") || !strings.Contains(text, "Commander to review the finished result") {
		t.Fatalf("Mac result missing from detached cockpit: %v\n%s", err, text)
	}
	if strings.Contains(text, "Manager review overdue") || strings.Contains(text, "real claude is disabled") || strings.Contains(text, "real codex is disabled") {
		t.Fatalf("unexpected live runtime or legacy status:\n%s", text)
	}
	t.Logf("Actual Mac PostgreSQL result in detached cockpit via scripts/cockpit-test.sh text:\n%s", text)
}
