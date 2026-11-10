package spawn

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// hostSessionPrefix is the name prefix of every operator tmux session
// (knowledgeSessionName / hostSessionName).
const hostSessionPrefix = "cmdr-"

// hostOrphanGrace is how old an unclaimed session must be before the reaper
// kills it. A spawn in flight has a session but no agent_runs row yet (the row
// is inserted last), so a young session is not an orphan.
const hostOrphanGrace = 10 * time.Minute

// hostStartGrace is how long a new run's pane may sit at a shell: the spawner
// opens the session with a shell and only then starts claude in it, so a
// just-spawned Operator looks exited for a moment.
const hostStartGrace = 2 * time.Minute

// ExitMarkerDir holds the files a host Operator's launcher writes when its
// process ends on its own, just before it closes its own session.
func ExitMarkerDir() string { return filepath.Join(agentrt.CommanderHome(), "operator-exits") }

// ExitMarker is session's exit marker file.
func ExitMarker(session string) string { return filepath.Join(ExitMarkerDir(), session) }

// exitedSince reports (and clears) session's exit marker written at or after since.
func exitedSince(session string, since time.Time) bool {
	exited := exitMarkedSince(session, since)
	_ = os.Remove(ExitMarker(session))
	return exited
}

// exitMarkedSince reports session's exit marker written at or after since, and leaves it.
func exitMarkedSince(session string, since time.Time) bool {
	fi, err := os.Stat(ExitMarker(session))
	return err == nil && !fi.ModTime().Before(since.Truncate(time.Second))
}

// RunLister is the slice of *store.Store the host reaper needs.
type RunLister interface {
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
}

// TmuxSession is one live tmux session as the host reaper sees it.
type TmuxSession struct {
	Name    string
	Created time.Time
	// Idle is true when every pane is dead or sits at a bare shell: the agent
	// process exited and left the session behind.
	Idle bool
}

// HostReapResult reports what ReapHost cleaned up.
type HostReapResult struct {
	// KilledSessions are operator sessions with no running agent_runs row.
	KilledSessions []string
	// ClosedRuns are the ids of every row closed below.
	ClosedRuns []int64
	// Exited are runs whose process ended on its own (its session is back at a
	// shell): the Operator finished, and its bead is ready for review.
	Exited []store.AgentRun
	// Died are runs whose session is gone: the process was killed or crashed.
	Died []store.AgentRun
	// Stopped are runs ended on request (StopHostRun): never restarted.
	Stopped []StoppedRun
}

// WithHost enables host-operator reconciliation (ReapHost): runs are the
// agent_runs source and tx kills orphaned sessions.
func (r *Reaper) WithHost(tx tmuxctl.Client, runs RunLister) *Reaper {
	r.tmux = tx
	r.runs = runs
	r.listSessions = listTmuxSessions
	return r
}

// ReapHost reconciles host operators (agent_runs rows that carry a tmux
// session instead of a container) against the live tmux sessions, the host
// counterpart of Reap:
//
//   - a running row whose session sits at a shell is closed as exited (the
//     Operator finished);
//   - a running row whose session is gone is closed as orphan-reaped (the
//     process died without anyone recording it);
//   - a cmdr-* session with no running row, older than hostOrphanGrace, is
//     killed (spawn crashed between launching the process and recording it).
//
// Per-item errors are remembered (first wins) without stopping the sweep.
func (r *Reaper) ReapHost(ctx context.Context) (HostReapResult, error) {
	var res HostReapResult
	if r.tmux == nil || r.runs == nil {
		return res, fmt.Errorf("reaper: host reconciliation not configured (call WithHost)")
	}
	sessions, err := r.listSessions(ctx)
	if err != nil {
		return res, fmt.Errorf("reaper: list tmux sessions: %w", err)
	}
	runs, err := r.runs.ListRunningRuns(ctx)
	if err != nil {
		return res, fmt.Errorf("reaper: list running runs: %w", err)
	}

	live := make(map[string]TmuxSession, len(sessions))
	for _, s := range sessions {
		live[s.Name] = s
	}
	claimed := make(map[string]bool, len(runs))
	var firstErr error
	remember := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, run := range runs {
		if !run.TmuxPaneID.Valid || run.TmuxPaneID.String == "" {
			continue // a Docker operator: Reap's job
		}
		name := run.TmuxPaneID.String
		claimed[name] = true
		sess, ok := live[name]
		if ok && (!sess.Idle || r.now().Sub(time.Unix(run.SpawnTime, 0)) < hostStartGrace) {
			continue
		}
		// A stop outranks the exit marker: the launcher may still write one on
		// the way down, and neither may read as a crash or a finish.
		if by, stopped := stoppedBy(name, time.Unix(run.SpawnTime, 0)); stopped {
			if err := r.store.MarkClosed(ctx, run.ID, StatusStopped); err != nil {
				remember(fmt.Errorf("reaper: close run %d: %w", run.ID, err))
				continue
			}
			_ = os.Remove(StopMarker(name))
			_ = os.Remove(ExitMarker(name))
			res.ClosedRuns = append(res.ClosedRuns, run.ID)
			res.Stopped = append(res.Stopped, StoppedRun{Run: run, By: by})
			continue
		}
		// A gone session ended on its own when its launcher left an exit marker
		// (the launcher closes its session after the Operator finishes); else it
		// was killed or crashed.
		finished := ok || (r.exited != nil && r.exited(name, time.Unix(run.SpawnTime, 0)))
		status := "exited"
		if !finished {
			status = "orphan-reaped"
		}
		if err := r.store.MarkClosed(ctx, run.ID, status); err != nil {
			remember(fmt.Errorf("reaper: close run %d: %w", run.ID, err))
			continue
		}
		res.ClosedRuns = append(res.ClosedRuns, run.ID)
		if finished {
			res.Exited = append(res.Exited, run)
			continue
		}
		res.Died = append(res.Died, run)
		r.recordOrphan(ctx, &run, fmt.Sprintf("tmux session %s is gone but its agent_runs row was running", name))
	}

	for _, s := range sessions {
		if claimed[s.Name] || !strings.HasPrefix(s.Name, hostSessionPrefix) {
			continue
		}
		if r.now().Sub(s.Created) < hostOrphanGrace {
			continue
		}
		if err := r.tmux.KillPane(ctx, s.Name); err != nil {
			remember(fmt.Errorf("reaper: kill session %s: %w", s.Name, err))
			continue
		}
		res.KilledSessions = append(res.KilledSessions, s.Name)
		r.recordOrphan(ctx, nil, fmt.Sprintf("tmux session %s had no running agent_runs row", s.Name))
	}
	return res, firstErr
}

