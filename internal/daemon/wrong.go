package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
)

// linearSelfFix ends Commander's Linear lines: per standing decision d27,
// Commander updates the ticket's status in Linear itself instead of asking the user.
const linearSelfFix = "Update the ticket's status in Linear yourself to match the work (decision d27)."

// ticketDoneSelfFix ends Commander's ticket-done line: a Done ticket beside open
// steps may mean the work or the ticket is stale, so Commander checks which first.
const ticketDoneSelfFix = "Check whether the work or the ticket is out of date, and fix that one (decision d27)."

// noTicketSelfFix ends Commander's no-ticket line: every workstream is tracked in
// Linear, so Commander finds or files the ticket rather than asking the user.
const noTicketSelfFix = "Find its ticket in Linear or file one, then add the id to its title or give it the label ticket:<ID>. If you can't reach Linear, tell the user."

// gateWarnWindow is how far back an agent blocked on an unapproved plan still counts.
const gateWarnWindow = 30 * time.Minute

// wrongOnScreen is every place the screen and what is happening disagree, in
// one list: the app's health badge shows it and Commander's board carries it,
// so neither the user nor Commander has to notice a mismatch by eye.
func wrongOnScreen(d livedata.Display, workstreams []livedata.SnapWorkstream, sprint linearsync.Response, home string, now time.Time) []livedata.SnapWrong {
	var out []livedata.SnapWrong
	add := func(key, target, text string) {
		out = append(out, livedata.SnapWrong{Key: key, Target: target, Text: text})
	}
	rows := map[string]livedata.BoardRow{}
	for _, w := range d.Workstreams {
		rows[w.Key] = w
		if w.Why != "" {
			add("why:"+w.Key, w.Key, fmt.Sprintf("#%d %s: %s. Its Manager is picking it up", w.N, plain(w.Title), whyWords(w.Why)))
		}
		for _, t := range w.AllTracks() {
			if t.Why != "" {
				add("why:"+t.Key, w.Key, fmt.Sprintf("#%d%s %s: %s. Its Manager is picking it up", w.N, t.Letter, plain(t.Title), whyWords(t.Why)))
			}
		}
	}
	for _, a := range d.Asks {
		w, ok := rows[a.WsKey]
		if !ok {
			continue
		}
		kind, label := w.Kind, fmt.Sprintf("#%d", w.N)
		for _, t := range w.AllTracks() {
			if a.TrackLetter != "" && t.Letter == a.TrackLetter {
				kind, label = t.Kind, label+t.Letter
			}
		}
		if kind != "needs" {
			add("ask-hidden:"+a.Key, a.WsKey, fmt.Sprintf("#%d%s %q is waiting on you, but row %s %s shows agents at work instead", a.Rank, a.TrackLetter, a.Text, label, plain(w.Title)))
		}
		if a.StaleWhy != "" && a.Stale != livedata.StaleSpec {
			add("stale:"+a.Key, a.WsKey, fmt.Sprintf("#%d%s %q may be out of date (%s). Check it still holds before you answer", a.Rank, a.TrackLetter, a.Text, strings.ToLower(a.StaleWhy[:1])+a.StaleWhy[1:]))
		}
	}
	if sprint.State == "ok" || sprint.State == "partial" {
		// The app only reads Linear: the badge tells the user what to do there, Commander's copy tells it to update the status itself (d27).
		linear := func(key, target, fact, userFix, selfFix string) {
			out = append(out, livedata.SnapWrong{Key: key, Target: target, Text: fact + userFix, Agent: fact + ". " + selfFix})
		}
		carriers := ticketCarriers(d, workstreams)
		for _, i := range sprint.FreshIssues() {
			cs := carriers[strings.ToUpper(i.ID)]
			if i.StatusType == "completed" || i.StatusType == "canceled" {
				for _, c := range cs {
					if w := c.row; i.StatusType == "completed" && w.Progress.Total > w.Progress.Done {
						linear("ticket-done:"+i.ID, w.Key, fmt.Sprintf("%s %s is Done in Linear, but its workstream #%d %s still has %d steps open", i.ID, i.Title, w.N, plain(w.Title), w.Progress.Total-w.Progress.Done), "", ticketDoneSelfFix)
						break
					}
				}
				continue
			}
			target, agent := "", false
			for n, c := range cs {
				if n == 0 {
					target = c.row.Key
				}
				agent = agent || c.working
			}
			switch {
			// Only In Progress claims an agent: In Review, Approval and Blocked rightly sit idle.
			case strings.EqualFold(strings.TrimSpace(i.StatusName), "In Progress") && !agent:
				linear("linear:"+i.ID, target, fmt.Sprintf("%s %s is %s in Linear, but no agent is working on it", i.ID, i.Title, i.StatusName), ". Give it to Commander or update it in Linear", linearSelfFix)
			case i.StatusType != "started" && agent:
				linear("linear:"+i.ID, target, fmt.Sprintf("%s %s is %s in Linear, but an agent is working on it", i.ID, i.Title, i.StatusName), ". Update it in Linear", linearSelfFix)
			}
		}
		for _, ws := range workstreams {
			key := ws.Project + ":" + ws.BeadID
			if r, on := rows[key]; on && len(livedata.WorkstreamTickets(ws)) == 0 {
				linear("no-ticket:"+key, key, fmt.Sprintf("#%d %s has no Linear ticket", r.N, plain(r.Title)), ". Commander needs to find or file it", noTicketSelfFix)
			}
		}
	}
	for _, g := range recentGateWarnings(home, now) {
		target, name := "", g.Scope
		for _, w := range d.Workstreams {
			if w.BeadID != "" && (g.Scope == w.BeadID || strings.HasPrefix(g.Scope, w.BeadID+".")) {
				target, name = w.Key, fmt.Sprintf("#%d %s", w.N, plain(w.Title))
			}
		}
		add("gate:"+g.Gate, target, fmt.Sprintf("Agents on %s tried to start before its plan was approved. Approve the plan, or tell its Manager to wait", name))
	}
	return out
}

