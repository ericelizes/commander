package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/nextactor"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

func TestHealthBoard(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	var alerts []string
	h.alert = func(key, detail string, _ time.Time) error { alerts = append(alerts, key); return nil }

	h.fail("mail:manager-x", "Mail can't reach manager-x: its chat could not be started")
	if got := h.list(); len(got) != 0 {
		t.Fatalf("a fresh failure is a problem before the grace: %v", got)
	}
	now = now.Add(healthGrace + time.Second)
	h.fail("mail:manager-x", "Mail can't reach manager-x: its chat could not be started")
	got := h.list()
	h.alertDue()
	if len(got) != 1 || got[0].Since != 1000 || len(alerts) != 1 {
		t.Fatalf("problems %v, alerts %v", got, alerts)
	}
	h.alertDue()
	if len(alerts) != 1 {
		t.Fatalf("alerted again: %v", alerts)
	}
	h.ok("mail:manager-x")
	if got := h.list(); len(got) != 0 {
		t.Fatalf("still listed after it worked: %v", got)
	}
	// A failure nothing reports any more goes stale.
	h.fail("start:manager-y", "x")
	now = now.Add(healthStale + time.Minute)
	if got := h.list(); len(got) != 0 {
		t.Fatalf("stale problem kept: %v", got)
	}
}

func TestCheckPlansAndWhoHears(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	snap := &livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "lab-x", ManagerRole: "manager-lab-x"}},
		Display: &livedata.Display{Workstreams: []livedata.BoardRow{{N: 2, Title: "Import pipeline", Project: "lab", BeadID: "lab-x",
			Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{
				{Check: "started-early", Bead: "lab-x.4", Title: "Deploy", Why: "it is marked in progress while a step it waits on is still open", Fix: "cmdr ..."},
				{Check: "gate-title", Bead: "lab-x.5", Advisory: true},
				{Check: "MERGE_REVIEW_INCOMPLETE", Bead: "lab-x.6"},
				{Check: "MERGE_REVIEW_REQUIRED", Bead: "lab-x.7"},
			}}}}},
	}
	checkPlans(h, snap)
	now = now.Add(healthGrace + time.Second)
	checkPlans(h, snap)
	got := h.list()
	if len(got) != 1 || got[0].Key != "plan:manager-lab-x" || !strings.Contains(got[0].Detail, "lab-x.4") || strings.Contains(got[0].Detail, "lab-x.5") || strings.Contains(got[0].Detail, "lab-x.6") || strings.Contains(got[0].Detail, "lab-x.7") {
		t.Fatalf("problems %+v", got)
	}
	to, subject, _ := healthMail(got[0].Key, got[0].Detail, time.Unix(got[0].Since, 0))
	if to != "manager-lab-x" || !strings.Contains(subject, "plan") {
		t.Errorf("plan problem went to %s (%s)", to, subject)
	}
	if to, _, _ := healthMail("mail:manager-lab-x", "Mail can't reach manager-lab-x", time.Unix(1000, 0)); to != commanderRole {
		t.Errorf("mail problem went to %s, want Commander", to)
	}
	// Fixed plan: the problem clears.
	snap.Display.Workstreams[0].Diagram.Findings = nil
	checkPlans(h, snap)
	if got := h.list(); len(got) != 0 {
		t.Fatalf("still listed after the plan was fixed: %v", got)
	}
}

// A bead whose Operator was spawned under spawnGrace ago is not reported: the
// claim and the snapshot are still settling.
func TestCheckPlansSkipsFreshlySpawned(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	snap := &livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "lab-x", ManagerRole: "manager-lab-x"}},
		Watch:       map[string]livedata.SnapWatch{"manager-lab-x": {Operators: []livedata.SnapWatchedOp{{Role: "operator-a", BeadID: "lab-x.4", Since: 1000 - 60}}}},
		Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Project: "lab", BeadID: "lab-x",
			Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{{Check: "unheld", Bead: "lab-x.4", Why: "w", Fix: "f"}}}}}},
	}
	checkPlans(h, snap)
	now = now.Add(healthGrace + time.Second)
	snap.Watch["manager-lab-x"].Operators[0].Since = 1000 + int64(healthGrace/time.Second) - 60 // 1 min before "now"
	checkPlans(h, snap)
	if got := h.list(); len(got) != 0 {
		t.Fatalf("fresh spawn reported: %v", got)
	}
	now = now.Add(spawnGrace + time.Second)
	checkPlans(h, snap)
	now = now.Add(healthGrace + time.Second)
	checkPlans(h, snap)
	if got := h.list(); len(got) != 1 {
		t.Fatalf("a finding past the spawn grace must report: %v", got)
	}
}

