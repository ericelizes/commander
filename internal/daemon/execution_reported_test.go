package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

type fakeReported struct{ all []store.ReportedRun }

func (f fakeReported) ReportedRuns(context.Context) ([]store.ReportedRun, error) { return f.all, nil }

type reportedRig struct {
	c       *executionReported
	clock   time.Time
	st      chat.Status
	stopErr error
	stopped []string
}

func newReportedRig() *reportedRig {
	r := &reportedRig{clock: time.Unix(5000, 0), st: chat.Status{Running: true, State: store.AgentIdle, LastSeq: 7}}
	f := fakeReported{all: []store.ReportedRun{{ProjectID: 1, BeadID: "b", RunID: 3, Agent: "operator-x", ReportedAt: 1000}}}
	r.c = &executionReported{store: f, now: func() time.Time { return r.clock },
		status: func(string) (chat.Status, error) { return r.st, nil },
		stop:   func(_ context.Context, role string) error { r.stopped = append(r.stopped, role); return r.stopErr }}
	return r
}

func (r *reportedRig) passAfter(d time.Duration) {
	r.clock = r.clock.Add(d)
	r.c.pass(context.Background())
}

func TestIdleRunAfterFinalReportIsEnded(t *testing.T) {
	r := newReportedRig()
	r.passAfter(0)
	r.passAfter(ReportGrace - time.Second)
	if len(r.stopped) != 0 {
		t.Fatalf("ended inside the grace: %v", r.stopped)
	}
	r.passAfter(time.Second)
	if len(r.stopped) != 1 || r.stopped[0] != "operator-x" {
		t.Fatalf("stopped: %v", r.stopped)
	}
}

func TestActiveRunAfterFinalReportIsNotEnded(t *testing.T) {
	r := newReportedRig()
	r.passAfter(0)
	// A turn in flight (a tool call) for far longer than the grace.
	r.st.State, r.st.Detail = store.AgentWorking, "Bash"
	for i := 0; i < 5; i++ {
		r.passAfter(2 * time.Minute)
	}
	// Idle again, but only just: the timer restarted at the last busy pass.
	r.st.State = store.AgentIdle
	r.passAfter(time.Minute)
	// Quiet state but events moved (output, commit): activity resets the timer.
	r.st.LastSeq++
	r.passAfter(ReportGrace - time.Second)
	r.st.LastSeq++
	r.passAfter(ReportGrace - time.Second)
	// A pending permission request is activity too.
	r.st.Pending = []chat.Request{{ID: "p"}}
	r.passAfter(ReportGrace * 2)
	if len(r.stopped) != 0 {
		t.Fatalf("interrupted real work: %v", r.stopped)
	}
	r.st.Pending = nil
	r.passAfter(0)
	r.passAfter(ReportGrace)
	if len(r.stopped) != 1 {
		t.Fatalf("an idle run should now end: %v", r.stopped)
	}
}

func TestStopErrorIsLoggedAndRetried(t *testing.T) {
	r := newReportedRig()
	r.passAfter(0)
	r.stopErr = errors.New("boom")
	r.passAfter(ReportGrace)
	r.passAfter(10 * time.Second)
	if len(r.stopped) != 2 {
		t.Fatalf("a failed stop must be retried at the next pass: %v", r.stopped)
	}
	r.stopErr = chat.ErrNotRunning // already gone: settled, no retry
	r.passAfter(10 * time.Second)
	r.passAfter(10 * time.Second)
	if len(r.stopped) != 3 {
		t.Fatalf("ErrNotRunning should end the tracking: %v", r.stopped)
	}
}

func TestUnreadableOrStoppedChatIsLeftAlone(t *testing.T) {
	r := newReportedRig()
	r.c.status = func(string) (chat.Status, error) { return chat.Status{}, errors.New("no chat") }
	r.passAfter(0)
	r.passAfter(time.Hour)
	if len(r.stopped) != 0 {
		t.Fatalf("stopped an unreadable chat: %v", r.stopped)
	}
}
