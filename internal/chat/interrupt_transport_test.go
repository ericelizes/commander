package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
)

// ackThenFailConn acknowledges a real control envelope, then rejects the
// following user envelope while the Engine still considers the process live.
type ackThenFailConn struct {
	net.Conn
	p        *process
	fail     bool
	controls int
	notes    int
}

func (c *ackThenFailConn) Write(raw []byte) (int, error) {
	var envelope hostMsg
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0, err
	}
	var msg struct {
		Type string `json:"type"`
		ID   string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(envelope.Line), &msg); err != nil {
		return 0, err
	}
	if msg.Type == "control_request" {
		c.controls++
		c.p.pmu.Lock()
		close(c.p.acks[msg.ID])
		delete(c.p.acks, msg.ID)
		c.p.pmu.Unlock()
	} else {
		if c.fail {
			return 0, net.ErrClosed
		}
		c.notes++
	}
	return len(raw), nil
}

func TestInterruptForMailPostACKTransportFailure(t *testing.T) {
	h := newHarness(t)
	role := "manager-transport"
	c, err := h.e.chat(role)
	if err != nil {
		t.Fatal(err)
	}
	p := &process{spec: Spec{Role: role}, done: make(chan struct{}), acks: map[string]chan struct{}{}}
	conn := &ackThenFailConn{p: p, fail: true}
	p.hc = &hostConn{c: conn}
	c.mu.Lock()
	c.proc = p
	c.tracker.turn = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.proc = nil; c.mu.Unlock() }()

	err = h.e.InterruptForMail(context.Background(), role, "commander")
	if !errors.Is(err, errHostGone) {
		t.Fatalf("want transport error, got %v", err)
	}
	note := InterruptNote("commander", role)
	raw, err := os.ReadFile(InterruptNoteFile(h.home, role))
	if err != nil || string(raw) != note {
		t.Fatalf("lost note: %q, %v", raw, err)
	}
	// A still-broken replacement retains the claimed note too.
	if h.e.sendPendingInterruptNote(role) {
		t.Fatal("failed send reported success")
	}
	if raw, err := os.ReadFile(InterruptNoteFile(h.home, role)); err != nil || string(raw) != note {
		t.Fatalf("retry lost note: %q, %v", raw, err)
	}
	conn.fail = false
	if !h.e.sendPendingInterruptNote(role) {
		t.Fatal("replacement did not consume note")
	}
	if h.e.sendPendingInterruptNote(role) {
		t.Fatal("note sent twice")
	}
	if conn.controls != 1 || conn.notes != 1 {
		t.Fatalf("controls=%d notes=%d", conn.controls, conn.notes)
	}
	// Successful immediate delivery must continue to suppress future replay.
	if err := h.e.InterruptForMail(context.Background(), role, "commander"); err != nil {
		t.Fatal(err)
	}
	if h.e.sendPendingInterruptNote(role) {
		t.Fatal("successful immediate note replayed")
	}
	if conn.controls != 2 || conn.notes != 2 {
		t.Fatalf("controls=%d notes=%d", conn.controls, conn.notes)
	}
}

// This scenario runs here because recovery's persisted metadata is private to
// chat. The spawned provider is chattest, never a user's runtime.
func TestInterruptNoteRecoveryScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/interrupt-note-recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Role   string `json:"role"`
			Sender string `json:"sender"`
			Keep   bool   `json:"keep_note"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 2 {
		t.Fatal("missing recovery/control cases")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Role, func(t *testing.T) {
			h := newHarness(t)
			h.seedLog(tc.Role, Event{Kind: KindUser, Source: SourceEngine, Text: "continue the invented task", Origin: "user"},
				Event{Kind: KindToolInput, Source: SourceEngine, Tool: &Tool{ID: "tool-demo", Name: "Bash", Input: json.RawMessage(`{"command":"pwd"}`)}})
			note := InterruptNote(tc.Sender, tc.Role)
			if tc.Keep {
				if err := h.e.keepInterruptNote(tc.Role, note); err != nil {
					t.Fatal(err)
				}
			}
			h.e.Close()
			h.e = New(h.opts)
			sub := h.subscribe(tc.Role)
			h.e.ResumeRunning(context.Background())
			seen := waitFor(t, sub, isKind(KindResult))
			users := 0
			want := ResumeMessage
			if tc.Keep {
				want = note
			}
			for _, ev := range seen {
				if ev.Kind == KindUser {
					users++
					if ev.Text != want || ev.Origin != "mail" {
						t.Fatalf("unexpected recovery message: %+v", ev)
					}
				}
			}
			if users != 1 {
				t.Fatalf("want one recovery message, got %s", summarize(seen))
			}
			if h.e.sendPendingInterruptNote(tc.Role) {
				t.Fatal("replayed consumed note")
			}
		})
	}
}
