package evals

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCockpitIsolation(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot(), "scripts", "cockpit-test_test.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cockpit isolation: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}
