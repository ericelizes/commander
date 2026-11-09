package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// A finished Operator whose background tasks keep claude -p alive must not hold its run open:
// past the grace the session is ended, with no stop marker, so the sweep settles it as exited.
func TestEndFinishedRuns(t *testing.T) {
	now := time.Date(2026, 11, 9, 16, 10, 0, 0, time.UTC)
	live := store.AgentRun{ID: 9, ProjectID: 3, TaskBeadID: "p-ws.2.1", AgentName: "operator-x", SpawnTime: now.Add(-time.Hour).Unix(),
		TmuxPaneID: sql.NullString{String: "op-x", Valid: true}}
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	for name, tc := range map[string]struct {
		comments []beadComment
		closed   bool
		output   time.Time // when the pane last printed (zero: unknown)
		shown    time.Time // when mail was last shown to it (zero: never)
		ended    bool
	}{
		"final report past the grace":  {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(10 * time.Minute)}}, ended: true},
		"final report just written":    {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(time.Minute)}}},
		"progress note, bead open":     {comments: []beadComment{{Text: "Step 2 done", CreatedAt: at(10 * time.Minute)}}},
		"progress note, bead closed":   {comments: []beadComment{{Text: "Step 2 done", CreatedAt: at(10 * time.Minute)}}, closed: true, ended: true},
		"no report":                    {},
		"report from an earlier run":   {comments: []beadComment{{Text: "Final report: old", CreatedAt: now.Add(-3 * time.Hour).Format(time.RFC3339)}}},
		"cmdrd note is never a report": {comments: []beadComment{{Text: noteMark + "Final report: x", CreatedAt: at(10 * time.Minute)}}},
		// M4: mail resumed it after its report and it is working (printing): not ended.
		"resumed after its report, working": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(30 * time.Minute)}}, output: now.Add(-5 * time.Minute)},
		// Its last words right after the report are wrapping up, not work.
		"last words after the report": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(10 * time.Minute)}}, output: now.Add(-9 * time.Minute), ended: true},
		// Resumed, then quiet long past a slow tool call: over.
		"resumed, then quiet for long": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(time.Hour)}}, output: now.Add(-25 * time.Minute), ended: true},
		// R2: the Manager's mail reached it inside the wrap-up window (its hook showed it at Stop)
		// and it is in a silent tool call (a slot-wrapped suite): the resume is tracked, not the silence.
		"resumed by mail inside the wrap-up window, silent tool call": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(4 * time.Minute)}},
			shown: now.Add(-3*time.Minute - 30*time.Second), output: now.Add(-3 * time.Minute)},
		"resumed by mail, tool call silent for 10 minutes": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(12 * time.Minute)}},
			shown: now.Add(-11 * time.Minute), output: now.Add(-11 * time.Minute)},
		// Resumed, with no readable output: no proof it is quiet.
		"resumed by mail, output unreadable": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(time.Hour)}}, shown: now.Add(-50 * time.Minute)},
		// Mail shown before the report is the work the report finished.
		"mail shown before the report": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(10 * time.Minute)}},
			shown: now.Add(-15 * time.Minute), output: now.Add(-9 * time.Minute), ended: true},
		// Resumed by mail, then quiet long past any tool call: over.
		"resumed by mail, then quiet for long": {comments: []beadComment{{Text: "Final report: done", CreatedAt: at(time.Hour)}},
			shown: now.Add(-58 * time.Minute), output: now.Add(-30 * time.Minute), ended: true},
	} {
		t.Run(name, func(t *testing.T) {
			mem := &beads.MemClient{}
			status := "open"
			if tc.closed {
				status = "closed"
			}
			mem.Seed(beads.Bead{ID: "p-ws.2.1", Title: "step", Status: status})
			s := runSettler{projects: oneProject{}, beads: mem, now: func() time.Time { return now },
				running:     func(context.Context) ([]store.AgentRun, error) { return []store.AgentRun{live}, nil },
				comments:    func(context.Context, string, string) ([]beadComment, error) { return tc.comments, nil },
				lastOutput:  func(store.AgentRun, string) time.Time { return tc.output },
				mailShownAt: func(store.AgentRun) time.Time { return tc.shown }}
			var killed []string
			got := s.endFinishedRuns(context.Background(), func(_ context.Context, session string) error { killed = append(killed, session); return nil })
			if (len(got) == 1) != tc.ended || len(killed) != len(got) {
				t.Fatalf("ended %v killed %v, want ended=%v", got, killed, tc.ended)
			}
		})
	}
}

