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

	"github.com/example-user/commander/internal/livedata"
)

const pitchEvery = time.Minute

// pitchWatcher mails a Manager once when one of its asks is ready for the user
// but has no pitch (livedata.NoPitch): the board reads "Manager writing the
// pitch" instead of a card with nothing in it. An ask must be pitchless on two
// passes in a row (a Manager often writes the card right after the gate's
// blockers close), and is forgotten once it has a pitch or is gone, so a later
// pitchless ask mails again. When it was mailed feeds the board's deadline.
type pitchWatcher struct {
	latest func() (livedata.Snapshot, bool)
	// mail sends from Commander to a role.
	mail func(ctx context.Context, toRole, subject, body string) error
	now  func() time.Time

	mu        sync.Mutex
	seen      map[string]int
	stale     map[string]bool // successful notices, persisted across restarts
	stalePath string
	asked     map[string]int64 // "project:bead" -> unix s the Manager was mailed
}

func newPitchWatcher(home string, latest func() (livedata.Snapshot, bool), mail func(context.Context, string, string, string) error) *pitchWatcher {
	w := &pitchWatcher{latest: latest, mail: mail, now: time.Now, seen: map[string]int{}, stale: map[string]bool{}, asked: map[string]int64{}, stalePath: filepath.Join(home, "stale-notices.json")}
	data, err := os.ReadFile(w.stalePath)
	if err == nil {
		if err := json.Unmarshal(data, &w.stale); err != nil {
			log.Printf("daemon: stale card: read notices: %v", err)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("daemon: stale card: read notices: %v", err)
	}
	if w.stale == nil {
		w.stale = map[string]bool{}
	}
	return w
}

func (w *pitchWatcher) Run(ctx context.Context) {
	t := time.NewTicker(pitchEvery)
	defer t.Stop()
	for {
		w.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Asked is when each pitchless ask's Manager was mailed, by "project:bead".
func (w *pitchWatcher) Asked() map[string]int64 {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int64, len(w.asked))
	for k, v := range w.asked {
		out[k] = v
	}
	return out
}

func (w *pitchWatcher) pass(ctx context.Context) {
	snap, ok := w.latest()
	if !ok || snap.Degraded != nil {
		return // a stale or empty snapshot would mail (or forget) asks that are fine
	}
	w.mailStale(ctx, snap)
	now := map[string]bool{}
	var due []livedata.NoPitch
	w.mu.Lock()
	for _, p := range snap.NoPitch {
		if p.Proposed != "" {
			continue // Commander's review, not the Manager's pitch
		}
		k := p.Project + ":" + p.BeadID
		now[k] = true
		w.seen[k]++
		if _, told := w.asked[k]; !told && w.seen[k] >= 2 {
			due = append(due, p)
		}
	}
	for k := range w.seen {
		if !now[k] {
			delete(w.seen, k)
			delete(w.asked, k)
		}
	}
	w.mu.Unlock()
	for _, p := range due {
		subject, body := pitchMail(p)
		if err := w.mail(ctx, p.ManagerRole, subject, body); err != nil {
			log.Printf("daemon: pitch: mail %s: %v", p.ManagerRole, err)
			continue
		}
		w.mu.Lock()
		w.asked[p.Project+":"+p.BeadID] = w.now().Unix()
		w.mu.Unlock()
	}
}

func pitchMail(p livedata.NoPitch) (subject, body string) {
	what := "step"
	approves := ""
	if p.Gate {
		what = "approval gate"
		approves = `, "approves": true`
	}
	subject = fmt.Sprintf("[pitch] write the pitch for %q", p.Title)
	body = fmt.Sprintf(`The %s %s %q (project %s, workstream %q) is ready for the user, but it has no pitch: nothing to decide on. The user does not see it as an ask; their board reads "Manager writing the pitch for ..." until you write one.

Write the card now, in the full card format from your ownership prompt ("The card"): context opens with **High level** (**The problem:**, **Approved so far:**, **You decide now:**), then **The plan** and the **Execution workflow** graph, so the user sees where this gate sits and what yes starts. A short summary paragraph is not a pitch.
  cmdr internal-set-objective --project %s --bead %s --state needs-user --decision '{"question": "...", "context": "**High level**\n\n**The problem:** ...", "options": [{"label": "Approve", "implications": "...", "recommended": true%s}, {"label": "Not yet", "implications": "..."}]}'

If it is not the user's call after all, re-plan or close the step instead. You are mailed once; after 30 minutes the user's board says the pitch is late.`,
		what, p.BeadID, p.Title, p.Project, p.Workstream, p.Project, p.BeadID, approves)
	return
}

// mailStale uses the board's flag, so hidden/settled asks are never mailed.
// A failed send is retried on the next existing watcher pass.
func (w *pitchWatcher) mailStale(ctx context.Context, snap livedata.Snapshot) {
	active := map[string]bool{}
	for _, card := range snap.NeedsYou {
		active[livedata.StaleNoticeKey(card)] = true
	}
	for key := range w.stale {
		if !active[key] {
			delete(w.stale, key)
		}
	}
	defer w.saveStale()
	for _, a := range livedata.Board(snap, w.now().Unix()).Asks {
		if a.Stale == "" {
			continue
		}
		for _, card := range snap.NeedsYou {
			if a.Key != "ask:"+card.Project+":"+card.BeadID {
				continue
			}
			key := livedata.StaleNoticeKey(card)
			if w.stale[key] {
				continue
			}
			role := ""
			for _, ws := range snap.Workstreams {
				if ws.Project+"\x00"+ws.Title == card.Project+"\x00"+card.Workstream {
					role = ws.ManagerRole
					break
				}
			}
			if role == "" {
				log.Printf("daemon: stale card: no Manager role for %s:%s", card.Project, card.BeadID)
				continue
			}
			_, why := livedata.CardStaleIn(card, snap, w.now().Unix())
			subject, body := staleCardMail(card, why)
			if err := w.mail(ctx, role, subject, body); err != nil {
				log.Printf("daemon: stale card: mail %s: %v", role, err)
				continue
			}
			w.stale[key] = true
		}
	}
}

func staleCardMail(a livedata.NeedsYou, why string) (string, string) {
	return fmt.Sprintf("[stale card] %s: %s", a.BeadID, why), fmt.Sprintf(`The card for %s (%s) is out of date: %s. The user can still answer it. Check the facts before keeping it up.

If still right as written, record what you checked (15+ characters):
  cmdr internal-set-objective --project %s --bead %s --card-check still-right --reason '<what you checked>'
An age-only check is allowed once; after that re-raise or withdraw.
For a pushed-back warning, decide whether to do, drop or defer the work. A check or re-raise does not reset the push counter. Moving an until: wait or park date later past the limits requires --push-reason '<why this moves, 15+ characters, not a repeat>'; only Commander and the user can --push-reset.

If the ask changed, write the corrected full card:
  cmdr internal-set-objective --project %s --bead %s --state needs-user --decision '<corrected decision JSON>'
Or withdraw it:
  cmdr internal-set-objective --project %s --bead %s --status in-progress

One notice per card generation or re-baseline, including across daemon restarts.`, a.BeadID, a.Project, why, a.Project, a.BeadID, a.Project, a.BeadID, a.Project, a.BeadID)
}

func (w *pitchWatcher) saveStale() {
	data, err := json.Marshal(w.stale)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(w.stalePath), 0700)
	}
	if err == nil {
		err = os.WriteFile(w.stalePath+".tmp", data, 0600)
	}
	if err == nil {
		err = os.Rename(w.stalePath+".tmp", w.stalePath)
	}
	if err != nil {
		log.Printf("daemon: stale card: persist notices: %v", err)
	}
}
