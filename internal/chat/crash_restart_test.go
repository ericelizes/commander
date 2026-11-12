package chat

import (
	"context"
	"os"
	"path/filepath"
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

// A mail interrupt whose turn is deaf to the control interrupt ends by
// SIGINT. For a role that comes back after a crash (commander, manager-*) that
// is an expected stop: the role is started again on the real restart path,
// its first message is the kept [commander interrupt] note, once, and it is
// never told it crashed or to carry on where it left off.
func TestMailInterruptSIGINTRestartSendsOnlyTheNote(t *testing.T) {
	h := newHarness(t)
	h.opts.CrashRestart = func(role string) bool { return strings.HasPrefix(role, "manager-") }
	h.opts.CrashRestartDelay = 10 * time.Millisecond
	h.opts.InterruptGrace = 300 * time.Millisecond
	h.e = New(h.opts)
	h.role("manager-r")
	h.start("manager-r")
	sub := h.subscribe("manager-r")
	_ = h.e.Send(context.Background(), "manager-r", "deaf long")
	waitFor(t, sub, isKind(KindTextDelta))
	if err := h.e.InterruptForMail(context.Background(), "manager-r", "commander"); err != nil {
		t.Fatal(err)
	}
	seen := waitFor(t, sub, isKind(KindExit))
	if ex := kinds(seen, KindExit)[0].Exit; ex == nil || !ex.Expected {
		t.Fatalf("the SIGINT fallback of a mail interrupt is not an expected stop: %+v", ex)
	}
	note := InterruptNote("commander", "manager-r")
	seen = waitFor(t, sub, func(e Event) bool { return e.Kind == KindUser && e.Text == note })
	seen = append(seen, waitFor(t, sub, isKind(KindResult))...)
	var users int
	for _, e := range seen {
		switch {
		case e.Kind == KindUser:
			users++
		case e.Kind == KindNotice && e.Subtype == "crash_restart":
			t.Fatalf("an interrupt restart read as a crash: %s", summarize(seen))
		}
		if e.Kind == KindUser && (e.Text == CrashResumeMessage || e.Text == ResumeMessage) {
			t.Fatalf("a resume message followed the note: %s", summarize(seen))
		}
	}
	if users != 1 {
		t.Fatalf("want only the note at the restart, got %s", summarize(seen))
	}
	if !h.e.Running("manager-r") {
		t.Fatal("the interrupted Manager was not restarted")
	}
	if _, err := os.Stat(InterruptNoteFile(h.home, "manager-r")); err == nil {
		t.Fatal("the kept note was not consumed")
	}
}

// A role that dies mid-turn while an interrupt note is kept gets the note at
// its crash restart and no "[resume] ... crashed" message after it.
func TestCrashRestartWithAKeptNoteSendsNoResumeMessage(t *testing.T) {
	h := crashHarness(t)
	h.role("commander")
	h.start("commander")
	sub := h.subscribe("commander")
	note := InterruptNote("manager-x", "commander")
	path := InterruptNoteFile(h.home, "commander")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(note), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = h.e.Send(context.Background(), "commander", "crash now")
	seen := waitFor(t, sub, func(e Event) bool { return e.Kind == KindUser && e.Text == note })
	seen = append(seen, waitFor(t, sub, isKind(KindResult))...)
	for _, e := range seen {
		if e.Kind == KindUser && e.Text == CrashResumeMessage {
			t.Fatalf("the crash resume message went after the note: %s", summarize(seen))
		}
	}
}
