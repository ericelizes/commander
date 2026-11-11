package gates

import (
	"context"
	"strings"
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

func TestRunShellRetainsBoundedEvidenceTail(t *testing.T) {
	out, err := RunShell(context.Background(), t.TempDir(), `head -c 100000 /dev/zero | tr '\000' x; printf '\nfinal evidence\n'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 20000 || !strings.HasSuffix(out, "final evidence\n") {
		t.Fatalf("check log was unbounded or lost its final evidence: bytes=%d", len(out))
	}
	evidence := TrimOutput(out)
	if len([]rune(evidence)) > 2001 || !strings.HasSuffix(evidence, "final evidence") {
		t.Fatalf("evidence tail changed: %d", len([]rune(evidence)))
	}
}