// A hung bd must cost endFinishedRuns its read deadline, not the minute loop: the pass returns,
// ends nothing, and reap and watch run on time after it.
func TestEndFinishedRunsHungReadIsBounded(t *testing.T) {
	old := endReadTimeout
	endReadTimeout = 50 * time.Millisecond
	defer func() { endReadTimeout = old }()
	mk := func(id int64) store.AgentRun {
		return store.AgentRun{ID: id, ProjectID: 3, TaskBeadID: "p-ws.2.1", AgentName: "operator-x", SpawnTime: 1,
			TmuxPaneID: sql.NullString{String: "op-x", Valid: true}}
	}
	s := runSettler{projects: oneProject{}, beads: &beads.MemClient{}, now: time.Now,
		running: func(context.Context) ([]store.AgentRun, error) { return []store.AgentRun{mk(1), mk(2)}, nil },
		comments: func(ctx context.Context, _, _ string) ([]beadComment, error) {
			<-ctx.Done() // a hung bd
			return nil, ctx.Err()
		}}
	start := time.Now()
	got := s.endFinishedRuns(context.Background(), func(context.Context, string) error { t.Fatal("killed on a failed read"); return nil })
	if len(got) != 0 {
		t.Fatalf("ended %v", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("hung read held the pass for %s", d)
	}
}

// The comments of every running Operator of a project are one batched read.
func TestEndFinishedRunsReadsOneBatchPerProject(t *testing.T) {
	mem := &beads.MemClient{}
	for _, id := range []string{"p-a", "p-b", "p-c"} {
		mem.Seed(beads.Bead{ID: id, Title: id, Status: "open"})
	}
	var runs []store.AgentRun
	for i, id := range []string{"p-a", "p-b", "p-c"} {
		runs = append(runs, store.AgentRun{ID: int64(i + 1), ProjectID: 3, TaskBeadID: id, AgentName: "operator-" + id,
			TmuxPaneID: sql.NullString{String: "op-" + id, Valid: true}})
	}
	s := runSettler{projects: oneProject{}, beads: mem, now: time.Now,
		running: func(context.Context) ([]store.AgentRun, error) { return runs, nil }}
	if got := s.runComments(context.Background(), "/p", []string{"p-a", "p-b", "p-c"}); len(got) != 3 {
		t.Fatalf("batched read answered %v", got)
	}
	if got := s.endFinishedRuns(context.Background(), func(context.Context, string) error { return nil }); len(got) != 0 {
		t.Fatalf("ended %v with no reports", got)
	}
}

// M1: ending a finished run leaves the exit marker the launcher would have written (the pane kill
// HUPs the launcher before it can), so the reaper reads the run as exited, not killed; a kill that
// fails takes the marker back.
func TestEndFinishedRunsLeavesTheExitMarker(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	run := store.AgentRun{ID: 9, ProjectID: 3, TaskBeadID: "p-ws.2.1", AgentName: "operator-x", SpawnTime: now.Add(-time.Hour).Unix(),
		TmuxPaneID: sql.NullString{String: "cmdr-p-x", Valid: true}}
	report := []beadComment{{Text: "Final report: done", CreatedAt: now.Add(-10 * time.Minute).Format(time.RFC3339)}}
	s := runSettler{projects: oneProject{}, beads: &beads.MemClient{}, now: func() time.Time { return now },
		running:  func(context.Context) ([]store.AgentRun, error) { return []store.AgentRun{run}, nil },
		comments: func(context.Context, string, string) ([]beadComment, error) { return report, nil }}

	var sawMarker bool
	got := s.endFinishedRuns(context.Background(), func(context.Context, string) error {
		_, err := os.Stat(spawn.ExitMarker("cmdr-p-x"))
		sawMarker = err == nil
		return errors.New("no such session")
	})
	if len(got) != 0 || !sawMarker {
		t.Fatalf("failed kill: ended %v, marker before kill %v", got, sawMarker)
	}
	if _, err := os.Stat(spawn.ExitMarker("cmdr-p-x")); !os.IsNotExist(err) {
		t.Fatalf("a failed kill kept the marker: %v", err)
	}

	got = s.endFinishedRuns(context.Background(), func(context.Context, string) error { return nil })
	if len(got) != 1 {
		t.Fatalf("ended %v", got)
	}
	fi, err := os.Stat(spawn.ExitMarker("cmdr-p-x"))
	if err != nil || fi.ModTime().Before(time.Unix(run.SpawnTime, 0)) {
		t.Fatalf("exit marker after the kill: %v %v", fi, err)
	}
}

// countingDetails answers like bd: a batch naming a bead that is gone fails the fresh read
// (FreshDetails reports missing details), and every read is counted.
type countingDetails struct {
	*beads.MemClient
	calls [][]string
}

func (c *countingDetails) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	c.calls = append(c.calls, ids)
	out, err := c.MemClient.Details(ctx, dir, ids)
	for _, id := range ids {
		if _, ok := out[id]; !ok && err == nil {
			return nil, fmt.Errorf("br show: missing details for %s", id)
		}
	}
	return out, err
}

// The production read (FreshDetails, no comments seam) is one batch per project; a task bead gone
// from the db (m1) skips only its own run, not every run of the project.
func TestEndFinishedRunsBatchSurvivesAMissingBead(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	now := time.Now()
	mem := &beads.MemClient{}
	for _, id := range []string{"p-a", "p-b"} {
		mem.Seed(beads.Bead{ID: id, Title: id, Status: "open"})
		mem.AddComment(context.Background(), "/p", id, "Final report: done")
	}
	c := &countingDetails{MemClient: mem}
	var runs []store.AgentRun
	for i, id := range []string{"p-a", "p-gone", "p-b"} {
		runs = append(runs, store.AgentRun{ID: int64(i + 1), ProjectID: 3, TaskBeadID: id, AgentName: "operator-" + id,
			SpawnTime: now.Add(-time.Hour).Unix(), TmuxPaneID: sql.NullString{String: "cmdr-" + id, Valid: true}})
	}
	s := runSettler{projects: oneProject{}, beads: c, now: func() time.Time { return now.Add(time.Hour) },
		running: func(context.Context) ([]store.AgentRun, error) { return runs, nil }}

	if got := s.runComments(context.Background(), "/p", []string{"p-a", "p-b"}); len(got) != 2 || len(c.calls) != 1 {
		t.Fatalf("one batch answered %v in %d reads", got, len(c.calls))
	}
	c.calls = nil
	got := s.endFinishedRuns(context.Background(), func(context.Context, string) error { return nil })
	if len(got) != 2 || got[0] != "cmdr-p-a" || got[1] != "cmdr-p-b" {
		t.Fatalf("ended %v, want the two reported runs beside the missing bead", got)
	}
	if len(c.calls) != 4 { // the failed batch, then one read per bead
		t.Fatalf("reads %v", c.calls)
	}
}
