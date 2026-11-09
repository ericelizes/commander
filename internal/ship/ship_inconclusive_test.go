package ship

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// A stop or an expired wait is no verdict on the branch; a refusal is one.
func TestInconclusive(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(cancelled, Options{Root: t.TempDir(), Branch: "feat"}, io.Discard); !errors.As(err, &Inconclusive{}) {
		t.Errorf("a stopped ship: %v", err)
	}
	if err := Run(context.Background(), Options{Root: t.TempDir(), Branch: "main"}, io.Discard); err == nil || errors.As(err, &Inconclusive{}) {
		t.Errorf("a refusal: %v", err)
	}
}

// The hold running out while the ship waits for threads: the caller's context is still alive,
// so only the wait's own site can say this is no verdict.
func TestInconclusiveThreadWait(t *testing.T) {
	dir := newRepo(t)
	released := false
	run := func(context.Context, string, string, ...string) (string, error) {
		t.Error("a check ran with the wait cut short")
		return "", nil
	}
	err := Run(context.Background(), Options{Root: dir, Branch: "feat", Run: run, Exclusive: exclusiveStub(&released, nil),
		HoldBudget: 100 * time.Millisecond, Threads: func() int { return 9999 },
		Sleep: func(ctx context.Context, _ time.Duration) { <-ctx.Done() }}, io.Discard)
	if !errors.As(err, &Inconclusive{}) || !strings.Contains(err.Error(), "the wait for threads ended") {
		t.Errorf("err = %v", err)
	}
}
