package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

func TestSleepGapUsesWallClockAndThreshold(t *testing.T) {
	now := time.Date(2026, 10, 24, 3, 9, 0, 0, time.UTC)
	w := sleepWatch{now: func() time.Time { return now }}
	if _, woke := w.tick(); woke {
		t.Fatal("no gap before the first iteration ends")
	}
	w.done()
	now = now.Add(61 * time.Second)
	if _, woke := w.tick(); woke {
		t.Fatal("a normal minute is not a wake")
	}
	w.done()
	now = now.Add(3 * time.Minute)
	if _, woke := w.tick(); woke {
		t.Fatal("exactly 3 min is not over the threshold")
	}
	w.done()
	now = now.Add(10 * time.Hour)
	gap, woke := w.tick()
	if !woke || gap != 10*time.Hour {
		t.Fatalf("gap=%s woke=%v", gap, woke)
	}
}

// A slow iteration (sweep, snapshot load) is not a sleep: only the wait between
// the end of one iteration and the next tick counts.
func TestSleepGapIgnoresSlowIterations(t *testing.T) {
	now := time.Date(2026, 10, 24, 3, 9, 0, 0, time.UTC)
	w := sleepWatch{now: func() time.Time { return now }}
	w.done()
	now = now.Add(time.Minute)
	if _, woke := w.tick(); woke {
		t.Fatal("woke")
	}
	now = now.Add(10 * time.Minute) // the iteration itself took 10 min
	w.done()
	now = now.Add(time.Second) // buffered ticker fires at once
	if _, woke := w.tick(); woke {
		t.Fatal("a slow iteration read as a wake")
	}
}

func wakeBeads() *beads.MockClient {
	return &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-x"}}}
}

func TestWakeFromSleepMarksVanishedRunsDiedAndMailsTheManagerOnce(t *testing.T) {
	b := wakeBeads()
	var sent []sentMail
	s := settler(b, &sent)
	var retried int
	n := s.afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, 10*time.Hour, func(func(context.Context) error) { retried++ })
	if n != 1 || retried != 0 {
		t.Fatalf("died=%d retried=%d", n, retried)
	}
	calls := strings.Join(b.Calls, "\n")
	if !strings.Contains(calls, "label=owner:operator-x|on=false") || !strings.Contains(calls, "|status=open|") {
		t.Fatalf("calls:\n%s", calls)
	}
	if len(sent) != 1 || sent[0].to != "manager-p-ws" || !strings.Contains(sent[0].subject, "died while the Mac slept (gap 600 min)") {
		t.Fatalf("mail: %+v", sent)
	}
}

func TestWakeFromSleepExitedWithoutReportIsDiedAndWithReportIsFinished(t *testing.T) {
	var sent []sentMail
	s := settler(wakeBeads(), &sent)
	s.comments = func(context.Context, string, string) ([]beadComment, error) { return nil, nil }
	n := s.afterWake(context.Background(), spawn.HostReapResult{Exited: []store.AgentRun{run}}, time.Hour, func(func(context.Context) error) {})
	if n != 1 || len(sent) != 1 || !strings.Contains(sent[0].subject, "died while the Mac slept") {
		t.Fatalf("died=%d mail=%+v", n, sent)
	}
	sent = nil
	s = settler(wakeBeads(), &sent) // has a report
	n = s.afterWake(context.Background(), spawn.HostReapResult{Exited: []store.AgentRun{run}}, time.Hour, func(func(context.Context) error) {})
	if n != 0 || len(sent) != 1 || !strings.Contains(sent[0].subject, "finished") {
		t.Fatalf("died=%d mail=%+v", n, sent)
	}
}

func TestWakeFromSleepRespawnsWithinTheCap(t *testing.T) {
	var sent []sentMail
	sp := &fakeSpawner{}
	s := respawner(wakeBeads(), sp, &sent)
	s.afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, time.Hour, func(func(context.Context) error) {})
	if len(sp.reqs) != 1 || !sp.reqs[0].Resume {
		t.Fatalf("reqs: %+v", sp.reqs)
	}
	capped := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"auto-respawn:1", "auto-respawn:2"}}}
	sp = &fakeSpawner{}
	sent = nil
	respawner(capped, sp, &sent).afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, time.Hour, func(func(context.Context) error) {})
	if len(sp.reqs) != 0 || len(sent) != 1 || !strings.Contains(sent[0].subject, "died while the Mac slept") {
		t.Fatalf("reqs=%+v mail=%+v", sp.reqs, sent)
	}
}

