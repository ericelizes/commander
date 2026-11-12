package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
)

// Commander watches the workstreams the way a Manager watches its Operators:
// the minute pass finds what is wrong (no model call) and wakes Commander when
// something new is, plus a check-in every CommanderCheckinDefault while work is
// moving.
const (
	CommanderCheckinDefault = 30 * time.Minute
	CommanderCheckinMin     = 5 * time.Minute
	CommanderCheckinMax     = 60 * time.Minute

	stalledAfter     = 30 * time.Minute
	slowCIAfter      = 30 * time.Minute
	quietEscalation  = 40 * time.Minute // the Manager was told at quietAfter; Commander hears later
	onCommanderAfter = 2 * time.Minute  // a step waiting on Commander: soon, but not mid-reply
)

const commanderRole = "commander"

type watchIssueStore interface {
	WatchIssues(ctx context.Context) ([]store.WatchIssue, error)
	SetWatchIssues(ctx context.Context, issues []store.WatchIssue) error
}

// candidate is a possible issue: it matters once it has lasted `after`.
// since 0 means "from when the watch first saw it".
type candidate struct {
	key, ws, text string
	since         int64
	after         time.Duration
}

// commanderCandidates reads what may be wrong with each workstream, and
// whether any work is moving at all.
func commanderCandidates(s livedata.Snapshot) (cands []candidate, moving bool) {
	for _, ws := range s.Workstreams {
		a := s.Agents[ws.ManagerRole]
		busy := a.State == store.AgentWorking || (a.State == store.AgentWaiting && a.Wait == "")
		var leaves []livedata.SnapStep
		otherWaits, ciSince, ciStep := false, int64(0), ""
		onUser := false                     // an open gate the user approves: NEEDS YOU already shows it
		var onCommander []livedata.SnapStep // steps a Manager left waiting on Commander
		var walk func(steps []livedata.SnapStep)
		walk = func(steps []livedata.SnapStep) {
			for _, st := range steps {
				if st.Status == "closed" || st.State == "done" {
					continue
				}
				if st.Gate {
					onUser = onUser || slices.Contains(st.Approvers, "user")
					continue
				}
				for _, w := range strings.Split(st.Waiting, " · ") {
					if w == commanderRole || strings.HasPrefix(w, commanderRole+" ") {
						// Waiting on Commander is Commander's own queue: it wakes Commander, it is not work moving.
						onCommander = append(onCommander, st)
						continue
					}
					if w != "" && w != "you" {
						otherWaits = true
					}
					if strings.HasPrefix(w, "CI") && st.WaitSince > 0 && (ciSince == 0 || st.WaitSince < ciSince) {
						ciSince, ciStep = st.WaitSince, st.Title
					}
				}
				open := false
				for _, c := range st.Children {
					if c.Status != "closed" && c.State != "done" && !c.Gate {
						open = true
					}
				}
				if !open {
					leaves = append(leaves, st)
				}
				walk(st.Children)
			}
		}
		walk(ws.Steps)
		ops, needsOnly, toReview := 0, true, ""
		for _, l := range leaves {
			if strings.HasPrefix(l.Owner, "operator-") {
				ops++
			}
			if l.State != "needs-user" && l.Waiting != "you" {
				needsOnly = false
			}
			if l.State == "review" && toReview == "" {
				toReview = l.Title
			}
		}
		if busy || ops > 0 || otherWaits {
			moving = true
		}
		// A hold puts the workstream down on purpose: nobody moving it is no issue.
		if a.State == store.AgentStuck && !ws.Hold {
			cands = append(cands, candidate{key: ws.BeadID + ":stuck", ws: ws.BeadID, text: "Manager stuck: unread mail, but its input box has text", since: a.Since})
		}
		if ciSince > 0 {
			cands = append(cands, candidate{key: ws.BeadID + ":ci", ws: ws.BeadID, text: "CI still running (" + ciStep + ")", since: ciSince, after: slowCIAfter})
		}
		if len(leaves) > 0 && ops == 0 && !otherWaits && !needsOnly && !onUser && !ws.Hold {
			text := "stalled: nobody on its open work"
			if busy {
				text = "Manager active without new recorded stage/gate evidence; inspect the blocker and choose one concrete next action (activity alone is not delivery progress)"
			} else if toReview != "" {
				text += "; finished work to review: " + toReview
			}
			// Busy/idle chatter is one unchanged-evidence interval, not a reset.
			cands = append(cands, candidate{key: ws.BeadID + ":stalled:" + deliveryEvidenceKey(ws.Steps), ws: ws.BeadID, text: text, after: stalledAfter})
		}
		for _, st := range onCommander {
			cands = append(cands, candidate{key: ws.BeadID + ":commander:" + st.BeadID, ws: ws.BeadID, text: "waiting on you (Commander): " + st.Title, since: st.WaitSince, after: onCommanderAfter})
		}
		for _, o := range s.Watch[ws.ManagerRole].Operators {
			at := o.ActiveAt
			if at == 0 {
				at = o.Since
			}
			cands = append(cands, candidate{key: ws.BeadID + ":quiet:" + o.Role, ws: ws.BeadID, text: fmt.Sprintf("%s quiet on %s", o.Role, o.BeadID), since: at, after: quietEscalation})
		}
	}
	return cands, moving
}

