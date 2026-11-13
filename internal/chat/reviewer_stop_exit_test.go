package chat

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

// Independent review reproducer (operator-exec-vm-12-420004332b82, independently
// confirmed by operator-exec-vm-9-de1639fc1517): Stop arrives after onExit snapshots noteStop.
func TestReviewerStopDuringMailExit(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/interrupt-stop-exit.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Role    string `json:"role"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Role == "" || scenario.Message == "" {
		t.Fatal("missing fake-host scenario inputs")
	}
	h := newHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	h.opts.CrashRestart = func(string) bool { once.Do(func() { close(entered); <-release }); return true }
	h.opts.CrashRestartDelay = 10 * time.Millisecond
	h.opts.InterruptGrace = 30 * time.Millisecond
	h.e = New(h.opts)
	role := scenario.Role
	h.role(role)
	h.start(role)
	sub := h.subscribe(role)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.e.Send(ctx, role, scenario.Message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindTextDelta))
	if err := h.e.InterruptForMail(ctx, role, "commander"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("exit barrier timed out")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.e.Stop(ctx, role) }()
	c, _ := h.e.chat(role)
	for {
		c.mu.Lock()
		running := c.meta.Running
		c.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-ctx.Done():
			unblock()
			t.Fatal("Stop did not mark stopped")
		case <-time.After(time.Millisecond):
		}
	}
	unblock()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindNotice && e.Subtype == "interrupt_restart" })
	if h.e.Running(role) {
		if err := h.e.Stop(ctx, role); err != nil {
			t.Fatal(err)
		}
		t.Fatal("explicit Stop lost: onExit used captured noteStop and restarted the stopped role")
	}
}

// Once a restart has passed admission, Stop waits for its start and then stops
// the replacement. Checking Running before start without serialization misses this.
func TestStopDuringAdmittedMailRestart(t *testing.T) {
	h := newHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(release) }) }
	defer unblock()
	resolver := h.opts.Resolver
	calls := 0
	h.opts.Resolver = ResolverFunc(func(ctx context.Context, role string) (Spec, error) {
		calls++
		if calls == 2 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Spec{}, ctx.Err()
			}
		}
		return resolver.Resolve(ctx, role)
	})
	h.opts.CrashRestart = func(string) bool { return true }
	h.opts.CrashRestartDelay = time.Millisecond
	h.opts.InterruptGrace = 30 * time.Millisecond
	h.e = New(h.opts)
	role := "manager-admitted-stop"
	h.role(role)
	h.start(role)
	sub := h.subscribe(role)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.e.Send(ctx, role, "deaf long"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, isKind(KindTextDelta))
	if err := h.e.InterruptForMail(ctx, role, "commander"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("restart never reached resolver")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.e.Stop(ctx, role) }()
	unblock()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Stop did not finish")
	}
	if h.e.Running(role) {
		t.Fatal("Stop left the admitted replacement running")
	}
}
