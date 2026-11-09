package chat

import (
	"context"
	"strings"
	"testing"
	"time"
)

func crashHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.opts.CrashRestart = func(role string) bool { return role == "commander" }
	h.opts.CrashRestartDelay = 10 * time.Millisecond
	h.e = New(h.opts)
	return h
}

func TestCrashedCommanderComesBackAndCarriesOn(t *testing.T) {
	h := crashHarness(t)
	h.role("commander")
	h.start("commander")
	sub := h.subscribe("commander")
	_ = h.e.Send(context.Background(), "commander", "crash now")
	waitFor(t, sub, func(e Event) bool {
		return e.Kind == KindUser && e.Text == CrashResumeMessage
	})
	if !h.e.Running("commander") {
		t.Fatal("commander should be running again")
	}
}

func TestCrashLoopStopsRestarting(t *testing.T) {
	h := crashHarness(t)
	h.role("commander")
	h.start("commander")
	sub := h.subscribe("commander")
	for i := 0; i < maxCrashRestarts; i++ {
		_ = h.e.Send(context.Background(), "commander", "crash now")
		waitFor(t, sub, func(e Event) bool { return e.Kind == KindUser && e.Text == CrashResumeMessage })
	}
	_ = h.e.Send(context.Background(), "commander", "crash now")
	waitFor(t, sub, func(e Event) bool {
		return e.Kind == KindNotice && strings.HasPrefix(e.Text, "Not restarting")
	})
	time.Sleep(50 * time.Millisecond)
	if h.e.Running("commander") {
		t.Fatal("a role crashing on every start must stay down")
	}
}

func TestStoppedCommanderStaysStopped(t *testing.T) {
	h := crashHarness(t)
	h.role("commander")
	h.start("commander")
	if err := h.e.Stop(context.Background(), "commander"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if h.e.Running("commander") {
		t.Fatal("a stop is not a crash")
	}
}
