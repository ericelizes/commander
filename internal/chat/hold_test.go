package chat

import (
	"context"
	"testing"
	"time"
)

func TestMailHoldFollowsUserAndLapses(t *testing.T) {
	home := t.TempDir()
	e := &Engine{opts: Options{Home: home}}
	if e.Talking("commander") {
		t.Fatal("talking before the user said anything")
	}
	if err := e.MarkTalking("commander"); err != nil {
		t.Fatal(err)
	}
	if !e.Talking("commander") || e.Talking("manager-x") {
		t.Fatal("hold is per role")
	}
	if Talking(home, "commander", time.Now().Add(MailHold+time.Second)) {
		t.Fatal("hold did not lapse after MailHold")
	}
	if err := e.ReleaseMail("commander"); err != nil {
		t.Fatal(err)
	}
	if e.Talking("commander") {
		t.Fatal("released hold still holds")
	}
	if err := e.MarkTalking("commander"); err != nil {
		t.Fatal(err)
	}
	if e.Talking("commander") {
		t.Fatal("typing after the release took the click back")
	}
	e.MailDelivered("commander")
	if !e.Talking("commander") {
		t.Fatal("after the mail went in, the user talking holds the next mail again")
	}
}

func TestWaitResumedOpensOnceChatsArePickedBackUp(t *testing.T) {
	e := New(Options{Home: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.WaitResumed(ctx); err != nil {
		t.Fatalf("before any resume: %v", err)
	}
	e.ResumeRunning(ctx)
	if err := e.WaitResumed(ctx); err != nil {
		t.Fatalf("nothing to resume should open at once: %v", err)
	}
}

func TestWaitRunningWaitsOutAStartInFlight(t *testing.T) {
	e := New(Options{Home: t.TempDir()})
	ctx := context.Background()
	if e.WaitRunning(ctx, "commander", time.Second) {
		t.Fatal("nothing starting: must not be running")
	}
	c, _ := e.chat("commander")
	c.mu.Lock()
	c.starting = true
	c.mu.Unlock()
	t0 := time.Now()
	if e.WaitRunning(ctx, "commander", 200*time.Millisecond) || time.Since(t0) < 150*time.Millisecond {
		t.Fatal("a start in flight should be waited for, then give up at the deadline")
	}
}