// commanderPass records the issues and wakes Commander when one is new, or for
// its check-in while work is moving.
func (w opWatch) commanderPass(ctx context.Context, now time.Time, checkins map[string]store.Checkin) {
	if w.latest == nil || w.issues == nil {
		return
	}
	snap, ok := w.latest()
	if snap.Degraded != nil {
		// The snapshot is stale or empty: a check-in built on it would lie.
		w.degraded.readFailure(ctx, snap.Degraded.Reason, snap.Degraded.Timeout)
		return
	}
	if !ok {
		return
	}
	w.degraded.healthy()
	w.degraded.refreshRoute(ctx)
	prev, err := w.issues.WatchIssues(ctx)
	if err != nil {
		log.Printf("daemon: watch: %v", err)
		return
	}
	byKey := map[string]store.WatchIssue{}
	for _, p := range prev {
		byKey[p.Key] = p
	}
	cands, moving := commanderCandidates(snap)
	// The board's exact failures are Commander's repair queue, including overdue
	// task handoffs hidden by a sibling's genuine wait or user card. Persisted
	// WatchIssues.Told bounds successful delivery to once per failure/evidence.
	for _, failure := range livedata.Checkin(snap, now.Unix(), livedata.AuditOptions{}).Stalled {
		// Generic lack of delivery already has a bounded, evidence-keyed watch.
		// Keep that clock stable across busy/idle Manager turns.
		if slices.ContainsFunc(cands, func(c candidate) bool { return c.ws == failure.WorkstreamBead && strings.Contains(c.key, ":stalled:") }) {
			continue
		}
		digest := sha256.Sum256([]byte(failure.Bead + "|" + failure.Reason))
		cands = append(cands, candidate{key: fmt.Sprintf("%s:failure:%x", failure.WorkstreamBead, digest[:16]), ws: failure.WorkstreamBead,
			text: "RED automation failure: " + failure.Reason + ". Commander: ensure the owning Manager takes the named recovery action on " + failure.Bead + "; use current-holder/generation guards, never duplicate or restart healthy workers, reviews or user cards"})
	}
	titles := map[string]string{}
	for _, ws := range snap.Workstreams {
		titles[ws.BeadID] = ws.Title
	}
	var rows []store.WatchIssue
	var active []int
	fresh := false
	for _, c := range cands {
		p := byKey[c.key]
		since := c.since
		if since == 0 {
			since = p.Since
		}
		if since == 0 {
			since = now.Unix()
		}
		row := store.WatchIssue{Key: c.key, WS: c.ws, Text: c.text, Since: since, Told: p.Told && p.Since == since}
		row.Active = now.Sub(time.Unix(since, 0)) >= c.after
		if row.Active {
			active = append(active, len(rows))
			fresh = fresh || !row.Told
		}
		rows = append(rows, row)
	}

	c := checkins[commanderRole]
	c.Checked = now.Unix()
	due := c.Due != 0 && now.Unix() >= c.Due
	if c.Due == 0 {
		c.Due = now.Add(CommanderCheckinDefault).Unix()
	}
	if (fresh || (due && (moving || len(active) > 0))) && w.deliver != nil {
		list := make([]store.WatchIssue, len(active))
		for i, at := range active {
			list[i] = rows[at]
		}
		err := w.deliver(ctx, commanderRole, commanderPrompt(list, titles, snap, now))
		switch {
		case errors.Is(err, wake.ErrDeferred):
			// Mid-turn, or the user is talking to it: next pass.
		case err != nil:
			log.Printf("daemon: watch: check-in commander: %v", err)
		default:
			for _, at := range active {
				rows[at].Told = true
			}
			c.Due = now.Add(CommanderCheckinDefault).Unix()
		}
	} else if due {
		c.Due = now.Add(CommanderCheckinDefault).Unix()
	}
	if err := w.issues.SetWatchIssues(ctx, rows); err != nil {
		log.Printf("daemon: watch: %v", err)
	}
	if err := w.store.SetCheckin(ctx, commanderRole, c); err != nil {
		log.Printf("daemon: watch: %v", err)
	}
}

