package daemon

import (
	"context"
	"log"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// finalReportGrace is how long an Operator keeps its session after writing its final report. Past
// it the run is over: background tasks it started (a `go test` poll loop) must not hold it open,
// count against the cap and read as working.
const finalReportGrace = 3 * time.Minute

// endReadTimeout bounds each beads read endFinishedRuns makes (the batched comments read of a
// project, a closed-status check). The pass runs on the minute loop ahead of reap and watch, so a
// hung bd may cost it this long per read, never the loop; a read that fails skips ending runs for
// that project this tick.
var endReadTimeout = 10 * time.Second

// wrapUpAfterReport is how long after its report an Operator's pane may still print (its mail to
// the Manager, its last words) and count as finishing. An Operator went back to work after its
// report (resumed, M4/R2) when mail was shown to it after the report (its mail hook injects mail
// into the turn and moves its cursor, even inside the wrap-up window, which is when a Manager's
// answer to the report usually lands) or its pane printed past the wrap-up window. A resumed run
// is ended only once neither has moved for resumedQuiet, long enough for a slow tool call (a
// slot-wrapped suite, up to the 10-minute Bash timeout) to print again, and never while its pane
// output cannot be read (no proof that it is quiet).
const (
	wrapUpAfterReport = 2 * time.Minute
	resumedQuiet      = 20 * time.Minute
)

// isFinalReport: the first line of a comment is an Operator's final report (the role prompt says to
// write one onto the bead when done), not a progress note.
func isFinalReport(line string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "final report")
}

// endFinishedRuns ends the host Operator runs whose work is over: a final report was written at
// least finalReportGrace ago (or the bead was closed after a report), yet the session is still up.
// It writes the exit marker the launcher would and kills the pane (spawn.EndFinishedSession), with
// no stop marker, so the next sweep settles it as exited with its report (Manager to close), not as
// a stopped or crashed run. kill nil = the real pane kill.
// Returns the sessions it ended.
func (s runSettler) endFinishedRuns(ctx context.Context, kill func(ctx context.Context, session string) error) []string {
	if s.running == nil {
		return nil
	}
	runs, err := s.running(ctx)
	if err != nil {
		return nil
	}
	var ended []string
	// One batched, limited read of the task beads' comments per project, not one process per run.
	type projRead struct {
		path string
		cs   map[string][]beadComment
		ok   bool
	}
	reads := map[int64]*projRead{}
	for _, run := range runs {
		if !run.TmuxPaneID.Valid || run.TmuxPaneID.String == "" {
			continue
		}
		pr := reads[run.ProjectID]
		if pr == nil {
			pr = &projRead{}
			reads[run.ProjectID] = pr
			p, err := s.projects.GetProjectByID(ctx, run.ProjectID)
			if err == nil && p != nil {
				pr.path = p.Path
			}
		}
	}
	for id, pr := range reads {
		if pr.path == "" {
			continue
		}
		var ids []string
		for _, run := range runs {
			if run.ProjectID == id && run.TmuxPaneID.Valid && run.TmuxPaneID.String != "" {
				ids = append(ids, run.TaskBeadID)
			}
		}
		rctx, cancel := context.WithTimeout(ctx, endReadTimeout)
		pr.cs, pr.ok = s.runComments(rctx, pr.path, ids), true
		cancel()
		if pr.cs == nil {
			pr.ok = false
		}
	}
	for _, run := range runs {
		if !run.TmuxPaneID.Valid || run.TmuxPaneID.String == "" {
			continue
		}
		pr := reads[run.ProjectID]
		if pr == nil || !pr.ok {
			continue
		}
		cs, ok := pr.cs[run.TaskBeadID]
		if !ok {
			continue
		}
		p := struct{ Path string }{pr.path}
		var reportAt time.Time
		final := false
		for i := len(cs) - 1; i >= 0; i-- {
			t, err := time.Parse(time.RFC3339, cs[i].CreatedAt)
			if err != nil || t.Unix() < run.SpawnTime || strings.HasPrefix(cs[i].Text, noteMark) {
				continue
			}
			if line := firstLine(cs[i].Text, 200); line != "" {
				reportAt, final = t, isFinalReport(line)
				break
			}
		}
		if reportAt.IsZero() || s.clock().Sub(reportAt) < finalReportGrace {
			continue
		}
		if s.resumedAndBusy(run, p.Path, reportAt) {
			continue // it went back to work after its report (resumed by mail): not finished
		}
		if !final {
			// A progress note ends nothing, unless the Manager already closed the bead on it.
			sctx, cancel := context.WithTimeout(ctx, endReadTimeout)
			b, err := beads.FreshShow(sctx, s.beads, p.Path, run.TaskBeadID)
			cancel()
			if err != nil || b == nil || b.Status != "closed" {
				continue
			}
		}
		if err := spawn.EndFinishedSession(ctx, run.TmuxPaneID.String, kill); err != nil {
			log.Printf("daemon: end finished run %s: %v", run.AgentName, err)
			continue
		}
		log.Printf("daemon: ended %s: its final report is %s old and its session was still running", run.AgentName, s.clock().Sub(reportAt).Round(time.Second))
		ended = append(ended, run.TmuxPaneID.String)
	}
	return ended
}

