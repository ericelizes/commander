package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
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

// Budget exhaustion never turns partial validation into mail or clears existing
// alert state; the next successful fresh read still controls that state.
func TestInvariantBudgetSkipsMailAndRetainsEvidence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: t.TempDir(), Status: "active"}); err != nil {
		t.Fatal(err)
	}
	bd := &budgetInvariantClient{MemClient: &beads.MemClient{}, t: t}
	sent := 0
	w := newInvariantWatcher(func() (livedata.Snapshot, bool) { return livedata.Snapshot{}, true }, func(context.Context, string, string, string) error { sent++; return nil })
	w.seen["demo-1"] = 1
	w.told["demo-2"] = true
	w.fresh = func(ctx context.Context) (livedata.Snapshot, bool) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		data, ok := loadInvariant(ctx, livedata.Deps{Store: st, Beads: bd})
		return livedata.BuildSnapshot(data, nil), ok
	}
	w.pass(context.Background())
	if sent != 0 || w.seen["demo-1"] != 1 || !w.told["demo-2"] || !bd.called {
		t.Fatalf("spent read changed alerts: sent=%d seen=%v told=%v read=%v", sent, w.seen, w.told, bd.called)
	}
	// The production bound is present even when the caller has no deadline.
	bd.checkOnly = true
	if _, ok := loadInvariant(context.Background(), livedata.Deps{Store: st, Beads: bd}); ok {
		t.Fatal("failed load accepted")
	}
}

type budgetInvariantClient struct {
	*beads.MemClient
	t                 *testing.T
	called, checkOnly bool
}

func (c *budgetInvariantClient) List(ctx context.Context, _ string, _ beads.ListOptions) ([]beads.Bead, error) {
	c.called = true
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > invariantReadBudget {
		c.t.Error("invariant load has no bounded deadline")
	}
	if !c.checkOnly {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("unreadable")
}
