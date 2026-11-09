package livedata

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/store"
)

// ChatState is a running native chat's own state: the chat engine knows at
// once when a turn starts or ends, the store's copy lags it.
type ChatState struct {
	State  string
	Detail string
}

// overlayChats puts each running chat's state over the snapshot's copy of
// it. since keeps when each overlay began, so the time does not restart on
// every snapshot.
func overlayChats(s *Snapshot, chats map[string]ChatState, since map[string]SnapAgent, now int64) {
	for role := range since {
		if _, ok := chats[role]; !ok {
			delete(since, role)
		}
	}
	for role, c := range chats {
		a, has := s.Agents[role]
		working := c.State == store.AgentWorking
		if (!has && !working) || (working == (a.State == store.AgentWorking)) {
			delete(since, role)
			continue
		}
		next := SnapAgent{State: store.AgentWorking, Detail: c.Detail, Since: a.Since}
		if c.Detail == "" {
			next.Detail = a.Detail
		}
		if !has {
			next.Since = now
		}
		if !working {
			next = a
			next.State, next.Detail, next.Since = store.AgentIdle, "", now
		}
		if prev, ok := since[role]; ok && prev.State == next.State {
			next.Since = prev.Since
		}
		since[role] = next
		if s.Agents == nil {
			s.Agents = map[string]SnapAgent{}
		}
		s.Agents[role] = next
	}
}

// sinceTracker times the board states the derivation has no start for
// ("Stalled", "Readying"): from when cmdrd first showed the row in that
// state, kept in the store so a restart does not reset the clock.
type sinceTracker struct {
	loaded bool
	rows   map[string]store.BoardState
}

