package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/livedata"
)

func TestInvariantMailsManagerOncePerViolation(t *testing.T) {
	snap := livedata.Snapshot{Broken: []livedata.Violation{{BeadID: "p-ws.1", Title: "orphan", Workstream: "p-ws", ManagerRole: "manager-p-ws"}}}
	var sent []sentMail
	var bodies []string
	w := newInvariantWatcher(func() (livedata.Snapshot, bool) { return snap, true }, func(_ context.Context, to, subject, body string) error {
		sent = append(sent, sentMail{to, subject})
		bodies = append(bodies, body)
		return nil
	})
	ctx := context.Background()
	w.pass(ctx)
	if len(sent) != 0 {
		t.Fatalf("mailed on first sight (a fresh bead gets a grace pass): %+v", sent)
	}
	w.pass(ctx)
	w.pass(ctx)
	if len(sent) != 1 || sent[0].to != "manager-p-ws" {
		t.Fatalf("want exactly one mail to the Manager, got %+v", sent)
	}
	for _, want := range []string{"p-ws.1", "internal-set-objective", "internal-spawn-operator", "gate", "close"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("mail lacks %q:\n%s", want, bodies[0])
		}
	}
	// Fixed, then broken again: told again after the grace pass.
	good := livedata.Snapshot{}
	w.latest = func() (livedata.Snapshot, bool) { return good, true }
	w.pass(ctx)
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	w.pass(ctx)
	w.pass(ctx)
	if len(sent) != 2 {
		t.Fatalf("a re-broken bead should be mailed again, got %d mails", len(sent))
	}
}

func TestInvariantSkipsDegradedAndHealthy(t *testing.T) {
	var sent []sentMail
	mail := func(_ context.Context, to, subject, _ string) error {
		sent = append(sent, sentMail{to, subject})
		return nil
	}
	broken := []livedata.Violation{{BeadID: "p-ws.1", ManagerRole: "manager-p-ws"}}
	w := newInvariantWatcher(func() (livedata.Snapshot, bool) {
		return livedata.Snapshot{Broken: broken, Degraded: &livedata.SnapDegraded{Reason: "x"}}, true
	}, mail)
	for range 3 {
		w.pass(context.Background())
	}
	w.latest = func() (livedata.Snapshot, bool) { return livedata.Snapshot{}, true }
	for range 3 {
		w.pass(context.Background())
	}
	if len(sent) != 0 {
		t.Fatalf("mailed from a degraded or healthy snapshot: %+v", sent)
	}
}

func TestInvariantFreshReadOverridesDisplayBeforeMail(t *testing.T) {
	broken := livedata.Snapshot{Broken: []livedata.Violation{{BeadID: "p-1", ManagerRole: "manager-p"}}}
	sent := 0
	w := newInvariantWatcher(func() (livedata.Snapshot, bool) { return broken, true }, func(context.Context, string, string, string) error { sent++; return nil })
	reads := 0
	w.fresh = func(context.Context) (livedata.Snapshot, bool) { reads++; return livedata.Snapshot{}, true }
	w.pass(context.Background())
	w.pass(context.Background())
	if reads != 2 || sent != 0 || len(w.seen) != 0 {
		t.Fatalf("fresh reads=%d sent=%d seen=%v", reads, sent, w.seen)
	}
	// A missing display violation is also discovered by a fresh action read.
	w.latest = func() (livedata.Snapshot, bool) { return livedata.Snapshot{}, true }
	w.fresh = func(context.Context) (livedata.Snapshot, bool) { return broken, true }
	w.pass(context.Background())
	w.pass(context.Background())
	if sent != 1 {
		t.Fatalf("fresh violation was not mailed: sent=%d", sent)
	}
}