func TestWakeFromSleepSummaryOnlyAboveTwo(t *testing.T) {
	for _, tc := range []struct{ n, summaries int }{{2, 0}, {3, 1}} {
		var sent []sentMail
		s := settler(wakeBeads(), &sent)
		res := spawn.HostReapResult{}
		for i := 0; i < tc.n; i++ {
			res.Died = append(res.Died, run)
		}
		s.afterWake(context.Background(), res, time.Hour, func(func(context.Context) error) {})
		got := 0
		for _, m := range sent {
			if m.to == "commander" {
				got++
			}
		}
		if got != tc.summaries || len(sent) != tc.n+tc.summaries {
			t.Fatalf("n=%d: mail %+v", tc.n, sent)
		}
	}
}

func TestWakeFromSleepFailedSettleIsRetried(t *testing.T) {
	b := wakeBeads()
	var sent []sentMail
	s := settler(b, &sent)
	fail := true
	s.mail = func(_ context.Context, to, subject, _ string) error {
		if fail {
			return errors.New("busy")
		}
		sent = append(sent, sentMail{to, subject})
		return nil
	}
	var q settleRetries
	s.afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, time.Hour, q.add)
	if len(q.pending) != 1 {
		t.Fatalf("pending=%d", len(q.pending))
	}
	fail = false
	q.run(context.Background())
	if len(q.pending) != 0 || len(sent) != 1 {
		t.Fatalf("pending=%d mail=%+v", len(q.pending), sent)
	}
}

// A settle that fails after the Operator was respawned retries only what is
// left: no second spawn, no second respawn label.
func TestWakeFromSleepRetryAfterRespawnDoesNotRespawnAgain(t *testing.T) {
	b := wakeBeads()
	var sent []sentMail
	sp := &fakeSpawner{}
	s := respawner(b, sp, &sent)
	fail := true
	s.mail = func(_ context.Context, to, subject, _ string) error {
		if fail {
			return errors.New("busy")
		}
		sent = append(sent, sentMail{to, subject})
		return nil
	}
	q := settleRetries{}
	s.afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, time.Hour, q.add)
	if len(sp.reqs) != 1 || len(q.pending) != 1 {
		t.Fatalf("reqs=%d pending=%d", len(sp.reqs), len(q.pending))
	}
	q.run(context.Background()) // still failing
	fail = false
	q.run(context.Background())
	if len(sp.reqs) != 1 || len(q.pending) != 0 || len(sent) != 1 || !strings.Contains(sent[0].subject, "respawned") {
		t.Fatalf("reqs=%d pending=%d mail=%+v", len(sp.reqs), len(q.pending), sent)
	}
	if n := strings.Count(strings.Join(b.Calls, "\n"), "label=auto-respawn:"); n != 1 {
		t.Fatalf("auto-respawn label written %d times:\n%s", n, strings.Join(b.Calls, "\n"))
	}
}

func TestWakeFromSleepRespawnMailNamesTheSleep(t *testing.T) {
	var bodies []string
	s := respawner(wakeBeads(), &fakeSpawner{}, new([]sentMail))
	s.mail = func(_ context.Context, _, _, body string) error { bodies = append(bodies, body); return nil }
	s.afterWake(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, time.Hour, func(func(context.Context) error) {})
	if len(bodies) != 1 || !strings.Contains(bodies[0], "died while the Mac slept") || strings.Contains(bodies[0], "restart") {
		t.Fatalf("bodies: %q", bodies)
	}
}

