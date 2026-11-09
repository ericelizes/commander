package chat

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// A switch stops the old claude and starts a new one on the same session: the
// stop must not return while the old pid still holds it.
func TestWaitGoneReturnsOnlyAfterTheProcessExits(t *testing.T) {
	cmd := exec.Command("sleep", "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	e := &Engine{opts: Options{StopGrace: 5 * time.Second}}
	start := time.Now()
	if err := e.waitGone(context.Background(), cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("returned after %v, before the process exited", time.Since(start))
	}
}

func TestWaitGoneKillsAProcessThatOutlivesTheGrace(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	e := &Engine{opts: Options{StopGrace: 100 * time.Millisecond}}
	if err := e.waitGone(context.Background(), cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
}