type gateWarning struct{ Time, Scope, Gate string }

var ticketGroup = regexp.MustCompile(`\s*\((?:[A-Z][A-Z0-9]{1,9}-\d{1,6}[,\s]*)+\)`)

// plain is a title without its "(TEAM-1, TEAM-2)" ticket ids, as the sidebar shows it.
func plain(title string) string { return strings.TrimSpace(ticketGroup.ReplaceAllString(title, "")) }

// whyWords is a row's "why" as part of a sentence.
func whyWords(why string) string {
	if strings.HasPrefix(why, "Operator is no longer running") {
		return "an agent stopped without reporting back"
	}
	return strings.ToLower(why[:1]) + why[1:]
}

// recentGateWarnings is the distinct open gates agents ran into within gateWarnWindow.
func recentGateWarnings(home string, now time.Time) []gateWarning {
	f, err := os.Open(filepath.Join(home, "logs", "workflow-gate-warnings.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]gateWarning{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var g gateWarning
		if json.Unmarshal(sc.Bytes(), &g) != nil || g.Gate == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, g.Time); err != nil || now.Sub(t) > gateWarnWindow {
			continue
		}
		seen[g.Gate] = g
	}
	out := make([]gateWarning, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gate < out[j].Gate })
	return out
}

// wrongEntries is the list as lines on Commander's board.
func wrongEntries(ws []livedata.SnapWrong) []boardturn.Entry {
	out := make([]boardturn.Entry, 0, len(ws))
	for _, w := range ws {
		text := w.Agent
		if text == "" {
			text = w.Text + ". Fix it, or tell the user."
		}
		out = append(out, boardturn.Entry{ID: "wrong:" + w.Key, Label: "Wrong on screen:", Kind: "wrong", Text: text, Key: text})
	}
	return out
}