// A retry gives up when the Manager moved the bead on meanwhile.
func TestSettleRetryDroppedWhenBeadMovedOn(t *testing.T) {
	for name, bead := range map[string]*beads.Bead{
		"closed":  {ID: "p-ws.2.1", Status: "closed"},
		"claimed": {ID: "p-ws.2.1", Status: "in_progress", Labels: []string{"owner:operator-other"}},
	} {
		b := &beads.MockClient{ShowResult: bead}
		var sent []sentMail
		s := settler(b, &sent)
		s.mail = func(context.Context, string, string, string) error { return errors.New("busy") }
		var q settleRetries
		s.settleOrRetry(context.Background(), q.add, "died", run, func(c context.Context) error { return errors.New("first try failed") })
		if len(q.pending) != 1 {
			t.Fatalf("%s: pending=%d", name, len(q.pending))
		}
		q.run(context.Background())
		if len(q.pending) != 0 {
			t.Fatalf("%s: retry not dropped", name)
		}
		for _, c := range b.Calls {
			if strings.HasPrefix(c, "Set") {
				t.Fatalf("%s: changed the bead: %v", name, b.Calls)
			}
		}
	}
}

func TestSettleRetriesExpire(t *testing.T) {
	now := time.Now()
	q := settleRetries{now: func() time.Time { return now }}
	calls := 0
	q.add(func(context.Context) error { calls++; return errors.New("gone") })
	for i := 0; i < maxSettleAttempts+5; i++ {
		q.run(context.Background())
	}
	if calls != maxSettleAttempts || len(q.pending) != 0 {
		t.Fatalf("calls=%d pending=%d", calls, len(q.pending))
	}
	q.add(func(context.Context) error { calls++; return errors.New("gone") })
	now = now.Add(maxSettleAge + time.Minute)
	q.run(context.Background())
	if calls != maxSettleAttempts || len(q.pending) != 0 {
		t.Fatalf("aged retry ran: calls=%d pending=%d", calls, len(q.pending))
	}
}

// The no-wake sweep path queues failed settles the same way.
func TestSettleReapedQueuesFailures(t *testing.T) {
	var sent []sentMail
	s := settler(wakeBeads(), &sent)
	s.mail = func(context.Context, string, string, string) error { return errors.New("busy") }
	var q settleRetries
	s.settleReaped(context.Background(), spawn.HostReapResult{Died: []store.AgentRun{run}}, false, q.add)
	if len(q.pending) != 1 {
		t.Fatalf("pending=%d", len(q.pending))
	}
}

const (
	battDischarging6  = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t6%; discharging; 0:20 remaining present: true\n"
	battCharging6     = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t6%; charging; 1:20 remaining present: true\n"
	battAC100         = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged; 0:00 remaining present: true\n"
	battDischarging25 = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t25%; discharging; 3:00 remaining present: true\n"
)

func TestLowBatteryParse(t *testing.T) {
	for _, tc := range []struct {
		out string
		pct int
		bat bool
	}{{battDischarging6, 6, true}, {battCharging6, 6, false}, {battAC100, 100, false}} {
		pct, bat, ok := parseBattery(tc.out)
		if !ok || pct != tc.pct || bat != tc.bat {
			t.Fatalf("%q: %d %v %v", tc.out, pct, bat, ok)
		}
	}
	if _, _, ok := parseBattery("No batteries available\n"); ok {
		t.Fatal("desktop has no battery")
	}
}

func TestLowBatteryMailsOncePerDischargeCycle(t *testing.T) {
	var sent []sentMail
	out := battDischarging6
	w := &batteryWatcher{
		read: func(context.Context) (string, error) { return out, nil },
		mail: func(_ context.Context, to, subject, _ string) error {
			sent = append(sent, sentMail{to, subject})
			return nil
		},
	}
	ctx := context.Background()
	w.check(ctx)
	w.check(ctx)
	if len(sent) != 1 || sent[0].to != "commander" || sent[0].subject != "Battery at 6%" {
		t.Fatalf("mail: %+v", sent)
	}
	out = battDischarging25 // above 20: clears the latch
	w.check(ctx)
	out = battDischarging6
	w.check(ctx)
	if len(sent) != 2 {
		t.Fatalf("after relatch: %+v", sent)
	}
	out = battCharging6 // charging clears it too
	w.check(ctx)
	out = battDischarging6
	w.check(ctx)
	if len(sent) != 3 {
		t.Fatalf("after charge: %+v", sent)
	}
	(&batteryWatcher{mail: w.mail}).check(ctx) // no reader (Linux): no-op
	if len(sent) != 3 {
		t.Fatal("no-op sent mail")
	}
}