// A workstream closed during the grace has no plan to fix: nobody is told to fix it.
func TestCheckPlansDropsAWorkstreamThatLeftTheBoard(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	var alerts []string
	h.alert = func(key, _ string, _ time.Time) error { alerts = append(alerts, key); return nil }
	broken := livedata.BoardRow{Project: "lab", BeadID: "lab-x",
		Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{{Check: "unheld", Bead: "lab-x.4", Why: "w", Fix: "f"}}}}
	snap := &livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "lab-x", ManagerRole: "manager-lab-x"}},
		Display:     &livedata.Display{Workstreams: []livedata.BoardRow{broken}},
	}
	checkPlans(h, snap)

	// A degraded snapshot may just have missed the row: the issue stays.
	gone := &livedata.Snapshot{Display: &livedata.Display{}, Degraded: &livedata.SnapDegraded{Reason: "read failed"}}
	checkPlans(h, gone)
	if _, kept := h.issues["plan:manager-lab-x"]; !kept {
		t.Fatal("a degraded snapshot dropped the issue")
	}

	h.fail("mail:manager-lab-y", "another kind of problem")
	gone.Degraded = nil
	checkPlans(h, gone)
	now = now.Add(healthGrace + time.Second)
	h.fail("mail:manager-lab-y", "another kind of problem")
	got := h.list()
	h.alertDue()
	if len(got) != 1 || got[0].Key != "mail:manager-lab-y" || len(alerts) != 1 || alerts[0] != "mail:manager-lab-y" {
		t.Fatalf("problems %v, alerts %v", got, alerts)
	}
}

