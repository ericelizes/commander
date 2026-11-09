package inbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentmail"
)

// fakeClient wraps agentmail.MockClient with call-count tracking so tests can
// wait for "the loop has run at least once" without sleeping for an arbitrary
// duration. The first call returns a scripted result; subsequent calls return
// empty so the test's assertion budget isn't drowned in repeats.
type fakeClient struct {
	*agentmail.MockClient
	calls atomic.Int64
}

func (f *fakeClient) Inbox(ctx context.Context, project, agent string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error) {
	n := f.calls.Add(1)
	if n == 1 {
		return f.MockClient.Inbox(ctx, project, agent, since, opts)
	}
	// After the first poll, return nothing so the Out channel stops growing.
	return nil, nil
}

// TestPoller_Happy confirms two scripted mails surface as two Notifications
// on Out within a short polling window.
func TestPoller_Happy(t *testing.T) {
	mock := &agentmail.MockClient{
		InboxResult: []agentmail.Mail{
			{
				From:       "lead-frontend",
				To:         "commander",
				Subject:    "blocker on cmdr-5pw",
				Body:       "operator wedged",
				Importance: "high",
				SentAt:     time.Now(),
			},
			{
				From:       "lead-dotfiles",
				To:         "commander",
				Subject:    "evolve done",
				Body:       "no patterns to adopt",
				Importance: "normal",
				SentAt:     time.Now(),
			},
		},
	}
	fc := &fakeClient{MockClient: mock}

	p := NewPoller(fc, "commander", "commander", 20*time.Millisecond)
	p.Start(context.Background())
	t.Cleanup(p.Stop)

	deadline := time.After(500 * time.Millisecond)
	got := []Notification{}
	for len(got) < 2 {
		select {
		case n := <-p.Out:
			got = append(got, n)
		case <-deadline:
			t.Fatalf("timed out waiting for 2 notifications, got %d", len(got))
		}
	}

	if got[0].Subject != "blocker on cmdr-5pw" {
		t.Errorf("first notification subject = %q, want %q", got[0].Subject, "blocker on cmdr-5pw")
	}
	if got[0].Project != "commander" {
		t.Errorf("first notification project = %q, want %q", got[0].Project, "commander")
	}
	if got[0].Importance != "high" {
		t.Errorf("first notification importance = %q, want %q", got[0].Importance, "high")
	}
	if got[1].Subject != "evolve done" {
		t.Errorf("second notification subject = %q, want %q", got[1].Subject, "evolve done")
	}
}

// TestPoller_Empty confirms the loop survives an empty Inbox response without
// emitting notifications or panicking. We give the goroutine a couple of
// ticks then assert no mail surfaced.
func TestPoller_Empty(t *testing.T) {
	mock := &agentmail.MockClient{
		InboxResult: nil,
	}

	p := NewPoller(mock, "commander", "commander", 10*time.Millisecond)
	p.Start(context.Background())
	t.Cleanup(p.Stop)

	// Wait for at least 3 ticks worth of time so we're sure the loop ran.
	time.Sleep(50 * time.Millisecond)

	select {
	case n := <-p.Out:
		t.Fatalf("expected no notifications, got %+v", n)
	default:
		// Expected path.
	}
}

// TestPoller_Stop confirms Stop blocks until the goroutine exits and Out is
// closed (a closed channel reads as a zero value with ok=false).
func TestPoller_Stop(t *testing.T) {
	mock := &agentmail.MockClient{}

	p := NewPoller(mock, "commander", "commander", 10*time.Millisecond)
	p.Start(context.Background())

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return within 1s")
	}

	// Out should be closed: a receive returns ok=false immediately.
	if _, ok := <-p.Out; ok {
		t.Fatal("expected Out to be closed after Stop")
	}
}