// runComments reads the comments of the given task beads of one project in one batch, through the
// limited beads client; nil when the read failed or timed out (the caller skips the project). A
// bead missing from the answer is skipped too. The comments seam (tests) reads one bead at a time.
func (s runSettler) runComments(ctx context.Context, path string, ids []string) map[string][]beadComment {
	out := map[string][]beadComment{}
	if s.comments != nil {
		for _, id := range ids {
			cs, err := s.comments(ctx, path, id)
			if err != nil {
				return nil
			}
			out[id] = cs
		}
		return out
	}
	if s.beads == nil {
		return nil
	}
	det, err := beads.FreshDetails(ctx, s.beads, path, ids)
	if err != nil && ctx.Err() == nil && len(ids) > 1 {
		// One bead the batch could not answer (a deleted task bead: bd omits it and exits 0) fails
		// the whole fresh read; read the rest one at a time on what is left of the deadline, so
		// only that run is skipped.
		det, err = map[string]beads.Detail{}, nil
		for _, id := range ids {
			one, oerr := beads.FreshDetails(ctx, s.beads, path, []string{id})
			if oerr != nil {
				if ctx.Err() != nil {
					err = oerr
					break
				}
				continue
			}
			maps.Copy(det, one)
		}
	}
	if err != nil {
		log.Printf("daemon: end finished runs: comments of %d beads in %s: %v", len(ids), path, err)
		return nil
	}
	for id, d := range det {
		cs := make([]beadComment, 0, len(d.Comments))
		for _, c := range d.Comments {
			cs = append(cs, beadComment{Text: c.Text, CreatedAt: c.CreatedAt})
		}
		out[id] = cs
	}
	return out
}

// resumedAndBusy: the run went back to work after its report at reportAt (mail shown to it after
// the report, or pane output past the wrap-up window) and has not been quiet for resumedQuiet
// since, or its output cannot be read.
func (s runSettler) resumedAndBusy(run store.AgentRun, projectPath string, reportAt time.Time) bool {
	shown := s.mailShownAfter(run)
	out := s.lastOutputAt(run, projectPath)
	if !shown.After(reportAt) && !out.After(reportAt.Add(wrapUpAfterReport)) {
		return false
	}
	if out.IsZero() {
		return true
	}
	last := out
	if shown.After(last) {
		last = shown
	}
	return s.clock().Sub(last) < resumedQuiet
}

// mailShownAfter is when mail was last shown to the run's Operator (its mail cursor, under either
// spelling of its role; zero when none).
func (s runSettler) mailShownAfter(run store.AgentRun) time.Time {
	if s.mailShownAt != nil {
		return s.mailShownAt(run)
	}
	var last time.Time
	for _, role := range []string{run.AgentName, strings.ReplaceAll(run.AgentName, ".", "-")} {
		if t, ok := mailstate.CursorAt(role); ok && t.After(last) {
			last = t
		}
	}
	return last
}

// lastOutputAt is when the run's pane last printed (the mtime of its output file; zero when
// unknown, which reads as quiet).
func (s runSettler) lastOutputAt(run store.AgentRun, projectPath string) time.Time {
	if s.lastOutput != nil {
		return s.lastOutput(run, projectPath)
	}
	fi, err := os.Stat(spawn.RunOutputPath(run, projectPath))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