// A send that fails must not use the alert up: the Manager would hear nothing
// until the issue clears and recurs. It is tried again, not on every tick, and
// further apart each time: a send that timed out may still have landed, and Agent
// Mail has no idempotency key.
func TestHealthAlertRetriesBackOffPerKey(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	var sent, other []time.Time
	sendErr := errors.New("mail server down")
	h.alert = func(key, _ string, _ time.Time) error {
		if key == "plan:manager-lab-x" {
			sent = append(sent, now)
		} else {
			other = append(other, now)
		}
		return sendErr
	}
	h.fail("plan:manager-lab-x", "#1 Lab: the plan breaks 1 rule(s)")
	now = now.Add(healthGrace + time.Second)
	for end := now.Add(50 * time.Minute); now.Before(end); now = now.Add(10 * time.Second) {
		h.fail("plan:manager-lab-x", "#1 Lab: the plan breaks 1 rule(s)")
		if len(sent) >= 3 {
			h.fail("mail:manager-lab-y", "another key has its own backoff")
		}
		h.alertDue()
	}
	if len(other) < 2 || other[1].Sub(other[0]) != time.Minute {
		t.Fatalf("another key's sends %v: it must start at the first retry gap", other)
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	if len(sent) != len(want)+1 {
		t.Fatalf("%d sends %v, want %d", len(sent), sent, len(want)+1)
	}
	for i, d := range want {
		if got := sent[i+1].Sub(sent[i]); got != d {
			t.Fatalf("gap %d = %s, want %s", i, got, d)
		}
	}
	// It goes through, and never goes again.
	n := len(sent)
	sendErr = nil
	now = now.Add(healthAlertRetryMax)
	h.fail("plan:manager-lab-x", "#1 Lab: the plan breaks 1 rule(s)")
	h.alertDue()
	now = now.Add(healthAlertRetryMax)
	h.fail("plan:manager-lab-x", "#1 Lab: the plan breaks 1 rule(s)")
	h.alertDue()
	if len(sent) != n+1 {
		t.Fatalf("%d sends after %d, want exactly one more", len(sent), n)
	}
}

// Source.Snapshot calls list: a hung mail server must never stall a snapshot.
func TestHealthListNeverSends(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	calls := 0
	h.alert = func(string, string, time.Time) error { calls++; return nil }
	h.fail("mail:manager-lab-x", "Mail can't reach manager-lab-x")
	now = now.Add(healthGrace + time.Second)
	if got := h.list(); len(got) != 1 {
		t.Fatalf("problems %v", got)
	}
	if calls != 0 {
		t.Fatalf("list sent %d alert(s) on the snapshot path", calls)
	}
	h.alertDue()
	if calls != 1 {
		t.Fatalf("alertDue sent %d, want 1", calls)
	}
}

// Held history is one issue however many roles hold it: one mail to Commander, a
// bounded list of roles, cleared when nothing is held.
func TestMailHistoryIsOneBoundedIssue(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	var alerts []string
	h.alert = func(key, _ string, _ time.Time) error { alerts = append(alerts, key); return nil }
	held := map[string]int{}
	for i := 0; i < healthHistoryRoles+5; i++ {
		held[fmt.Sprintf("manager-lab-%02d", i)] = 2
	}
	reportMailHistory(h, held, nil)
	now = now.Add(healthGrace + time.Second)
	reportMailHistory(h, held, nil)
	got := h.list()
	h.alertDue()
	h.alertDue()
	if len(got) != 1 || got[0].Key != mailHistoryKey || len(alerts) != 1 {
		t.Fatalf("problems %v alerts %v", got, alerts)
	}
	d := got[0].Detail
	if !strings.Contains(d, "manager-lab-00 (2)") || strings.Contains(d, fmt.Sprintf("manager-lab-%02d", healthHistoryRoles)) || !strings.Contains(d, "+5 more") || !strings.Contains(d, "cmdr internal-inbox --agent $r --mark-read") {
		t.Fatalf("detail %q", d)
	}
	reportMailHistory(h, nil, nil)
	if got := h.list(); len(got) != 0 {
		t.Fatalf("still listed with nothing held: %v", got)
	}
}

// The alert names held subjects that mention an answer, by role and count, and
// its clearing line covers exactly the roles it names.
func TestMailHistoryNamesHeldAnswersAndHowToClear(t *testing.T) {
	now := time.Now()
	h := newHealthBoard(func() time.Time { return now })
	report := func() {
		reportMailHistory(h, map[string]int{"manager-lab-a": 3, "manager-lab-b": 1}, map[string][]string{"manager-lab-b": {"Re: [decision] lab-1: Merge"}})
	}
	report()
	now = now.Add(healthGrace + time.Second)
	report()
	got := h.list()
	if len(got) != 1 {
		t.Fatalf("problems %v", got)
	}
	d := got[0].Detail
	for _, want := range []string{"manager-lab-a (3), manager-lab-b (1)", `May hold a decision answer: manager-lab-b: "Re: [decision] lab-1: Merge"`, "for r in manager-lab-a manager-lab-b; do cmdr internal-inbox --agent $r --mark-read >/dev/null; done"} {
		if !strings.Contains(d, want) {
			t.Fatalf("detail lacks %q: %q", want, d)
		}
	}
}

// A hold quiets the plan's errors, so they cannot turn the check-in into NOT all on track,
// and clears what was raised before it. A hold on another workstream changes nothing.
func TestCheckPlansSkipsAWorkstreamOnHold(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	row := func(bead string) livedata.BoardRow {
		return livedata.BoardRow{Project: "lab", BeadID: bead, Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{{Check: "unheld", Bead: bead + ".1", Why: "w", Fix: "f"}}}}
	}
	snap := &livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "w", ManagerRole: "manager-w"}, {Project: "lab", BeadID: "x", ManagerRole: "manager-x"}},
		Display:     &livedata.Display{Workstreams: []livedata.BoardRow{row("w"), row("x")}},
	}
	settle := func() {
		checkPlans(h, snap)
		now = now.Add(healthGrace + time.Second)
		checkPlans(h, snap)
	}
	settle()
	if got := h.list(); len(got) != 2 {
		t.Fatalf("no hold: %+v", got)
	}
	snap.Workstreams[0].Hold = true
	settle()
	if got := h.list(); len(got) != 1 || got[0].Key != "plan:manager-x" {
		t.Fatalf("held: %+v", got)
	}
}

// The health loop's load shares a read slot with the API's and had no timer.
func TestHealthLoopLogsASlowLoad(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	load := func(context.Context) livedata.Snapshot {
		time.Sleep(5 * time.Millisecond)
		return livedata.Snapshot{Phases: livedata.LoadPhases{Gates: time.Minute}}
	}
	timedHealthLoad(context.Background(), load, time.Hour)
	if logs.Len() != 0 {
		t.Fatalf("a quick load logged: %s", logs.String())
	}
	timedHealthLoad(context.Background(), load, time.Millisecond)
	if got := logs.String(); !strings.Contains(got, "health loop load took") || !strings.Contains(got, "gates 1m0s") {
		t.Fatalf("log = %q", got)
	}
}

