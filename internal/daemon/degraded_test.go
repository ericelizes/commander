package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/livedata"
)

type degradedMsg struct {
	key, sender, subject, body, importance string
	to                                     []string
}

type fakeDegradedMail struct{ sent []degradedMsg }

func (f *fakeDegradedMail) SendMessage(_ context.Context, key, sender string, to []string, subject, body string, o agentmail.SendOptions) error {
	f.sent = append(f.sent, degradedMsg{key, sender, subject, body, o.Importance, to})
	return nil
}

type fakeNames map[string]string

func (f fakeNames) GetAmAgent(_ context.Context, _, role string) (string, error) { return f[role], nil }

func degradedAlertFor(t *testing.T, now *time.Time) (*degradedAlert, *fakeDegradedMail) {
	t.Helper()
	m := &fakeDegradedMail{}
	return &degradedAlert{
		home: t.TempDir(), mail: m, key: "/repo", now: func() time.Time { return *now },
		names: fakeNames{"cmdrd": "GoldTiger", "commander": "BlueFox"},
	}, m
}

func TestUnreadableStateIsNotAllOnTrack(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	now := start
	snap := stalledSnap()
	w, _, delivered, _, _ := commanderWatch(snap, start)
	alert, mail := degradedAlertFor(t, &now)
	w.degraded = alert

	// Healthy pass caches the mail route.
	w.pass(context.Background())
	// The store becomes unreadable; the last good data is still around.
	snap.Degraded = &livedata.SnapDegraded{Reason: "SQL logic error: no such table: chat_costs"}
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	now = start.Add(31 * time.Minute)
	w.pass(context.Background())

	for _, d := range *delivered {
		if strings.Contains(d.text, "all on track") {
			t.Fatalf("degraded state reported as on track: %q", d.text)
		}
	}
	if len(*delivered) != 0 {
		t.Fatalf("model check-in delivered while degraded: %v", *delivered)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("sent = %+v", mail.sent)
	}
	got := mail.sent[0]
	if got.importance != "urgent" || got.sender != "GoldTiger" || got.to[0] != "BlueFox" || !strings.Contains(got.body, "no such table") {
		t.Fatalf("mail = %+v", got)
	}
	if _, err := os.Stat(alert.degradedPath()); err != nil {
		t.Fatalf("degraded file missing: %v", err)
	}

	// Repeats are held back for degradedRepeat, then sent again.
	now = now.Add(time.Minute)
	w.pass(context.Background())
	if len(mail.sent) != 1 {
		t.Fatalf("repeat within window: %d", len(mail.sent))
	}
	now = now.Add(degradedRepeat)
	w.pass(context.Background())
	if len(mail.sent) != 2 {
		t.Fatalf("no repeat after window: %d", len(mail.sent))
	}

	// Recovery clears the file.
	snap.Degraded = nil
	w.pass(context.Background())
	if _, err := os.Stat(alert.degradedPath()); !os.IsNotExist(err) {
		t.Fatalf("degraded file kept after recovery: %v", err)
	}
}

func TestStoreErrorInPassRaisesAlert(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	now := start
	w, _, _, _, st := commanderWatch(stalledSnap(), start)
	alert, mail := degradedAlertFor(t, &now)
	w.degraded = alert
	w.pass(context.Background()) // caches the route
	st.listErr = errors.New("unable to open database file")
	w.pass(context.Background())
	if len(mail.sent) != 1 || !strings.Contains(mail.sent[0].body, "unable to open database file") {
		t.Fatalf("sent = %+v", mail.sent)
	}
}

func TestDegradedWithoutRouteStillWritesFile(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	alert, mail := degradedAlertFor(t, &now)
	alert.raise(context.Background(), "boom")
	if len(mail.sent) != 0 {
		t.Fatalf("mailed without a route: %+v", mail.sent)
	}
	if b, err := os.ReadFile(alert.degradedPath()); err != nil || !strings.Contains(string(b), "boom") {
		t.Fatalf("file = %q, %v", b, err)
	}
}

func TestWatchTimeoutStreak(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/watch-read-timeout.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		ConsecutiveTimeouts int `json:"consecutive_timeouts"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil || scenario.ConsecutiveTimeouts != 3 {
		t.Fatalf("invalid scenario: %s", raw)
	}

	now := time.Unix(1_000_000, 0)
	w, _, delivered, _, _ := commanderWatch(stalledSnap(), now)
	alert, mail := degradedAlertFor(t, &now)
	w.degraded = alert
	w.pass(context.Background())
	snap := livedata.Snapshot{Degraded: &livedata.SnapDegraded{Reason: "context deadline exceeded", Timeout: true}}
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	w.pass(context.Background())
	if len(mail.sent) != 0 {
		t.Fatal("isolated timeout alerted")
	}
	if _, err := os.Stat(alert.degradedPath()); !os.IsNotExist(err) {
		t.Fatal("isolated timeout recorded degradation")
	}
	snap.Degraded = nil
	w.pass(context.Background())
	snap.Degraded = &livedata.SnapDegraded{Reason: "context deadline exceeded", Timeout: true}
	for i := 0; i < scenario.ConsecutiveTimeouts-1; i++ {
		now = now.Add(time.Minute)
		w.pass(context.Background())
	}
	if len(mail.sent) != 0 {
		t.Fatal("healthy read did not reset streak")
	}
	now = now.Add(time.Minute)
	w.pass(context.Background())
	if len(mail.sent) != 1 || !strings.Contains(mail.sent[0].subject, "timeout") || strings.Contains(mail.sent[0].body, "schema") {
		t.Fatalf("timeout mail: %+v", mail.sent)
	}
	if len(*delivered) != 0 {
		t.Fatal("check-in delivered using timed out state")
	}
	// A real error remains immediate even after a timeout alert.
	snap.Degraded = &livedata.SnapDegraded{Reason: "database is locked"}
	w.pass(context.Background())
	if len(mail.sent) != 2 || !strings.Contains(mail.sent[1].subject, "error") {
		t.Fatalf("error mail: %+v", mail.sent)
	}
}

func TestReadErrorAfterOneTimeoutAlertsImmediately(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	alert, mail := degradedAlertFor(t, &now)
	alert.refreshRoute(context.Background())
	alert.readFailure(context.Background(), "deadline exceeded", true)
	alert.readFailure(context.Background(), "schema too new", false)
	if len(mail.sent) != 1 || !strings.Contains(mail.sent[0].subject, "error") || !strings.Contains(mail.sent[0].body, "schema too new") {
		t.Fatalf("real error delayed: %+v", mail.sent)
	}
}
