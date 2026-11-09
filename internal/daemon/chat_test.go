package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/wsmgr"
)

type recordingDeliverer struct{ got []string }

func (r *recordingDeliverer) Deliver(_ context.Context, role, prompt string) error {
	r.got = append(r.got, role+": "+prompt)
	return nil
}

func TestChatDelivererFallsBackToTmux(t *testing.T) {
	tm := &recordingDeliverer{}
	d := chatDeliverer{chat: chat.New(chat.Options{Home: t.TempDir()}), tmux: tm}
	if err := d.Deliver(context.Background(), "manager-x", "[mail] hi"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeliverStale(context.Background(), "commander", "[mail] old"); err != nil {
		t.Fatal(err)
	}
	if len(tm.got) != 2 || tm.got[0] != "manager-x: [mail] hi" {
		t.Fatalf("tmux got %v", tm.got)
	}
}

type fakeEnsurer struct {
	roles  []string
	engine *fakeEngine
	err    error
}

func (f *fakeEnsurer) EnsureRole(_ context.Context, role string) error {
	f.roles = append(f.roles, role)
	if f.err != nil {
		return f.err
	}
	if f.engine != nil {
		_, err := f.engine.Start(context.Background(), role, chat.StartOptions{})
		return err
	}
	return nil
}

func TestChatDelivererStartsAnIdleManagerBeforeFallingBack(t *testing.T) {
	tm, en := &recordingDeliverer{}, &fakeEnsurer{}
	d := chatDeliverer{chat: &fakeEngine{startErr: errors.New("no")}, tmux: tm, managers: en}
	_ = d.Deliver(context.Background(), "manager-x", "[mail] hi")
	_ = d.Deliver(context.Background(), "commander", "[mail] hi")
	if len(en.roles) != 1 || en.roles[0] != "manager-x" {
		t.Fatalf("ensured %v, want only manager-x", en.roles)
	}
}

type failingDeliverer struct{}

func (failingDeliverer) Deliver(context.Context, string, string) error {
	return errors.New("commander tmux session not running")
}

// fakeEngine scripts the chat engine: running flips on after runAfterWait
// (a start in flight finishing late) or after a successful Start.
type fakeEngine struct {
	running     bool
	waitBecomes bool // WaitRunning finds it running
	startErr    error
	startRuns   bool // a successful Start leaves it running
	starts      int
	delivered   []string
}

func (f *fakeEngine) Running(string) bool { return f.running }
func (f *fakeEngine) WaitRunning(context.Context, string, time.Duration) bool {
	if f.waitBecomes {
		f.running = true
	}
	return f.running
}
func (f *fakeEngine) Start(context.Context, string, chat.StartOptions) (chat.Status, error) {
	f.starts++
	if f.startErr == nil && f.startRuns {
		f.running = true
	}
	return chat.Status{}, f.startErr
}
func (f *fakeEngine) Talking(string) bool { return false }
func (f *fakeEngine) DeliverMail(_ context.Context, _, text string) error {
	f.delivered = append(f.delivered, text)
	return nil
}
func (f *fakeEngine) MailDelivered(string) {}

func wakeCase(t *testing.T, en *fakeEngine) (*recordingDeliverer, error) {
	t.Helper()
	tm := &recordingDeliverer{}
	d := chatDeliverer{chat: en, tmux: tm, managers: &fakeEnsurer{}}
	return tm, d.Deliver(context.Background(), "manager-x", "[mail] hi")
}

func TestWake_StartInFlightFinishesLate(t *testing.T) {
	en := &fakeEngine{waitBecomes: true}
	tm, err := wakeCase(t, en)
	if err != nil || en.starts != 0 || len(en.delivered) != 1 || len(tm.got) != 0 {
		t.Fatalf("err=%v starts=%d delivered=%v tmux=%v", err, en.starts, en.delivered, tm.got)
	}
}

func TestWake_InvalidRoleNeverBypassesOwnershipValidation(t *testing.T) {
	en := &fakeEngine{running: true, startRuns: true}
	tm := &recordingDeliverer{}
	mgr := &fakeEnsurer{err: wsmgr.ErrInvalidRoute}
	d := chatDeliverer{chat: en, tmux: tm, managers: mgr}
	for _, deliver := range []func(context.Context, string, string) error{d.Deliver, d.DeliverStale} {
		err := deliver(context.Background(), "manager-nested", "mail")
		var blocked *wake.BlockedError
		if !errors.Is(err, wsmgr.ErrInvalidRoute) || !errors.As(err, &blocked) || en.starts != 0 || len(tm.got) != 0 {
			t.Fatalf("err=%v starts=%d tmux=%v", err, en.starts, tm.got)
		}
	}
}

func TestWake_TerminalHolderRequiresExplicitHandover(t *testing.T) {
	en := &fakeEngine{}
	tm := &recordingDeliverer{}
	mgr := &fakeEnsurer{err: fmt.Errorf("%w: %w", wsmgr.ErrSessionHeld, &chat.OwnedError{By: "terminal", PID: 7})}
	d := chatDeliverer{chat: en, tmux: tm, managers: mgr}
	err := d.Deliver(context.Background(), "manager-x", "mail")
	var blocked *wake.BlockedError
	if !errors.As(err, &blocked) || !strings.Contains(err.Error(), "authorize a handover") || en.starts != 0 || len(tm.got) != 0 {
		t.Fatalf("err=%v starts=%d tmux=%v", err, en.starts, tm.got)
	}
	// The actual holder releases ownership; a newly validated start can deliver.
	mgr.err, mgr.engine, en.startRuns = nil, en, true
	if err := d.Deliver(context.Background(), "manager-x", "mail"); err != nil || len(en.delivered) != 1 {
		t.Fatalf("ownership recovery: err=%v delivered=%v", err, en.delivered)
	}
}

func TestWake_ValidatedStartFailureIsNotRetriedDirectly(t *testing.T) {
	en := &fakeEngine{startErr: errors.New("claude missing")}
	tm := &recordingDeliverer{}
	d := chatDeliverer{chat: en, tmux: tm, managers: &fakeEnsurer{engine: en}}
	err := d.Deliver(context.Background(), "manager-x", "mail")
	if err == nil || !strings.Contains(err.Error(), "claude missing") || en.starts != 1 || len(tm.got) != 0 {
		t.Fatalf("err=%v starts=%d tmux=%v", err, en.starts, tm.got)
	}
}

func TestWake_ValidatedStartSucceeds(t *testing.T) {
	en := &fakeEngine{startRuns: true}
	d := chatDeliverer{chat: en, tmux: &recordingDeliverer{}, managers: &fakeEnsurer{engine: en}}
	if err := d.Deliver(context.Background(), "manager-x", "mail"); err != nil || en.starts != 1 || len(en.delivered) != 1 {
		t.Fatalf("err=%v starts=%d delivered=%v", err, en.starts, en.delivered)
	}
}
