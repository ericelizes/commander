package slot

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isolate pins the env a test's Acquire reads: no outer slot, no workstream,
// n slots.
func isolate(t *testing.T, n int) {
	t.Helper()
	t.Setenv(HeldEnv, "")
	t.Setenv(managerRoleEnv, "")
	t.Setenv(CountEnv, strconv.Itoa(n))
}

func TestSecondHolderWaitsAndGetsNotice(t *testing.T) {
	isolate(t, 1)
	home := t.TempDir()
	first, err := Acquire(context.Background(), home, Options{Cmd: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got := make(chan *Holder, 1)
	go func() {
		h, _ := Acquire(context.Background(), home, Options{Cmd: "second", Out: &out, Notice: 50 * time.Millisecond, PollWait: 10 * time.Millisecond})
		got <- h
	}()
	select {
	case <-got:
		t.Fatal("second holder got the slot while the first held it")
	case <-time.After(200 * time.Millisecond):
	}
	first.Release()
	select {
	case h := <-got:
		if h == nil {
			t.Fatal("second acquire failed")
		}
		h.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("second holder never got the slot after release")
	}
	if !strings.Contains(out.String(), "waiting for a build slot (1/1 held:") || !strings.Contains(out.String(), "go test ./...") {
		t.Fatalf("notice = %q", out.String())
	}
}

func TestQueueTimeout(t *testing.T) {
	isolate(t, 1)
	home := t.TempDir()
	first, _ := Acquire(context.Background(), home, Options{})
	defer first.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, home, Options{PollWait: 10 * time.Millisecond}); err == nil {
		t.Fatal("want a timeout error")
	}
}

func TestHolderDeathReleases(t *testing.T) {
	isolate(t, 1)
	home := t.TempDir()
	var out bytes.Buffer
	code, err := Run(context.Background(), home, []string{"sh", "-c", "exit 3"}, 0, &out, &out)
	if err != nil || code != 3 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	// the child's slot is gone: acquiring is immediate
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := Acquire(ctx, home, Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	if _, err := os.Stat(infoPath(home, 0)); !os.IsNotExist(err) {
		t.Fatalf("info file left behind: %v", err)
	}
}

func TestRunTimeoutKillsChild(t *testing.T) {
	isolate(t, 1)
	var out bytes.Buffer
	start := time.Now()
	code, err := Run(context.Background(), t.TempDir(), []string{"sleep", "30"}, 200*time.Millisecond, &out, &out)
	if err == nil || code != 124 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("child not killed")
	}
}

func TestRunSerialisesTwoRuns(t *testing.T) {
	isolate(t, 1)
	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	script := "echo start >> " + log + "; sleep 0.3; echo end >> " + log
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var b bytes.Buffer
			Run(context.Background(), home, []string{"sh", "-c", script}, 0, &b, &b)
			done <- struct{}{}
		}()
	}
	<-done
	<-done
	raw, _ := os.ReadFile(log)
	if got := strings.Fields(string(raw)); strings.Join(got, ",") != "start,end,start,end" {
		t.Fatalf("runs overlapped: %v", got)
	}
}

