package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

func reportedLooks(t *testing.T, runs []store.AgentRun, report func(store.AgentRun) (string, error)) (*liveOperators, *reportedRuns, *time.Time) {
	t.Helper()
	now := time.Unix(1793214000, 0)
	rep := newReportedRuns(func() time.Time { return now })
	return &liveOperators{
		now:      func() time.Time { return now },
		list:     func(context.Context) ([]store.AgentRun, error) { return nil, nil },
		finished: func(context.Context) ([]store.AgentRun, error) { return runs, nil },
		report:   func(_ context.Context, r store.AgentRun) (string, error) { return report(r) },
		running:  func(context.Context) ([]store.AgentRun, error) { return runs, nil },
		reported: rep,
	}, rep, &now
}

// A finished run that left a report is on the list from the first look, read once;
// one with no report never is, and a failed read is tried again at the next look.
func TestLiveOperatorsNoteFinishedReports(t *testing.T) {
	runs := []store.AgentRun{
		{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a-p.1"},
		{ID: 2, ProjectID: 3, TaskBeadID: "p-2", AgentName: "operator-op-b-p.2"},
		{ID: 3, ProjectID: 3, TaskBeadID: "p-3", AgentName: "operator-op-c-p.3"},
	}
	reads := map[int64]int{}
	fail := errors.New("busy")
	l, rep, now := reportedLooks(t, runs, func(r store.AgentRun) (string, error) {
		reads[r.ID]++
		switch r.ID {
		case 1:
			return "Done", nil
		case 2:
			return "", nil
		}
		return "Done too", fail
	})
	l.get()
	got := rep.list()
	if len(got) != 1 || got[0].Bead != "p-1" || got[0].Operator != beads.OwnerLabelName("operator-op-a-p.1") || got[0].ProjectID != 3 || got[0].Since != now.Unix() {
		t.Fatalf("after the first look: %+v", got)
	}
	since := now.Unix()
	fail = nil
	*now = now.Add(liveEvery)
	l.get()
	got = rep.list()
	if len(got) != 2 || got[0].Bead != "p-1" || got[0].Since != since || got[1].Bead != "p-3" {
		t.Fatalf("after the second look: %+v", got)
	}
	if reads[1] != 1 || reads[2] != 1 || reads[3] != 2 {
		t.Fatalf("reads %v, want each answered run read once", reads)
	}
}

// A settle that lands ends the window, and a look that read the run before the
// settle cannot bring it back: the step reads as its bead says from then on.
func TestReportedRunsEndWhenSettled(t *testing.T) {
	now := time.Unix(1793214000, 0)
	rep := newReportedRuns(func() time.Time { return now })
	run := store.AgentRun{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}
	rep.add(run, now)
	rep.done(run, now)
	rep.add(run, now)
	if got := rep.list(); len(got) != 0 {
		t.Fatalf("settled run listed: %+v", got)
	}
	if !rep.seen(run.ID) {
		t.Fatal("settled run forgotten too soon")
	}
}

// A settle that never lands stops speaking for the step and the board flags it.
func TestReportedRunsAgeOut(t *testing.T) {
	now := time.Unix(1793214000, 0)
	rep := newReportedRuns(func() time.Time { return now })
	rep.settling(store.AgentRun{ID: 1, TaskBeadID: "p-1", AgentName: "operator-op-a"}, now)
	now = now.Add(reportedMaxAge - time.Second)
	if got := rep.list(); len(got) != 1 {
		t.Fatalf("inside the bound: %+v", got)
	}
	now = now.Add(time.Second)
	if got := rep.list(); len(got) != 0 {
		t.Fatalf("past the bound: %+v", got)
	}
	// A retry that runs this late must not open it again.
	rep.prune(nil, now)
	rep.settling(store.AgentRun{ID: 1, TaskBeadID: "p-1", AgentName: "operator-op-a"}, now)
	if got := rep.list(); len(got) != 0 {
		t.Fatalf("a late retry opened it again: %+v", got)
	}
	now = now.Add(2 * reportedMaxAge)
	rep.prune(nil, now)
	if rep.seen(1) {
		t.Fatal("kept long after its retries ended")
	}
}

// A row the sweep cannot close keeps its entry, so the first time stands: looks
// past the bound never open the window again, and the step flags while the sweep fails.
func TestReportedKeepsItsFirstSinceWhileTheRowRuns(t *testing.T) {
	runs := []store.AgentRun{{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}}
	l, rep, now := reportedLooks(t, runs, func(store.AgentRun) (string, error) { return "Done", nil })
	l.get()
	*now = now.Add(reportedMaxAge)
	for range 3 {
		*now = now.Add(liveEvery)
		l.get()
		if got := rep.list(); len(got) != 0 {
			t.Fatalf("a look past the bound opened it again: %+v", got)
		}
	}
}

// A row closed by anything but the sweep (a runtime or account switch, the
// ceilings watchdog) has no settle coming: its entry goes at the next look, so it
// cannot read a later death on the same step as a handoff.
func TestReportedDropsARowClosedWithNoSettle(t *testing.T) {
	runs := []store.AgentRun{{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}}
	l, rep, now := reportedLooks(t, runs, func(store.AgentRun) (string, error) { return "Done", nil })
	l.get()
	if got := rep.list(); len(got) != 1 {
		t.Fatalf("after the first look: %+v", got)
	}
	l.finished = func(context.Context) ([]store.AgentRun, error) { return nil, nil }
	l.running = l.finished
	*now = now.Add(liveEvery)
	l.get()
	if got := rep.list(); len(got) != 0 || rep.seen(1) {
		t.Fatalf("leftover entry: %+v", got)
	}
}

// The sweep closes rows before their settles start, and a failed settle waits a
// minute for its retry: neither is a row closed with no settle coming.
func TestReportedKeepsARowTheSweepIsSettling(t *testing.T) {
	run := store.AgentRun{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}
	now := time.Unix(1793214000, 0)
	rep := newReportedRuns(func() time.Time { return now })
	rep.add(run, now)

	rep.beginSweep()
	rep.prune(nil, now) // the sweep closed the row; its settle has not started
	if got := rep.list(); len(got) != 1 {
		t.Fatalf("lost between the close and the settle: %+v", got)
	}
	var s runSettler
	s.reported = rep
	s.now = func() time.Time { return now }
	s.settleOrRetry(context.Background(), func(func(context.Context) error) {}, "exited", run, func(context.Context) error { return errors.New("bd busy") })
	rep.endSweep()

	rep.prune(nil, now) // the settle failed; the retry is a minute off
	if got := rep.list(); len(got) != 1 {
		t.Fatalf("lost between a failed settle and its retry: %+v", got)
	}
	rep.done(run, now)
	rep.prune(nil, now)
	if got := rep.list(); len(got) != 0 || !rep.seen(1) {
		t.Fatalf("settled entry: %+v, remembered %v", got, rep.seen(1))
	}
}

// A slow look holds nothing up: others answer from the last look, and the look's
// names and reports appear together.
func TestLiveOperatorsLookHoldsNoLock(t *testing.T) {
	runs := []store.AgentRun{{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}}
	entered, release := make(chan struct{}), make(chan struct{})
	l, rep, now := reportedLooks(t, runs, func(store.AgentRun) (string, error) {
		close(entered)
		<-release
		return "Done", nil
	})
	l.finished = func(context.Context) ([]store.AgentRun, error) { return nil, nil }
	if _, _, ok := l.get(); !ok {
		t.Fatal("first look")
	}
	first := l.at
	l.list = func(context.Context) ([]store.AgentRun, error) { return runs, nil }
	l.finished = func(context.Context) ([]store.AgentRun, error) { return runs, nil }
	*now = now.Add(liveEvery)

	looked := make(chan []string)
	go func() {
		names, _, _ := l.get()
		looked <- names
	}()
	<-entered
	answered := make(chan time.Time)
	go func() {
		_, at, _ := l.get()
		answered <- at
	}()
	select {
	case at := <-answered:
		if !at.Equal(first) {
			t.Fatalf("answered from %v, want the last look %v", at, first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a snapshot waited for the look's comment read")
	}
	if got := rep.list(); len(got) != 0 {
		t.Fatalf("report listed before the read ended: %+v", got)
	}
	close(release)
	if names := <-looked; len(names) != 1 {
		t.Fatalf("the look's own answer: %v", names)
	}
	if got := rep.list(); len(got) != 1 {
		t.Fatalf("the look's report: %+v", got)
	}
}

// With no last look to answer from, others wait for the one under way.
func TestLiveOperatorsFirstLookIsWaitedFor(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	l := &liveOperators{now: func() time.Time { return time.Unix(1793214000, 0) }, list: func(context.Context) ([]store.AgentRun, error) {
		calls++
		close(entered)
		<-release
		return []store.AgentRun{{AgentName: "operator-op-a"}}, nil
	}}
	first := make(chan bool)
	go func() {
		_, _, ok := l.get()
		first <- ok
	}()
	<-entered
	second := make(chan []string)
	go func() {
		names, _, _ := l.get()
		second <- names
	}()
	close(release)
	if ok := <-first; !ok {
		t.Fatal("the look failed")
	}
	if names := <-second; len(names) != 1 || calls != 1 {
		t.Fatalf("names %v after %d looks", names, calls)
	}
}

// The window runs from the settle's first step through a failed attempt to the
// retry that lands; a run that died never opens one.
func TestSettleHoldsTheReportedWindow(t *testing.T) {
	now := time.Unix(1793214000, 0)
	var sent []sentMail
	b := &beads.MockClient{ShowResult: &beads.Bead{ID: "p-1", Status: "in_progress", Labels: []string{"owner:operator-op-a"}}}
	s := settler(b, &sent)
	s.now = func() time.Time { return now }
	s.reported = newReportedRuns(s.now)
	run := store.AgentRun{ID: 1, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}

	var retry func(context.Context) error
	attempts := 0
	settle := func(c context.Context) error {
		attempts++
		s.reported.add(run, now) // what exited does before its first write
		if attempts == 1 {
			return errors.New("bd busy")
		}
		return nil
	}
	s.settleOrRetry(context.Background(), func(f func(context.Context) error) { retry = f }, "exited", run, settle)
	if got := s.reported.list(); len(got) != 1 || got[0].Bead != "p-1" {
		t.Fatalf("failed settle lost the window: %+v", got)
	}
	if err := retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.reported.list(); len(got) != 0 {
		t.Fatalf("landed settle kept the window: %+v", got)
	}

	if err := s.exited(context.Background(), store.AgentRun{ID: 2, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"}); err != nil {
		t.Fatal(err)
	}
	if got := s.reported.list(); len(got) != 1 || got[0].Since != now.Unix() {
		t.Fatalf("exited with a report opens the window: %+v", got)
	}

	s.comments = func(context.Context, string, string) ([]beadComment, error) { return nil, nil }
	s.reported = newReportedRuns(s.now)
	_ = s.exited(context.Background(), store.AgentRun{ID: 3, ProjectID: 3, TaskBeadID: "p-1", AgentName: "operator-op-a"})
	if got := s.reported.list(); len(got) != 0 {
		t.Fatalf("no report opened the window: %+v", got)
	}
}

// Through New itself: a real store row whose session ended with a marker and a
// report on the bead reads as the Manager's move on the snapshot and in the
// invariant watcher's fresh read, and stops once the row closes or the settle lands.
func TestReportedThroughDaemonWiring(t *testing.T) {
	home, err := os.MkdirTemp("", "rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	testguard.NoLeakedProcesses(t, home)
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("TMUX_TMPDIR", home)
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "invented")

	// Comments come from the beads CLI; the board reads the export.
	project := filepath.Join(home, "app")
	if err := os.MkdirAll(filepath.Join(project, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	const operator = "operator-op-done-app-w-1"
	var export strings.Builder
	for _, b := range []map[string]any{
		{"id": "app-w", "title": "Queue work", "status": "open", "labels": []string{"kind:objective"}},
		{"id": "app-w.1", "title": "Measure the queue", "status": "in_progress", "labels": []string{"kind:objective", "owner:" + operator},
			"dependencies": []beads.Dep{{IssueID: "app-w.1", DependsOnID: "app-w", Type: "parent-child"}}},
	} {
		b["created_at"], b["updated_at"] = "2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z"
		line, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		export.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(export.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(home, "br")
	script := fmt.Sprintf("#!/bin/sh\necho '[{\"created_at\":%q,\"text\":\"Final report: done\"}]'\n", time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_BEADS_BIN", fake)

	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.store.Close() })
	ctx := context.Background()
	pid, err := d.store.InsertProject(ctx, store.Project{Name: "app", Type: "code", Path: project, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	const session = "cmdr-app-done"
	run := store.AgentRun{ProjectID: pid, TaskBeadID: "app-w.1", AgentName: operator, Status: "running", SpawnTime: time.Now().Add(-time.Hour).Unix(),
		TmuxPaneID: sql.NullString{String: session, Valid: true}}
	if run.ID, err = d.store.InsertAgentRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spawn.ExitMarkerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spawn.ExitMarker(session), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	snap := d.live.Snapshot(ctx)
	if snap.Live == nil || len(snap.Live.Reported) != 1 || snap.Live.Reported[0].Bead != "app-w.1" {
		t.Fatalf("snapshot's reported: %+v", snap.Live)
	}
	if row := reportedRow(t, snap); row.Text != "Result ready for Manager review" {
		t.Fatalf("row reads %q, why %q", row.Text, row.Why)
	}
	fresh, ok := d.invariant.fresh(ctx)
	if !ok {
		t.Fatal("invariant read failed")
	}
	step := reportedStepOf(t, fresh)
	if step.Owner != "manager-app-w" || step.State != beads.StateReview {
		t.Fatalf("invariant's fresh read: %+v", step)
	}
	if v := livedata.SnapshotViolations(fresh); len(v) != 0 {
		t.Fatalf("invariant: %+v", v)
	}

	// The settle shares the board's record.
	d.settle.reported.done(run, time.Now())
	if got := d.live.Snapshot(ctx); len(got.Live.Reported) != 0 {
		t.Fatalf("settled, still reported: %+v", got.Live.Reported)
	}
	if fresh, _ := d.invariant.fresh(ctx); reportedStepOf(t, fresh).Owner != operator {
		t.Fatalf("settled, the fresh read still overlays: %+v", reportedStepOf(t, fresh))
	}

	// A row closed by another route: the next look reads the store and drops it.
	second := store.AgentRun{ID: run.ID + 1, ProjectID: pid, TaskBeadID: "app-w.1", AgentName: operator}
	d.settle.reported.add(second, time.Now())
	d.liveOps.ok = false
	if got := d.live.Snapshot(ctx); len(got.Live.Reported) != 0 {
		t.Fatalf("an entry with no running row stayed: %+v", got.Live.Reported)
	}
}

func reportedRow(t *testing.T, s livedata.Snapshot) livedata.BoardRow {
	t.Helper()
	if s.Display == nil || len(s.Display.Workstreams) != 1 {
		t.Fatalf("display: %+v", s.Display)
	}
	return s.Display.Workstreams[0]
}

func reportedStepOf(t *testing.T, s livedata.Snapshot) livedata.SnapStep {
	t.Helper()
	for _, w := range s.Workstreams {
		for _, st := range w.Steps {
			if st.BeadID == "app-w.1" {
				return st
			}
		}
	}
	t.Fatalf("no step in %+v", s.Workstreams)
	return livedata.SnapStep{}
}
