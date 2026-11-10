package ship

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// runFakeShip isolates command/branch/queue fixtures from the shared host's
// resource load. Admission tests must call Run directly with their own Threads
// and Sleep callbacks (see resources_test.go and ship_inconclusive_test.go).
func runFakeShip(ctx context.Context, opts Options, out io.Writer) error {
	opts.Threads = func() int { return 0 }
	return Run(ctx, opts, out)
}

func TestFakeShipDoesNotConsultOverloadedHost(t *testing.T) {
	dir := newRepo(t)
	hostCalls, checks := 0, 0
	limit := defaultMaxThreads
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runFakeShip(ctx, Options{
		Root: dir, Branch: "feat", MaxThreads: &limit,
		// Stand in for a host counter reporting heavy fleet load. The fake-command
		// fixture must replace this dependency before production admission runs.
		Threads: func() int { hostCalls++; return 9999 },
		Sleep:   func(context.Context, time.Duration) { t.Fatal("fake ship waited for host resources") },
		Run:     func(context.Context, string, string, ...string) (string, error) { checks++; return "", nil },
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if hostCalls != 0 || checks == 0 {
		t.Fatalf("host calls=%d, checks=%d", hostCalls, checks)
	}
	if strings.Contains(out.String(), "waiting for the machine") {
		t.Fatalf("unexpected admission wait: %s", &out)
	}
	if git(t, dir, "rev-parse", "main") != git(t, dir, "rev-parse", "feat") {
		t.Fatal("fake scenario did not finish merging")
	}
}
