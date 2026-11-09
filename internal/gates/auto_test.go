package gates

import (
	"context"
	"testing"
	"time"
)

// A grandchild holding the output pipe must not keep RunShell past its timeout.
func TestRunShellTimeoutEndsGrandchild(t *testing.T) {
	old := CheckTimeout
	CheckTimeout = 300 * time.Millisecond
	defer func() { CheckTimeout = old }()
	start := time.Now()
	_, err := RunShell(context.Background(), t.TempDir(), "sleep 30 & sleep 30")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("RunShell blocked %s", d)
	}
}