// A nested workstream with its own Manager gets the findings on its own steps; the
// root's Manager keeps the rest; a hold on the root quiets both.
func TestCheckPlansNestedFindingsGoToNestedManager(t *testing.T) {
	now := time.Unix(1000, 0)
	h := newHealthBoard(func() time.Time { return now })
	snap := &livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "w", ManagerRole: "manager-w", Steps: []livedata.SnapStep{
			{BeadID: "w.a", ManagerRole: "manager-w.a", Children: []livedata.SnapStep{{BeadID: "w.a.1"}}},
			{BeadID: "w.2"},
		}}},
		Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Project: "lab", BeadID: "w", Diagram: &livedata.Diagram{Findings: []livedata.DiagramFinding{
			{Check: "unheld", Bead: "w.a.1", Why: "nested", Fix: "f"},
		}}}}},
	}
	settle := func() {
		checkPlans(h, snap)
		now = now.Add(healthGrace + time.Second)
		checkPlans(h, snap)
	}
	settle()
	if got := h.list(); len(got) != 1 || got[0].Key != "plan:manager-w.a" {
		t.Fatalf("findings went to %+v, want plan:manager-w.a only", got)
	}
	// A snapshot whose root row has no diagram this time keeps the nested
	// issue, as it keeps the root's: the workstream is still on the board.
	diagram := snap.Display.Workstreams[0].Diagram
	snap.Display.Workstreams[0].Diagram = nil
	checkPlans(h, snap)
	if got := h.list(); len(got) != 1 || got[0].Key != "plan:manager-w.a" {
		t.Fatalf("a row without a diagram dropped the nested issue: %+v", got)
	}
	snap.Display.Workstreams[0].Diagram = diagram
	snap.Workstreams[0].Hold = true
	settle()
	if got := h.list(); len(got) != 0 {
		t.Fatalf("a hold on the root left %+v", got)
	}
}

type nestedProject struct{ path string }

func (p nestedProject) GetProjectByID(context.Context, int64) (*store.Project, error) {
	return &store.Project{ID: 1, Path: p.path}, nil
}

// Settle mail, crash restarts and hand-backs name the Manager computed at settle
// time: after the cutover an old run's report reaches the nested Manager.
func TestSettleMailsNestedManager(t *testing.T) {
	dir := t.TempDir()
	run := store.AgentRun{ProjectID: 1, TaskBeadID: beads.NestedStep}
	tree := beads.NestedTree()
	if got := runManager(context.Background(), nestedProject{dir}, tree, run); got != "manager-"+beads.NestedRoot {
		t.Fatalf("switch off: %s", got)
	}
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	if got := runManager(context.Background(), nestedProject{dir}, tree, run); got != "manager-"+beads.NestedA1 {
		t.Fatalf("switch on: %s, want the nested Manager", got)
	}
}

func TestCheckStuckAlertsOnlyPastTheThreshold(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := now
	h := newHealthBoard(func() time.Time { return clock })
	row := func(since int64) livedata.BoardRow {
		return livedata.BoardRow{N: 1, Project: "p", BeadID: "ws", Title: "Goal", Actor: &nextactor.Row{Lead: nextactor.Result{Actor: nextactor.Stuck, Reason: "exited_without_report", Text: "Stuck: the agent stopped", Since: since}}}
	}
	snap := &livedata.Snapshot{Display: &livedata.Display{Workstreams: []livedata.BoardRow{row(now.Add(-5 * time.Minute).Unix())}}}
	checkStuck(h, snap, now)
	if len(h.list()) != 0 {
		t.Fatal("a row stuck for 5 minutes raised an alert", h.list())
	}
	snap.Display.Workstreams[0] = row(now.Add(-time.Hour).Unix())
	checkStuck(h, snap, now)
	clock = now.Add(healthGrace + time.Second) // a failure is listed once it outlasts the grace
	checkStuck(h, snap, clock)
	if l := h.list(); len(l) != 1 || !strings.Contains(l[0].Detail, "Stuck: the agent stopped") {
		t.Fatal("a row stuck for an hour did not alert", l)
	}
	snap.Display.Workstreams[0].Actor.Lead.Actor = nextactor.Agent
	checkStuck(h, snap, clock)
	if len(h.list()) != 0 {
		t.Fatal("the alert did not clear once the row moved", h.list())
	}
}