func TestNestedAcquireInsideSlotDoesNotWait(t *testing.T) {
	home := t.TempDir()
	isolate(t, 1)
	first, _ := Acquire(context.Background(), home, Options{})
	defer first.Release()
	t.Setenv(HeldEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h, err := Acquire(ctx, home, Options{})
	if err != nil {
		t.Fatalf("nested acquire waited: %v", err)
	}
	h.Release()
}

func TestNSlotsRunNConcurrently(t *testing.T) {
	isolate(t, 3)
	home := t.TempDir()
	var held []*Holder
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		h, err := Acquire(ctx, home, Options{Cmd: "suite", PollWait: 10 * time.Millisecond})
		cancel()
		if err != nil {
			t.Fatalf("slot %d of 3 not free: %v", i, err)
		}
		held = append(held, h)
	}
	if got := len(Holders(home, 3)); got != 3 {
		t.Fatalf("Holders = %d, want 3", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, home, Options{PollWait: 10 * time.Millisecond}); err == nil {
		t.Fatal("a fourth holder got in with 3 slots")
	}
	held[1].Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	h, err := Acquire(ctx2, home, Options{PollWait: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("freed slot not reused: %v", err)
	}
	h.Release()
	held[0].Release()
	held[2].Release()
	if !strings.HasPrefix(Status(home), "free") {
		t.Fatalf("Status = %q", Status(home))
	}
}

// With every slot taken and workstream A already holding one, a freed slot goes
// to B (holding none) even though A started waiting first and polls far more
// often.
func TestFairnessWorkstreamHoldingNoneGoesFirst(t *testing.T) {
	isolate(t, 2)
	home := t.TempDir()
	opts := func(ws string) Options {
		poll := 30 * time.Millisecond
		if ws == "ws-a" {
			poll = time.Millisecond
		}
		return Options{Cmd: "suite " + ws, Workstream: ws, Slots: 2, PollWait: poll}
	}
	a1, _ := Acquire(context.Background(), home, opts("ws-a"))
	c1, _ := Acquire(context.Background(), home, opts("ws-c"))

	got := make(chan string, 2)
	wait := func(ws string) {
		h, err := Acquire(context.Background(), home, opts(ws))
		if err != nil {
			return
		}
		got <- ws
		time.Sleep(300 * time.Millisecond)
		h.Release()
	}
	go wait("ws-a")
	time.Sleep(50 * time.Millisecond) // A is in line first
	go wait("ws-b")
	time.Sleep(50 * time.Millisecond)

	c1.Release()
	select {
	case ws := <-got:
		if ws != "ws-b" {
			t.Fatalf("freed slot went to %s, want ws-b (holds none)", ws)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nobody got the freed slot")
	}
	a1.Release()
	select {
	case ws := <-got:
		if ws != "ws-a" {
			t.Fatalf("second slot went to %s, want ws-a", ws)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ws-a never got a slot")
	}
}

// A workstream that holds a slot is not held back when nobody else waits.
func TestFairnessNoOtherWaiterNoYield(t *testing.T) {
	isolate(t, 2)
	home := t.TempDir()
	a1, _ := Acquire(context.Background(), home, Options{Workstream: "ws-a"})
	defer a1.Release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a2, err := Acquire(ctx, home, Options{Workstream: "ws-a", PollWait: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("second slot for ws-a with no other waiter: %v", err)
	}
	a2.Release()
}

func TestStaleWaiterIsIgnored(t *testing.T) {
	isolate(t, 2)
	home := t.TempDir()
	a1, _ := Acquire(context.Background(), home, Options{Workstream: "ws-a"})
	defer a1.Release()
	// a waiter whose process is gone (pid 0 is never alive)
	_ = os.MkdirAll(filepath.Join(home, waitingDir), 0o755)
	_ = os.WriteFile(filepath.Join(home, waitingDir, "0-1"), []byte("ws-b"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a2, err := Acquire(ctx, home, Options{Workstream: "ws-a", PollWait: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("a dead waiter held ws-a back: %v", err)
	}
	a2.Release()
}

func TestCountEnvThenConfigThenDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv(CountEnv, "")
	if got := Count(home); got != DefaultCount() || got < 2 {
		t.Fatalf("default Count = %d, want %d (>= 2)", got, DefaultCount())
	}
	_ = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[concurrency]\nbuild_slots = 7\n"), 0o644)
	if got := Count(home); got != 7 {
		t.Fatalf("config Count = %d, want 7", got)
	}
	t.Setenv(CountEnv, "3")
	if got := Count(home); got != 3 {
		t.Fatalf("env Count = %d, want 3", got)
	}
}

func TestWorkstreamFromManagerRole(t *testing.T) {
	t.Setenv(managerRoleEnv, "manager-il9.15")
	if got := WorkstreamFromEnv(); got != "il9.15" {
		t.Fatalf("WorkstreamFromEnv = %q", got)
	}
}