// recordOrphan writes the orphan-reaped audit event; run is nil when no row
// exists.
func (r *Reaper) recordOrphan(ctx context.Context, run *store.AgentRun, reason string) {
	ev := store.CostEvent{
		EventType:  "orphan-reaped",
		Reason:     sql.NullString{String: reason, Valid: true},
		OccurredAt: r.now().Unix(),
	}
	if run != nil {
		ev.ProjectID = run.ProjectID
		ev.TaskBeadID = sql.NullString{String: run.TaskBeadID, Valid: run.TaskBeadID != ""}
		ev.AgentName = run.AgentName
	}
	_, _ = r.store.InsertCostEvent(ctx, ev)
}

// listTmuxSessions lists sessions on the ops socket and the default server. No server at all
// means no sessions, not an error: every host operator is then gone.
func listTmuxSessions(ctx context.Context) ([]TmuxSession, error) {
	out, err := tmuxctl.ListPanes(ctx,
		"#{session_name}|#{session_created}|#{pane_dead}|#{pane_current_command}|#{pane_pid}")
	if err != nil {
		return nil, err
	}
	parents, err := exec.CommandContext(ctx, "ps", "-axo", "ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("read process parents: %w", err)
	}
	children := map[int]bool{}
	for _, v := range strings.Fields(string(parents)) {
		if n, e := strconv.Atoi(v); e == nil {
			children[n] = true
		}
	}
	return parseTmuxPanesWithChildren(out, func(pid int) bool { return children[pid] }), nil
}

var bareShells = map[string]bool{"zsh": true, "bash": true, "sh": true, "fish": true, "dash": true}

// pidAlive reports whether a process with this pid exists. A pane whose
// process is gone can still be listed with a stale command name (macOS shows
// "kernel_task"), so the command alone never proves liveness.
var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// pidHasChildren reports whether the process has a live child. The launcher
// shell of a running Operator always has one (claude); an idle interactive shell
// has none. tmux reports the foreground command, which is the Bash tool's
// bash/zsh while claude runs a tool call, so the command name alone would make
// a working Operator look finished.
var pidHasChildren = func(pid int) bool {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// parseTmuxPanes folds "name|unix-created|dead|command|pid" pane lines into one
// TmuxSession each; malformed lines are skipped. A session is Idle when none
// of its panes runs a live non-shell command, or a shell that has children (an
// Operator mid tool call), whose pane pid exists.
func parseTmuxPanes(out string) []TmuxSession { return parseTmuxPanesWithChildren(out, pidHasChildren) }

func parseTmuxPanesWithChildren(out string, hasChildren func(int) bool) []TmuxSession {
	var sessions []TmuxSession
	index := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 5)
		if len(f) != 5 || f[0] == "" {
			continue
		}
		secs, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(f[4])
		busy := f[2] != "1" && pidAlive(pid) && (!bareShells[f[3]] || hasChildren(pid))
		i, ok := index[f[0]]
		if !ok {
			index[f[0]] = len(sessions)
			sessions = append(sessions, TmuxSession{Name: f[0], Created: time.Unix(secs, 0), Idle: !busy})
			continue
		}
		if busy {
			sessions[i].Idle = false
		}
	}
	return sessions
}

