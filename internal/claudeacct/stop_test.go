package claudeacct

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A claude child whose context ends gets SIGTERM and time to save its work
// (an OAuth refresh in flight), not an immediate SIGKILL.
func TestGracefulStopsWithSIGTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "saved")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := `trap 'echo saved > "$0"; exit 0' TERM; echo up; while :; do sleep 0.05; done`
	cmd := Graceful(exec.CommandContext(ctx, "sh", "-c", script, marker))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Read(make([]byte, 3)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	_ = cmd.Wait()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("child was not given SIGTERM to finish its work: %v", err)
	}
	if !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("child did not exit by itself: %v", cmd.ProcessState)
	}
	if took := time.Since(start); took >= StopGrace {
		t.Fatalf("stop took %v, the whole grace", took)
	}
}
