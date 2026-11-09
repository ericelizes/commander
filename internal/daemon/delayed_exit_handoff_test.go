package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// Invented queue handoff: the Manager closes a review task with internal-close-objective
// --then-step/--then-waiting, naming the successor's wait, and the old
// Operators' exit or death notices reach the settler only afterwards (late
// sweep, wake from sleep, a retried settle). Those late events must leave the
// closed report alone, never put a Manager-to-close timer back, never clear the
// successor's waits or owner, and the board must read nothing as unheld.
func TestDelayedExitAfterCloseKeepsTheNextHolder(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_900_000_000, 0)
	successor := beads.Wait{Kind: beads.WaitOperator, Ref: "operator-op-fix-3", Since: now.Add(-time.Minute)}
	mem := &beads.MemClient{}
	mem.Seed(beads.Bead{ID: "p-ws", Title: "Queue", Labels: []string{"kind:objective"}})
	// The step the close handed on: its hold is the successor's wait.
	mem.Seed(beads.Bead{ID: "p-ws.2", ParentID: "p-ws", Title: "Ship the queue", Status: "in_progress", Labels: []string{"kind:objective", "owner:manager-p-ws", successor.Label()}})
	// The older attempt and the review task, both accepted and closed.
	mem.Seed(beads.Bead{ID: "p-ws.2.1", ParentID: "p-ws.2", Title: "First attempt", Status: "closed", Labels: []string{"kind:task", "owner:manager-p-ws", "review"}})
	mem.Seed(beads.Bead{ID: "p-ws.2.2", ParentID: "p-ws.2", Title: "Review the queue", Status: "closed", Labels: []string{"kind:task", "owner:manager-p-ws", "review"}})
	// The successor at work.
	mem.Seed(beads.Bead{ID: "p-ws.2.3", ParentID: "p-ws.2", Title: "Fix the queue", Status: "in_progress", Labels: []string{"kind:task", "owner:operator-op-fix-3"}})
	before := snapshotLabels(t, mem)

	var sent []sentMail
	s := runSettler{projects: oneProject{}, beads: mem, running: noRunning, now: func() time.Time { return now },
		comments: reportComments("Head: abc\nCLEAN"),
		mail: func(_ context.Context, to, subject, _ string) error {
			sent = append(sent, sentMail{to, subject})
			return nil
		}}
	review := store.AgentRun{ID: 11, ProjectID: 3, TaskBeadID: "p-ws.2.2", AgentName: "operator-op-review-2"}
	older := store.AgentRun{ID: 10, ProjectID: 3, TaskBeadID: "p-ws.2.1", AgentName: "operator-op-first-1"}
	for name, late := range map[string]func() error{
		"review exited": func() error { return s.exited(ctx, review) },
		"review died":   func() error { return s.died(ctx, review, false) },
		"older exited":  func() error { return s.exited(ctx, older) },
		"older died":    func() error { return s.died(ctx, older, true) },
		"review woke":   func() error { return s.diedBecause(ctx, review, false, asleepWhy) },
	} {
		if err := late(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	after := snapshotLabels(t, mem)
	for id, was := range before {
		if got := after[id]; got != was {
			t.Fatalf("%s changed by a late event:\n was %s\n now %s", id, was, got)
		}
	}
	for _, l := range strings.Split(after["p-ws.2"], " ") {
		if beads.IsSettleMarker(l) {
			t.Fatalf("a stale settle marker came back on the step: %s", after["p-ws.2"])
		}
	}

	// The board over the same beads: closed tasks read closed, nothing unheld.
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: "/p", ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	// The successor's Operator is alive: cmdrd's liveness fact.
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: mem},
		Live: func() ([]string, time.Time, bool) { return []string{"operator-op-fix-3"}, now, true }}
	snap := src.Snapshot(ctx)
	if snap.Degraded != nil {
		t.Fatalf("degraded: %+v", snap.Degraded)
	}
	if v := livedata.SnapshotViolations(snap); len(v) != 0 {
		t.Fatalf("unheld after the late events: %+v", v)
	}
	for _, ws := range livedata.Board(snap, now.Unix()).Workstreams {
		for _, f := range ws.Diagram.Findings {
			if f.Check == "unheld" && !f.Advisory {
				t.Fatalf("diagram finding: %+v", f)
			}
		}
	}
}

// snapshotLabels is each bead's status and sorted labels.
func snapshotLabels(t *testing.T, mem *beads.MemClient) map[string]string {
	t.Helper()
	bs, err := mem.List(context.Background(), "/p", beads.ListOptions{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, b := range bs {
		ls := slices.Clone(b.Labels)
		slices.Sort(ls)
		out[b.ID] = b.Status + " " + strings.Join(ls, " ")
	}
	return out
}