// ReapHostRuns closes running host-operator rows whose tmux session has no live
// agent process, so dead Operators stop counting against their workstream's cap.
// Only cmdrd calls it: the caller must settle what it returns (reopen the bead,
// tell the Manager), or those Operators are lost.
func ReapHostRuns(ctx context.Context, st *store.Store) (HostReapResult, error) {
	return NewReaper(st, docker.New()).WithHost(tmuxctl.New(), st).ReapHost(ctx)
}

// LiveRunningRuns lists running rows minus host Operators that are already
// dead (session gone, or only a shell left), without closing anything: cmdrd's
// reaper closes and settles those. For callers that only count, like the cap.
func (r *Reaper) LiveRunningRuns(ctx context.Context) ([]store.AgentRun, error) {
	return r.liveRunningRuns(ctx, false)
}
func (r *Reaper) liveRunningRuns(ctx context.Context, strict bool) ([]store.AgentRun, error) {
	runs, err := r.runs.ListRunningRuns(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := r.listSessions(ctx)
	if err != nil {
		if strict {
			return nil, err
		}
		return runs, nil // legacy readers keep their previous policy
	}
	live := make(map[string]TmuxSession, len(sessions))
	for _, s := range sessions {
		live[s.Name] = s
	}
	out := runs[:0:0]
	for _, run := range runs {
		if run.TmuxPaneID.Valid && run.TmuxPaneID.String != "" {
			sess, ok := live[run.TmuxPaneID.String]
			if !ok || (sess.Idle && r.now().Sub(time.Unix(run.SpawnTime, 0)) >= hostStartGrace) {
				continue
			}
		}
		out = append(out, run)
	}
	return out, nil
}

// FinishedRunningRuns lists the running rows ReapHost will close as exited:
// the session is back at a shell or gone with an exit marker, and no stop was
// asked for. It closes nothing and clears no marker, so cmdrd's reaper still
// settles them. A killed session (gone, no marker) is not listed.
func (r *Reaper) FinishedRunningRuns(ctx context.Context) ([]store.AgentRun, error) {
	runs, err := r.runs.ListRunningRuns(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := r.listSessions(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]TmuxSession, len(sessions))
	for _, s := range sessions {
		live[s.Name] = s
	}
	var out []store.AgentRun
	for _, run := range runs {
		if !run.TmuxPaneID.Valid || run.TmuxPaneID.String == "" {
			continue
		}
		name, since := run.TmuxPaneID.String, time.Unix(run.SpawnTime, 0)
		sess, ok := live[name]
		if ok && (!sess.Idle || r.now().Sub(since) < hostStartGrace) {
			continue
		}
		if _, stopped := stopMarked(name, since); stopped {
			continue
		}
		if ok || exitMarkedSince(name, since) {
			out = append(out, run)
		}
	}
	return out, nil
}

// LiveRuns adapts LiveRunningRuns to RunLister.
type LiveRuns struct {
	Reaper *Reaper
	Strict bool
}

// ListRunningRuns implements RunLister.
func (l LiveRuns) ListRunningRuns(ctx context.Context) ([]store.AgentRun, error) {
	return l.Reaper.liveRunningRuns(ctx, l.Strict)
}

// NewLiveRuns is LiveRuns over the real store and tmux.
func NewLiveRuns(st *store.Store) LiveRuns {
	return LiveRuns{Reaper: NewReaper(st, docker.New()).WithHost(tmuxctl.New(), st), Strict: st.IsPostgres()}
}

// EndFinishedSession ends a host Operator's session whose work is over (its final report written)
// so the reaper reads it as exited, not as killed: killing the pane HUPs the launcher before it can
// write its own exit marker, so the marker is written here first, the way the launcher would. A
// kill that fails takes back a marker this call created (the session still runs; a later kill
// writes it again). kill nil = the pane on the ops socket (tmuxctl.ExactPane).
func EndFinishedSession(ctx context.Context, session string, kill func(ctx context.Context, session string) error) error {
	if kill == nil {
		tx := tmuxctl.New()
		kill = func(ctx context.Context, session string) error { return tx.KillPane(ctx, tmuxctl.ExactPane(session)) }
	}
	marker := ExitMarker(session)
	_, statErr := os.Stat(marker)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(ExitMarkerDir(), 0o755); err != nil {
		return fmt.Errorf("exit marker dir: %w", err)
	}
	// Written fresh (mtime now) either way: a marker left from an earlier run would predate this
	// run's spawn time and read as no finish.
	if err := os.WriteFile(marker, []byte("ended after its final report\n"), 0o644); err != nil {
		return fmt.Errorf("exit marker: %w", err)
	}
	if err := kill(ctx, session); err != nil {
		if created {
			_ = os.Remove(marker)
		}
		return err
	}
	return nil
}
