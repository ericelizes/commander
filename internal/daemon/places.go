package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/rolemail"
	"github.com/example-user/commander/internal/store"
)

// activePlaceRuns is the set of run ids offered to or running on a place (the
// user's Mac): their liveness comes from heartbeats (place.Service.Sweep),
// never from a session file or tmux.
func activePlaceRuns(st *store.Store) func(ctx context.Context) map[int64]bool {
	return func(ctx context.Context) map[int64]bool {
		runs, err := st.ListPlaceRuns(ctx, "", true)
		if err != nil {
			return nil
		}
		out := make(map[int64]bool, len(runs))
		for _, r := range runs {
			out[r.RunID] = true
		}
		return out
	}
}

// settlePlace writes the bead side of an ended place run. Like a host
// Operator's settle, but never a restart: a new run needs the user's dialog on
// the Mac. No worktree to freeze.
func (s runSettler) settlePlace(ctx context.Context, st place.Settlement) error {
	s.lockWorktree = func(string) error { return nil }
	run := st.Run
	if st.Outcome == place.Finished {
		return s.exited(ctx, run)
	}
	manager := s.managerOf(ctx, run)
	moved, err := s.move(ctx, run, "open", "", false)
	if err != nil {
		return err
	}
	where := st.PlaceRun.Place
	var why, subject, body string
	switch st.Outcome {
	case place.Declined:
		why = "not started on " + where + ": " + st.Why
		subject = fmt.Sprintf("Run on %s not started for %s", where, run.TaskBeadID)
		body = fmt.Sprintf("The run offered to %s did not start (%s). %s is open again with no owner. Offer it again only if the user wants it (each run asks the user on the Mac).", where, st.Why, run.TaskBeadID)
	case place.Stopped:
		why = "stopped on " + where + ": " + st.Why
		subject = fmt.Sprintf("Operator on %s stopped on %s", where, run.TaskBeadID)
		body = fmt.Sprintf("%s on %s was stopped (%s). %s is open again with no owner; it is not restarted.", run.AgentName, where, st.Why, run.TaskBeadID)
		if st.Why == "usage_limit" {
			body += " The Mac's Claude login hit its usage limit; nothing moves to another account or runtime. Spawn again when it has usage left."
		}
	default:
		why = "lost on " + where + ": " + st.Why
		subject = fmt.Sprintf("Operator on %s died on %s", where, run.TaskBeadID)
		body = fmt.Sprintf("%s on %s ended without a report (%s). %s is open again with no owner; a place run is never restarted by cmdrd. Read what it left on the bead (bd show %s), then offer it again or take another path.", run.AgentName, where, st.Why, run.TaskBeadID, run.TaskBeadID)
	}
	return s.stopped(ctx, run, moved, manager, why, subject, body, "")
}

// placeDeps wires the place service to cmdrd's store, mail and settle.
type placeDeps struct {
	st      *store.Store
	am      *agentmail.ServerClient // nil without the mail server
	repo    string
	settle  runSettler
	home    string
	usageMu sync.Mutex
}

func (p *placeDeps) deps(comment func(ctx context.Context, dir, bead, role, text string) error) place.Deps {
	return place.Deps{
		Store: p.st,
		ReadBead: func(ctx context.Context, dir, id string) (place.Bead, error) {
			b, err := beads.FreshShow(ctx, p.settle.beads, dir, id)
			if err != nil {
				return place.Bead{}, err
			}
			if b == nil {
				return place.Bead{}, beads.ErrNotFound
			}
			return place.Bead{Title: b.Title, Description: b.Description, Status: b.Status, Labels: b.Labels}, nil
		},
		Comment: comment,
		Mail: func(ctx context.Context, from, to, subject, body string) error {
			if p.am == nil {
				return fmt.Errorf("mail server not configured")
			}
			if from == "cmdrd" && p.settle.mail != nil {
				return p.settle.mail(ctx, to, subject, body)
			}
			return rolemail.Send(ctx, p.st, p.am, p.repo, from, []string{to}, subject, body, agentmail.SendOptions{})
		},
		Inbox: func(ctx context.Context, role string) ([]agentmail.Mail, error) {
			// The Mac's hook asks at every tool call; the mail server is read
			// only when the role was signalled since its last full read.
			if !mailstate.NeedsCheck(role) {
				return nil, nil
			}
			start := time.Now()
			mails, err := livedata.NewUnreadBodyListFunc(p.st)(ctx, role)
			if err != nil {
				_ = mailstate.MarkReadFailed(role, start)
				return nil, err
			}
			// The hook displays at most ten messages. Retain both the cursor and
			// the signal for anything not handed to that hook.
			if len(mails) > place.HookMailLimit {
				mails = mails[:place.HookMailLimit]
			} else {
				_ = mailstate.MarkChecked(role, start)
			}
			if len(mails) == 0 {
				return mails, nil
			}
			// Handed to the Mac: they count as read, as the mail hook's cursor does.
			_ = mailstate.MarkAllRead(role, mails)
			return mails, nil
		},
		RecordUsage: p.recordUsage,
		Settle:      p.settle.settlePlace,
		Revision:    place.BuildRevision(),
	}
}

// recordUsage appends a usage event a Mac forwarded to chat/place-usage.jsonl,
// under the place's name (the Mac's own Claude login).
func (p *placeDeps) recordUsage(name string, ev json.RawMessage) {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	dir := filepath.Join(p.home, "chat")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "place-usage.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]any{"account": name, "at": time.Now().Unix(), "event": ev})
	_, _ = f.Write(append(line, '\n'))
}

// sweepPlaces runs the place sweep once a minute with the host reaper.
func (d *Daemon) sweepPlaces(ctx context.Context) {
	if d.places == nil {
		return
	}
	if err := d.places.Sweep(ctx); err != nil {
		log.Printf("daemon: place sweep: %v", err)
	}
}

// snapPlaces is what the board says of each active place run.
func snapPlaces(st *store.Store, now func() time.Time) func() []livedata.SnapPlace {
	return func() []livedata.SnapPlace {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runs, err := st.ListPlaceRuns(ctx, "", true)
		if err != nil || len(runs) == 0 {
			return nil
		}
		t := now()
		out := make([]livedata.SnapPlace, 0, len(runs))
		for _, pr := range runs {
			run, err := st.GetAgentRun(ctx, pr.RunID)
			if err != nil || run == nil {
				continue
			}
			out = append(out, livedata.SnapPlace{Operator: beads.OwnerLabelName(run.AgentName), Place: pr.Place,
				State: string(place.LivenessOf(pr, t)), Text: place.Words(pr, t)})
		}
		return out
	}
}