// A failed load (loadFailed) proves nothing about a row it does not show, so the
// row and its clock stay until a clean load says it has left the board.
func (t *sinceTracker) fill(ctx context.Context, st *store.Store, d *Display, now int64, loadFailed bool) error {
	if !t.loaded {
		t.rows = map[string]store.BoardState{}
		if st != nil {
			rows, err := st.BoardSince(ctx)
			if err != nil {
				return err
			}
			t.rows = rows
		}
		t.loaded = true
	}
	var errs []string
	save := func(key string, b store.BoardState) {
		if st != nil {
			if err := st.SetBoardSince(ctx, key, b); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	seen := map[string]bool{}
	track := func(key, kind, text, ageAs string, since *int64) {
		if *since != 0 {
			return
		}
		seen[key] = true
		state := kind + "|" + text
		if ageAs != "" {
			state = ageAs
		}
		e, ok := t.rows[key]
		if !ok || e.State != state {
			e = store.BoardState{State: state, Since: now}
			t.rows[key] = e
			save(key, e)
		}
		*since = e.Since
	}
	for i := range d.Workstreams {
		w := &d.Workstreams[i]
		track(w.Key, w.Kind, w.Text, w.AgeAs, &w.Since)
		var nested func(ts []BoardTrack)
		nested = func(ts []BoardTrack) {
			for j := range ts {
				tr := &ts[j]
				track(tr.Key, tr.Kind, tr.Text, tr.AgeAs, &tr.Since)
				nested(tr.Children)
			}
		}
		nested(w.Tracks)
	}
	for key := range t.rows {
		if !seen[key] && !loadFailed {
			delete(t.rows, key)
			save(key, store.BoardState{})
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("board since: %s", strings.Join(errs, "; "))
	}
	return nil
}

// BoardLines is the board as text, one line per row as the sidebar reads it:
// Commander and every workstream for Commander, only its own workstream for a
// Manager (role manager-<bead>).
func BoardLines(d Display, now int64, role string) []string {
	es := boardEntries(d, now, role, true)
	asks := 0
	for _, e := range es {
		if e.Ask {
			asks++
		}
	}
	var out []string
	headed := false
	for _, e := range es {
		switch {
		case e.Ask:
			if !headed {
				out, headed = append(out, fmt.Sprintf("Needs you (%d):", asks)), true
			}
			out = append(out, "  "+strings.TrimPrefix(e.Label, "ask ")+" "+e.Text)
		case e.Track:
			out = append(out, "  "+strings.TrimPrefix(e.Label, "#")+" "+e.Text)
		default:
			out = append(out, strings.TrimPrefix(e.Label, "#")+" "+e.Text)
		}
	}
	return out
}

// BoardEntries is role's board for the agents' turns (boardturn): BoardLines'
// lines with ids, kinds and age-free keys. Commander's own row is left out:
// it is the agent reading it, and its working/idle flip would change the
// board on every turn.
func BoardEntries(d Display, now int64, role string) []boardturn.Entry {
	es := boardEntries(d, now, role, true)
	keys := boardEntries(d, now, role, false)
	out := es[:0]
	for i, e := range es {
		if e.ID == "0" {
			continue
		}
		e.Key = keys[i].Text
		out = append(out, e)
	}
	return out
}

// boardEntries renders d for role; ages false leaves the "for 3m" out.
func boardEntries(d Display, now int64, role string, ages bool) []boardturn.Entry {
	var out []boardturn.Entry
	commander := role == "" || role == "commander"
	meta := func(since int64, working int, parts ...string) string {
		if working > 0 {
			parts = append(parts, fmt.Sprintf("%d working", working))
		}
		if since != 0 && ages {
			parts = append(parts, "for "+FmtAge(now-since))
		}
		if len(parts) == 0 {
			return ""
		}
		return " (" + strings.Join(parts, ", ") + ")"
	}
	if commander {
		c := d.Commander
		var parts []string
		if c.Unread > 0 {
			parts = append(parts, fmt.Sprintf("%d unread", c.Unread))
		}
		out = append(out, boardturn.Entry{ID: "0", Label: "#0", Kind: c.Kind, Text: "Commander: " + c.Text + meta(c.Since, 0, parts...)})
	}
	var mine []BoardRow
	for _, w := range d.Workstreams {
		if commander || w.ManagerRole == role || (w.ScopeRole != "" && w.ScopeRole == role) {
			mine = append(mine, w)
		} else if t, ok := nestedScope(w.Tracks, role); ok {
			// A nested Manager sees its own subtree: the row stands for it.
			w.Tracks = t.Children
			w.Title, w.Kind, w.Text, w.Since, w.Working, w.Progress, w.Why = w.Title+" › "+t.Title, t.Kind, t.Text, t.Since, t.Working, t.Progress, t.Why
			w.Gate = nil
			mine = append(mine, w)
		}
	}
	for _, w := range mine {
		pct := doneText(w.Progress, w.Text)
		gate := "no gates"
		if w.Gate != nil {
			gate = "next gate: " + w.Gate.Title + ", approved by " + w.Gate.Who
		}
		parts := []string{pct, gate}
		if w.Flag > 0 {
			parts = append(parts, fmt.Sprintf("⚑ %d", w.Flag))
		}
		id := strconv.Itoa(w.N)
		out = append(out, boardturn.Entry{ID: id, Label: "#" + id, Kind: w.Kind, BeadID: w.BeadID,
			Text: w.Title + ": " + w.Text + meta(w.Since, w.Working, parts...) + whyOf(w.Why)})
		for _, t := range w.AllTracks() {
			out = append(out, boardturn.Entry{ID: id + t.Letter, Label: "#" + id + t.Letter, Kind: t.Kind, BeadID: t.BeadID, Track: true,
				Text: t.Title + ": " + t.Text + meta(t.Since, t.Working, doneText(t.Progress, t.Text)) + whyOf(t.Why)})
		}
	}
	for _, a := range d.Asks {
		if !commander && !slices.ContainsFunc(mine, func(r BoardRow) bool { return r.Key == a.WsKey }) {
			continue
		}
		where := a.Workstream
		if a.Track != "" {
			where += " › " + a.Track
		}
		out = append(out, boardturn.Entry{ID: a.Key, Label: fmt.Sprintf("ask #%d%s", a.Rank, a.TrackLetter), Kind: "needs", Ask: true,
			Text: withCardWarning(a.Text, shownStaleWhy(a)) + " (" + where + ")"})
	}
	return out
}

// shownStaleWhy is the card warning people and agents see: a spec edit is only
// for the card's Manager (the stale mail), never a label.
func shownStaleWhy(a BoardAsk) string {
	if a.Stale == StaleSpec {
		return ""
	}
	return a.StaleWhy
}

// whyOf is a corrected row's reason as the text board shows it.
func whyOf(why string) string {
	if why == "" {
		return ""
	}
	return " · why: " + why
}

// doneText is the progress part of a row's line. A row with no step planned
// is not done: "100% done" beside "Not started" read as finished work.
func doneText(p Progress, text string) string {
	if p.Total == 0 && text == notPlannedText {
		return "0% done"
	}
	return fmt.Sprintf("%d%% done", percentOf(p))
}

// percentOf is how far along the work is.
func percentOf(p Progress) int {
	if p.Total == 0 {
		return 100
	}
	return (200*p.Done + p.Total) / (2 * p.Total) // rounded half up, as the app's Math.round
}

// markOverlaid derives Broken again over the overlaid agent states (store vs overlay: one state).
func markOverlaid(s *Snapshot, rows []ObjectiveRow, at time.Time) {
	s.Broken = OpenBeadInvariantAt(rows, func(owner string) string { return s.Agents[owner].State }, at)
	for i := range s.Workstreams {
		clearBroken(s.Workstreams[i].Steps)
	}
	markBroken(s.Workstreams, s.Broken)
}

// nestedScope finds the nested workstream whose own Manager is role.
func nestedScope(tracks []BoardTrack, role string) (BoardTrack, bool) {
	for _, t := range tracks {
		if t.ManagerRole != "" && t.ManagerRole == role {
			return t, true
		}
		if found, ok := nestedScope(t.Children, role); ok {
			return found, true
		}
	}
	return BoardTrack{}, false
}