func commanderPrompt(issues []store.WatchIssue, titles map[string]string, snap livedata.Snapshot, now time.Time) string {
	workstreams := len(snap.Workstreams)
	sort.Slice(issues, func(i, j int) bool { return issues[i].Since < issues[j].Since })
	var b strings.Builder
	facts := livedata.Checkin(snap, now.Unix(), livedata.AuditOptions{})
	suspects, errors, blockers, advisories, stalled := facts.Suspects, facts.Errors, facts.Blockers, facts.Advisories, len(facts.Stalled)
	found := fmt.Sprintf("%d workflow errors, %d blockers, %d advisories, %d sanity-audit suspects, %d stopped or stalled, %d needs you, %d screen mismatches", errors, blockers, advisories, len(suspects), stalled, len(snap.NeedsYou), len(snap.Wrong))
	// Expected review blockers, advisories and needs-you items ask nothing of Commander.
	calm := len(suspects)+errors+stalled+len(snap.Wrong) == 0
	if len(issues) == 0 && (len(snap.Health) > 0 || snap.Degraded != nil) {
		// Never "all on track" while something keeps failing: the rows may only look calm.
		fmt.Fprintf(&b, "[mail] cmdrd: Check-in: %d workstream%s, NOT all on track, action needed: %d health problem%s (below), %s\n", workstreams, plural(workstreams), len(snap.Health), plural(len(snap.Health)), found)
		b.WriteString("Fix each HEALTH problem below, or tell the user what only they can fix. ")
	} else if len(issues) == 0 && !calm {
		fmt.Fprintf(&b, "[mail] cmdrd: Check-in: %d workstream%s, action needed: %s\n", workstreams, plural(workstreams), found)
		b.WriteString(checkinActions(errors, len(suspects), stalled, len(snap.Wrong)))
	} else if len(issues) == 0 {
		fmt.Fprintf(&b, "[mail] cmdrd: Check-in: %d workstream%s, all on track%s\n", workstreams, plural(workstreams), expectedCounts(len(snap.NeedsYou), blockers, advisories))
		b.WriteString("Nothing is required: end your turn. ")
	} else {
		fmt.Fprintf(&b, "[mail] cmdrd: Check-in: %d workstream%s, %d need%s attention\n", workstreams, plural(workstreams), len(issues), map[bool]string{true: "s"}[len(issues) == 1])
		for _, i := range issues {
			title := titles[i.WS]
			if title == "" {
				title = i.WS
			}
			fmt.Fprintf(&b, "- %s: %s, %s\n", title, i.Text, roundMin(now.Sub(time.Unix(i.Since, 0))))
		}
		b.WriteString("For each: an item waiting on you (Commander) is yours: do it now, in sidebar order, before anything lower. Otherwise nudge its Manager (cmdr internal-mail; add --interrupt only if it is doing the wrong thing), or tell the user if only they can fix it. ")
	}
	b.WriteString("\n\n")
	b.WriteString(livedata.CheckinReport(snap, now.Unix(), livedata.AuditOptions{}))
	if len(suspects) > 0 {
		b.WriteString("For each sanity-audit suspect: nudge the named Manager about that bead (cmdr internal-mail), or tell the user if only they can fix it.\n")
	}
	fmt.Fprintf(&b, "Next check-in in %s; set another with `cmdr internal-checkin --agent commander --in <%s..%s>`.",
		roundMin(CommanderCheckinDefault), roundMin(CommanderCheckinMin), roundMin(CommanderCheckinMax))
	return b.String()
}

// checkinActions names an action only for what needs one.
func checkinActions(errors, suspects, stalled, wrong int) string {
	var acts []string
	if errors > 0 {
		acts = append(acts, "the workflow errors")
	}
	if suspects > 0 {
		acts = append(acts, "the sanity-audit suspects")
	}
	if stalled > 0 {
		acts = append(acts, "the stopped or stalled rows")
	}
	var out string
	switch n := len(acts); n {
	case 0:
	case 1:
		out = "Act on " + acts[0] + " below. "
	default:
		out = "Act on " + strings.Join(acts[:n-1], ", ") + " and " + acts[n-1] + " below. "
	}
	if wrong > 0 {
		out += "Fix the screen mismatches listed below. "
	}
	return out
}

// expectedCounts is what the calm headline carries beside it, empty when nothing is expected.
func expectedCounts(needsYou, blockers, advisories int) string {
	var parts []string
	if needsYou > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting on the user", needsYou))
	}
	if blockers > 0 {
		parts = append(parts, fmt.Sprintf("%d review blocker%s", blockers, plural(blockers)))
	}
	if advisories == 1 {
		parts = append(parts, "1 advisory")
	} else if advisories > 1 {
		parts = append(parts, fmt.Sprintf("%d advisories", advisories))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, ", ")
}

// deliveryEvidenceKey ignores administrative task closures, output and busy state.
// It is deliberately not a source-code progress claim: only stages and gate receipts
// are available in the snapshot. Actual Operator work and external waits use their
// own watches rather than this Manager-only clock.
func deliveryEvidenceKey(steps []livedata.SnapStep) string {
	type fact struct {
		ID     string
		Closed bool
		Gates  []livedata.SnapGate
	}
	var facts []fact
	var walk func([]livedata.SnapStep)
	walk = func(rows []livedata.SnapStep) {
		for _, st := range rows {
			if st.Task || st.Superseded {
				continue
			}
			facts = append(facts, fact{st.BeadID, st.Status == "closed", st.Gates})
			walk(st.Children)
		}
	}
	walk(steps)
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	b, _ := json.Marshal(facts)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
