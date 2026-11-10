package daemon

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/livedata"
)

const (
	// Whole-project validation must not occupy backend read capacity for minutes.
	// A spent pass preserves seen/told and sends nothing; the next pass retries.
	invariantReadBudget = 15 * time.Second
	invariantEvery      = 3 * time.Minute
	invariantJitter     = 2 * time.Minute
)

// invariantWatcher enforces "every open bead has an agent, a recorded wait, or
// is the user's" (livedata.OpenBeadInvariant): it mails the owning Manager once per
// violating bead, with the ways to fix it. A bead must violate on two passes in
// a row before it is mailed (a bead just made is spawned onto an Operator within
// minutes), and is forgotten once fixed, so breaking again mails again. It reads
// a fresh DB-backed snapshot before deciding whether to mail.
type invariantWatcher struct {
	latest func() (livedata.Snapshot, bool)
	fresh  func(context.Context) (livedata.Snapshot, bool)
	// mail sends from Commander to a role.
	mail func(ctx context.Context, toRole, subject, body string) error
	// seen counts consecutive passes a bead violated; told marks those mailed.
	seen map[string]int
	told map[string]bool
}

func newInvariantWatcher(latest func() (livedata.Snapshot, bool), mail func(context.Context, string, string, string) error) *invariantWatcher {
	return &invariantWatcher{latest: latest, mail: mail, seen: map[string]int{}, told: map[string]bool{}}
}

// Run passes sequentially (one in flight) every 3-5 minutes.
func (w *invariantWatcher) Run(ctx context.Context) {
	for {
		w.pass(ctx)
		wait := invariantEvery + time.Duration(rand.Int64N(int64(invariantJitter)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (w *invariantWatcher) pass(ctx context.Context) {
	snap, ok := w.latest()
	if w.fresh != nil {
		snap, ok = w.fresh(ctx)
	}
	if !ok || snap.Degraded != nil {
		return // a stale or empty snapshot would flag (or clear) beads that are fine
	}
	now := map[string]bool{}
	byManager := map[string][]livedata.Violation{}
	for _, v := range snap.Broken {
		now[v.BeadID] = true
		w.seen[v.BeadID]++
		if w.seen[v.BeadID] >= 2 && !w.told[v.BeadID] {
			byManager[v.ManagerRole] = append(byManager[v.ManagerRole], v)
		}
	}
	for id := range w.seen {
		if !now[id] {
			delete(w.seen, id)
			delete(w.told, id)
		}
	}
	roles := make([]string, 0, len(byManager))
	for r := range byManager {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, role := range roles {
		vs := byManager[role]
		subject, body := invariantMail(vs)
		if err := w.mail(ctx, role, subject, body); err != nil {
			log.Printf("daemon: invariant: mail %s: %v", role, err)
			continue
		}
		for _, v := range vs {
			w.told[v.BeadID] = true
		}
	}
}

func invariantMail(vs []livedata.Violation) (subject, body string) {
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "- %s  %s\n", v.BeadID, v.Title)
		if v.Why != "" {
			fmt.Fprintf(&b, "  %s\n", v.Why)
		}
	}
	subject = fmt.Sprintf("[invariant] %d open bead(s) need a wait or an owner", len(vs))
	body = fmt.Sprintf(`Every open bead needs an agent on it, a recorded wait, or the user. These have none, which is broken data, not a status (the board reads "State missing"):

%s
Fix each one:
- record a wait: cmdr internal-set-objective --project <p> --bead <id> --waiting <ci:#N|review:<login>|main-ci:<sha>|prod-tag:<sha>|operator:<role>|user> (or --until merged for an owned job)
- or spawn an Operator on it: cmdr internal-spawn-operator --project <p> --step <id> --title ...
- or make it a gate for the user: an approval gate bead approved by user, or mark it needs-user
- or close it if it is done or dropped.
You are mailed once per bead; it is forgotten when fixed.`, b.String())
	return
}
