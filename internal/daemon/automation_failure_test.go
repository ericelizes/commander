package daemon

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
	"time"
)

func TestAutomationFailureEscalatesOnceAndRetainsRecoveryTarget(t *testing.T) {
	snap := stalledSnap()
	leaf := &snap.Workstreams[0].Steps[0].Children[0]
	leaf.Waiting, leaf.WaitSince, leaf.WaitUntil, leaf.WaitUntilAs = beads.ManagerCloseWait, 1, 2, beads.ManagerCloseWait
	w, _, delivered, issues, _ := commanderWatch(snap, time.Unix(2000, 0))
	w.commanderPass(context.Background(), time.Unix(2000, 0), map[string]store.Checkin{})
	w.commanderPass(context.Background(), time.Unix(2001, 0), map[string]store.Checkin{})
	if len(*delivered) != 1 || len(issues.rows) != 1 || !issues.rows[0].Told || !strings.Contains((*delivered)[0].text, "RED automation failure") || !strings.Contains((*delivered)[0].text, "manager-c-1") {
		t.Fatalf("unbounded or unowned escalation: %+v %+v", *delivered, issues.rows)
	}
	// The persisted receipt survives a watcher replacement; no old actor starts.
	next, _, again, _, _ := commanderWatch(snap, time.Unix(2002, 0))
	next.issues = issues
	next.commanderPass(context.Background(), time.Unix(2002, 0), map[string]store.Checkin{})
	if len(*again) != 0 {
		t.Fatal("restart repeated successful escalation")
	}
}

func TestTaskHandoffWakeIsBoundedAndFresh(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{}
	wt := beads.Wait{Kind: beads.WaitUntil, Since: time.Unix(1, 0), Until: time.Unix(2, 0), Ref: beads.ManagerCloseWait}
	m.Seed(beads.Bead{ID: "sample-flow", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "sample-flow.1", ParentID: "sample-flow", Labels: []string{"kind:task", wt.Label()}})
	var sent []string
	w := &waitWatcher{projects: fakeProjects{}, beads: m, woken: map[string]bool{}, now: func() time.Time { return time.Unix(2000, 0) }, mail: func(_ context.Context, to, subject, body string) error {
		sent = append(sent, to+subject+body)
		return nil
	}}
	w.pollUntil(ctx)
	w.woken = map[string]bool{}
	w.pollUntil(ctx)
	if len(sent) != 1 || !strings.Contains(sent[0], "[failure]") || !strings.Contains(sent[0], "sample-flow.1") {
		t.Fatalf("task handoff not bounded: %v", sent)
	}
	b, _ := m.Show(ctx, "/p", "sample-flow.1")
	if !strings.Contains(strings.Join(b.Labels, " "), "handoff-notified:") {
		t.Fatal("notification receipt missing")
	}
	// A stale snapshot of the old handoff cannot wake or alter its replacement.
	old := *b
	_ = m.UpdateFields(ctx, "/p", b.ID, beads.Fields{RemoveLabels: []string{wt.Label()}, AddLabels: []string{"owner:operator-new"}})
	w.woken = map[string]bool{}
	old.Labels = []string{wt.Label()}
	w.wake(ctx, store.Project{Path: "/p"}, old, wt, "failure", "old")
	if len(sent) != 1 {
		t.Fatal("old handoff woke over replacement")
	}
}
