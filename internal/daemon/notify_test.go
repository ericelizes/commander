package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/inbox"
	"github.com/example-user/commander/internal/notify"
)

// TestDispatch_CeilingHitSubject covers the subject-prefix branch: a mail
// shaped like ceilings.notifyLead's emit ("operator <name> killed: <reason>")
// should fire exactly one OS notification with title=From and a "subject ·
// body" body. We assert:
//   - exactly one Notify call (not double-emitted via the urgent branch)
//   - title is the sender ("watchdog")
//   - body contains both subject and body, joined with " · "
func TestDispatch_CeilingHitSubject(t *testing.T) {
	mock := &notify.MockNotifier{}
	d := &Daemon{notifier: mock, notifyEnabled: true}

	d.dispatchNotification(context.Background(), inbox.Notification{
		From:       "watchdog",
		Subject:    "operator operator-x killed: tokens-ceiling",
		Body:       "Tokens used: 200000",
		Importance: "urgent", // also urgent — must not double-emit
		Project:    "demo",
	})

	calls := mock.Snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls: want 1, got %d (%+v)", len(calls), calls)
	}
	if calls[0].Title != "watchdog" {
		t.Errorf("title: want watchdog, got %q", calls[0].Title)
	}
	wantBody := "operator operator-x killed: tokens-ceiling · Tokens used: 200000"
	if calls[0].Body != wantBody {
		t.Errorf("body: want %q, got %q", wantBody, calls[0].Body)
	}
}

// TestDispatch_UrgentMail exercises the second branch — non-ceiling mail
// flagged Importance="urgent". Title prefix must include the project so the
// human can disambiguate which project woke them up.
func TestDispatch_UrgentMail(t *testing.T) {
	mock := &notify.MockNotifier{}
	d := &Daemon{notifier: mock, notifyEnabled: true}

	d.dispatchNotification(context.Background(), inbox.Notification{
		From:       "lead-demo",
		Subject:    "blocker on auth flow",
		Body:       "need user eyes",
		Importance: "urgent",
		Project:    "demo",
	})

	calls := mock.Snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls: want 1, got %d", len(calls))
	}
	if calls[0].Title != "commander · demo" {
		t.Errorf("title: want %q, got %q", "commander · demo", calls[0].Title)
	}
	if calls[0].Body != "lead-demo: blocker on auth flow" {
		t.Errorf("body: %q", calls[0].Body)
	}
}

// TestDispatch_NormalMailIgnored asserts the dispatcher does NOT pop a
// notification for ordinary chatter. Most mail is normal — over-notifying
// trains the human to ignore Notification Center, defeating the point.
func TestDispatch_NormalMailIgnored(t *testing.T) {
	mock := &notify.MockNotifier{}
	d := &Daemon{notifier: mock, notifyEnabled: true}

	d.dispatchNotification(context.Background(), inbox.Notification{
		From:       "operator-x",
		Subject:    "status update",
		Body:       "still working",
		Importance: "normal",
		Project:    "demo",
	})

	if calls := mock.Snapshot(); len(calls) != 0 {
		t.Fatalf("expected 0 notifications for normal mail, got %d (%+v)", len(calls), calls)
	}
}

// TestDispatch_DisabledFlagSuppresses verifies the --notifications=false
// flag actually short-circuits the fan-out, even for ceiling-hit mail.
// This is the contract the headless / CI runs depend on.
func TestDispatch_DisabledFlagSuppresses(t *testing.T) {
	mock := &notify.MockNotifier{}
	d := &Daemon{notifier: mock, notifyEnabled: false}

	// drainInbox is the gate; calling dispatchNotification directly would
	// bypass the flag check (by design — dispatch is the unit, drain is
	// the policy). Assert via drainInbox semantics: send one notification
	// through a real inbox poller's Out channel and confirm nothing fires.
	pl := &inbox.Poller{Out: make(chan inbox.Notification, 1)}
	d.inbox = pl
	pl.Out <- inbox.Notification{
		From:       "watchdog",
		Subject:    "operator operator-x killed: budget",
		Importance: "urgent",
	}
	close(pl.Out)

	d.drainInbox(context.Background())

	if calls := mock.Snapshot(); len(calls) != 0 {
		t.Fatalf("flag=false: expected 0 notifications, got %d", len(calls))
	}
}

type fakeRoles map[string]string

func (f fakeRoles) GetAmAgent(_ context.Context, key, role string) (string, error) {
	return f[key+"|"+role], nil
}

func TestServerInbox_UnmappedRoleIsQuietNotAnError(t *testing.T) {
	si := &serverInbox{roles: fakeRoles{}, key: "/repo"}
	mail, err := si.Inbox(context.Background(), "ignored", "commander", time.Time{}, agentmail.InboxOptions{})
	if err != nil || mail != nil {
		t.Fatalf("mail=%v err=%v; an unmapped role should just have no mail", mail, err)
	}
}
