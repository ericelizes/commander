package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/wsmgr"
)

// The minute watch looks at every running Operator without a model call: when
// it last did something (its transcript's last write) and whether it has gone
// quiet. Its Manager is woken only when something needs it: an Operator quiet
// for quietAfter, or the check-in the Manager set coming due.
const (
	CheckinDefault = 15 * time.Minute
	CheckinMax     = 30 * time.Minute
	quietAfter     = 20 * time.Minute
)

type watchStore interface {
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
	RunWatches(ctx context.Context) (map[int64]store.RunWatch, error)
	SetRunWatch(ctx context.Context, id int64, w store.RunWatch) error
	Checkins(ctx context.Context) (map[string]store.Checkin, error)
	SetCheckin(ctx context.Context, role string, c store.Checkin) error
	ClearCheckin(ctx context.Context, role string) error
}

type opWatch struct {
	store watchStore
	// deliver puts a check-in in front of a Manager; wake.ErrDeferred means it
	// is mid-turn, so the check-in waits for the next pass. Nil turns check-ins off.
	deliver func(ctx context.Context, role, prompt string) error
	// mail tells a Manager an Operator went quiet; nil turns that off.
	mail func(ctx context.Context, role, subject, body string) error
	// activeAt is when a Claude session last wrote its transcript.
	activeAt func(sessionID string) (time.Time, bool)
	now      func() time.Time
	// latest is cmdrd's last loaded snapshot data; nil turns Commander's watch off.
	latest func() (livedata.Snapshot, bool)
	// manager names the Manager of a run the snapshot does not show yet (a
	// fresh spawn); nil leaves such a run to the dotted id of its bead.
	manager func(ctx context.Context, run store.AgentRun) string
	issues  watchIssueStore
	// degraded raises the can't-read-state alarm; nil turns it off.
	degraded *degradedAlert
}

func (w opWatch) pass(ctx context.Context) {
	runs, err := w.store.ListRunningRuns(ctx)
	if err != nil {
		log.Printf("daemon: watch: %v", err)
		w.degraded.readFailure(ctx, err.Error(), errors.Is(err, context.DeadlineExceeded))
		return
	}
	seen, err := w.store.RunWatches(ctx)
	if err != nil {
		log.Printf("daemon: watch: %v", err)
		w.degraded.readFailure(ctx, err.Error(), errors.Is(err, context.DeadlineExceeded))
		return
	}
	now := w.now()
	owners := w.snapshotOwners()
	byManager := map[string][]store.AgentRun{}
	for _, run := range runs {
		manager, ok := owners[run.TaskBeadID]
		switch {
		case ok:
		case w.manager != nil:
			manager = w.manager(ctx, run)
		default:
			manager = wsmgr.Role(beads.Root(ctx, nil, "", nil, run.TaskBeadID))
		}
		byManager[manager] = append(byManager[manager], run)
		rw := seen[run.ID]
		active := run.SpawnTime
		if t, ok := w.activeAt(run.OpencodeSessionID); ok && t.Unix() > active {
			active = t.Unix()
		}
		rw.ActiveAt = active
		if quiet := now.Sub(time.Unix(active, 0)); quiet >= quietAfter && rw.QuietTold != active && w.mail != nil {
			subject := fmt.Sprintf("%s quiet for %s on %s", run.AgentName, roundMin(quiet), run.TaskBeadID)
			body := fmt.Sprintf("%s has written nothing for %s: it may be stuck. Look at its session (tmux -L commander-ops attach -t %s; older ones are on the default socket) and the bead (bd show %s), then nudge it by mail, or stop it and spawn again.", run.AgentName, roundMin(quiet), run.TmuxPaneID.String, run.TaskBeadID)
			if err := w.mail(ctx, manager, subject, body); err != nil {
				log.Printf("daemon: watch: tell %s: %v", manager, err)
			} else {
				rw.QuietTold = active
			}
		}
		if err := w.store.SetRunWatch(ctx, run.ID, rw); err != nil {
			log.Printf("daemon: watch: %v", err)
		}
	}

	checkins, err := w.store.Checkins(ctx)
	if err != nil {
		log.Printf("daemon: watch: %v", err)
		return
	}
	w.commanderPass(ctx, now, checkins)
	for role := range checkins {
		if role == commanderRole {
			continue
		}
		if len(byManager[role]) == 0 {
			_ = w.store.ClearCheckin(ctx, role)
		}
	}
	for role, rs := range byManager {
		c, ok := checkins[role]
		c.Checked = now.Unix()
		// Only a check-in the Manager asked for (internal-checkin) wakes it; with
		// none set the watch just records its look and wakes it on events.
		if ok && c.Due != 0 && now.Unix() >= c.Due && w.deliver != nil {
			err := w.deliver(ctx, role, checkinPrompt(role, rs, now))
			switch {
			case errors.Is(err, wake.ErrDeferred):
				// Mid-turn: it is looking already; try again next pass.
			case err != nil:
				log.Printf("daemon: watch: check-in %s: %v", role, err)
			default:
				c.Due = 0
			}
		}
		if err := w.store.SetCheckin(ctx, role, c); err != nil {
			log.Printf("daemon: watch: %v", err)
		}
	}
}

// checkinPrompt reads like any mail notice, so the app shows it as a message.
func checkinPrompt(role string, runs []store.AgentRun, now time.Time) string {
	sort.Slice(runs, func(i, j int) bool { return runs[i].TaskBeadID < runs[j].TaskBeadID })
	var b strings.Builder
	n := len(runs)
	fmt.Fprintf(&b, "[mail] cmdrd: Check-in: %d Operator%s still at work\n", n, plural(n))
	for _, r := range runs {
		fmt.Fprintf(&b, "- %s on %s\n", r.AgentName, r.TaskBeadID)
	}
	fmt.Fprintf(&b, "Nothing is required. Look if you want (bd show <bead>, its tmux session), then end your turn. "+
		"For another check-in, `cmdr internal-checkin --agent %s --in <1m..%s>`.",
		role, roundMin(CheckinMax))
	return b.String()
}

func roundMin(d time.Duration) string {
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// transcriptActiveAt finds a Claude session's transcript under
// ~/.claude/projects/<cwd slug>/<session>.jsonl and returns its last write.
func transcriptActiveAt(sessionID string) (time.Time, bool) {
	if sessionID == "" {
		return time.Time{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return time.Time{}, false
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	var latest time.Time
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	return latest, !latest.IsZero()
}

// snapshotOwners maps each running Operator's task bead to its Manager as the
// last snapshot grouped them (livedata watchOf, by real parent edges), so the
// minute watch costs no br call.
func (w opWatch) snapshotOwners() map[string]string {
	out := map[string]string{}
	if w.latest == nil {
		return out
	}
	snap, ok := w.latest()
	if !ok {
		return out
	}
	for manager, sw := range snap.Watch {
		for _, op := range sw.Operators {
			out[op.BeadID] = manager
		}
	}
	return out
}
