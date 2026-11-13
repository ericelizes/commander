package chat

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// overlapConn leaves controls unacknowledged and the process alive until the
// test reports exit. These barriers select lock order without scheduler sleeps.
type overlapConn struct {
	net.Conn
	control, signal, closed             chan struct{}
	onceControl, onceSignal, onceClosed sync.Once
}

func (c *overlapConn) Write(raw []byte) (int, error) {
	var m hostMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, err
	}
	switch m.Op {
	case "in":
		c.onceControl.Do(func() { close(c.control) })
	case "signal":
		if m.Sig == int(syscall.SIGINT) {
			c.onceSignal.Do(func() { close(c.signal) })
		}
	case "close_stdin":
		c.onceClosed.Do(func() { close(c.closed) })
	}
	return len(raw), nil
}
func (c *overlapConn) Close() error { return nil }

func TestInterruptStopOverlapScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/interrupt-stop-overlap.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name         string `json:"name"`
			TimeoutFirst bool   `json:"timeout_first"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 2 || fixture.Cases[0].TimeoutFirst == fixture.Cases[1].TimeoutFirst {
		t.Fatal("need both orders")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) { testInterruptStopOverlap(t, tc.TimeoutFirst) })
	}
}

func testInterruptStopOverlap(t *testing.T, timeoutFirst bool) {
	h := newHarness(t)
	h.e.opts.InterruptGrace = 100 * time.Millisecond
	h.e.opts.StopGrace = time.Second
	h.e.opts.CrashRestart = func(string) bool { return true }
	role := "manager-overlap"
	c, err := h.e.chat(role)
	if err != nil {
		t.Fatal(err)
	}
	conn := &overlapConn{control: make(chan struct{}), signal: make(chan struct{}), closed: make(chan struct{})}
	p := &process{spec: Spec{Role: role}, done: make(chan struct{}), acks: map[string]chan struct{}{}}
	p.hc = &hostConn{c: conn}
	c.mu.Lock()
	c.proc = p
	c.meta.Running = true
	c.tracker.turn = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.proc = nil; c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("transport barrier timed out")
		}
	}
	mailDone := make(chan error, 1)
	go func() { mailDone <- h.e.InterruptForMail(ctx, role, "commander") }()
	await(conn.control)
	if timeoutFirst {
		await(conn.signal)
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- h.e.Stop(ctx, role) }()
	// Stop must accept a mail-stopping process and mark it explicitly stopped.
	select {
	case <-conn.closed:
	case err := <-stopDone:
		t.Fatalf("Stop refused overlapping mail interrupt: %v", err)
	case <-ctx.Done():
		t.Fatal("Stop did not close stdin")
	}
	await(conn.signal)
	if err := <-mailDone; err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	running, noteStop := c.meta.Running, p.noteStop
	c.mu.Unlock()
	// onExit schedules restartInterrupted exactly when noteStop is set. Check
	// before exit so a broken implementation cannot launch a background restart.
	if running || noteStop {
		close(p.done)
		<-stopDone
		t.Fatalf("explicit stop lost: Running=%v noteStop=%v", running, noteStop)
	}
	h.e.onExit(c, p, &hostExit{Signal: "interrupt"})
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if h.e.Running(role) {
		t.Fatal("stopped role running after exit")
	}
}
