package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// StatusStopped is the agent_runs status of a run ended on request.
const StatusStopped = "stopped"

// StopMarkerDir holds the files StopHostRun leaves, so the reaper can tell a
// stop from a crash (which it restarts).
func StopMarkerDir() string { return filepath.Join(agentrt.CommanderHome(), "operator-stops") }

// StopMarker is session's stop marker file; it holds who stopped the run.
func StopMarker(session string) string { return filepath.Join(StopMarkerDir(), session) }

// StopPending reports whether run was stopped and no sweep has closed it yet.
func StopPending(run store.AgentRun) bool {
	_, stopped := stoppedBy(run.TmuxPaneID.String, time.Unix(run.SpawnTime, 0))
	return stopped
}

// StoppedRun is a run a sweep found ended by StopHostRun.
type StoppedRun struct {
	Run store.AgentRun
	By  string
}

// StopHostRun ends a running host Operator now: it leaves the stop marker, then
// kills the session's pane, whose hangup takes the process down (a Codex bridge
// ends its turn and its Codex with it). cmdrd's next sweep closes the run and
// hands the step to its Manager. The marker goes first, so no sweep can see the
// session gone without it.
func StopHostRun(ctx context.Context, tx tmuxctl.Client, run store.AgentRun, by string) error {
	session := run.TmuxPaneID.String
	if !run.TmuxPaneID.Valid || session == "" {
		return errors.New("the run has no tmux session")
	}
	// Exact targets: a bare name matches by prefix, so a stop sent just after
	// this session ended would end a sibling's (op-x-1 vs op-x-11), which cmdrd
	// would then restart as crashed.
	exists, err := tx.SessionExists(ctx, tmuxctl.ExactSession(session))
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("its session %s already ended", session)
	}
	if err := os.MkdirAll(StopMarkerDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(StopMarker(session), []byte(by+"\n"), 0o600); err != nil {
		return err
	}
	if err := tx.KillPane(ctx, tmuxctl.ExactPane(session)); err != nil {
		_ = os.Remove(StopMarker(session))
		return err
	}
	return nil
}

// refuseUnsweptStop refuses a spawn onto worktree while the run there was stopped
// and no sweep has closed its row. Under the old name the new run would share that
// row's session, and the sweep could not tell the two apart (the old row stays open
// while the session lives, then settles as a stop against the new run's step); under
// a new name a Codex tool may still be writing there (codexStopGrace). This holds
// only as long as the marker and the running row: the next sweep closes the row, and
// a run closed another way refuses nothing. What comes after the sweep needs no hold:
// the freeze refuses a worktree a running run owns (FreezeWorktree).
func (s *Spawner) refuseUnsweptStop(ctx context.Context, req SpawnRequest, worktree string) error {
	session := hostSessionName(req.ProjectName, filepath.Base(worktree))
	if _, err := os.Stat(StopMarker(session)); err != nil {
		return nil
	}
	runs, err := s.store.ListRunningRuns(ctx)
	if err != nil {
		return fmt.Errorf("spawn: branch %s: the Operator at %s was stopped and its run could not be read: %w", req.Branch, worktree, err)
	}
	for _, run := range runs {
		if run.TmuxPaneID.String != session {
			continue
		}
		if _, stopped := stoppedBy(session, time.Unix(run.SpawnTime, 0)); stopped {
			return fmt.Errorf("spawn: branch %s: the Operator at %s was stopped and cmdrd has not swept it yet: wait for cmdrd's next sweep (about a minute; its 'Operator stopped' mail follows), then spawn again", req.Branch, worktree)
		}
	}
	return nil
}

// stoppedBy reports who stopped session's run spawned at since, from its marker.
// A marker older than the run is an earlier run's: it is cleared and not a stop.
func stoppedBy(session string, since time.Time) (string, bool) {
	by, stopped := stopMarked(session, since)
	if !stopped {
		_ = os.Remove(StopMarker(session))
	}
	return by, stopped
}

// stopMarked is stoppedBy for an observer: it clears nothing.
func stopMarked(session string, since time.Time) (string, bool) {
	fi, err := os.Stat(StopMarker(session))
	if err != nil || fi.ModTime().Before(since.Truncate(time.Second)) {
		return "", false
	}
	raw, _ := os.ReadFile(StopMarker(session))
	return strings.TrimSpace(string(raw)), true
}
