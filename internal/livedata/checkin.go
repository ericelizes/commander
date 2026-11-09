package livedata

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// CheckinReport is the Commander check-in body: one line per workstream row as
// the sidebar shows it, the NEEDS YOU list and the sanity audit. cmdrd sends
// it and `cmdr graph check --audit` prints it on demand.
func CheckinReport(s Snapshot, nowSec int64, opt AuditOptions) string {
	var b strings.Builder
	facts := Checkin(s, nowSec, opt)
	fmt.Fprintf(&b, "Workflow errors: %d\nBlockers: %d\nAdvisories: %d\n", facts.Errors, facts.Blockers, facts.Advisories)
	for _, f := range facts.Findings {
		fmt.Fprintf(&b, "- [%s:%s] %s: %s\n", FindingKind(f), f.Check, f.Bead, f.Why)
	}
	b.WriteString("\n")
	if s.Degraded != nil {
		fmt.Fprintf(&b, "DEGRADED: some projects are missing or stale: %s\n\n", s.Degraded.Reason)
	}
	if len(s.Health) > 0 {
		fmt.Fprintf(&b, "HEALTH: %d problem(s) still failing (the board is not moving for these; act on them first):\n", len(s.Health))
		for _, h := range s.Health {
			first, _, _ := strings.Cut(h.Detail, "\n")
			fmt.Fprintf(&b, "- %s (since %s)\n", first, time.Unix(h.Since, 0).In(BoardZone()).Format(time.Kitchen))
		}
		b.WriteString("\n")
	}
	rows := Rows(s, nowSec)
	b.WriteString("Workstreams (# | title | state | next gate | agents | flag):\n")
	for _, r := range rows {
		state := r.Line.Text
		if r.Line.Age != "" {
			state += " (" + r.Line.Age + ")"
		}
		gate := r.NextGate
		if gate == "" {
			gate = "-"
		}
		flag := "-"
		if r.Flag > 0 {
			flag = fmt.Sprintf("⚑%d", r.Flag)
		}
		fmt.Fprintf(&b, "%d | %s | [%s] %s | %s | %d | %s\n", r.N, r.Title, r.Colour, state, gate, r.Agents, flag)
	}
	if stalled := facts.Stalled; len(stalled) > 0 {
		fmt.Fprintf(&b, "\n%s (%d; bead | workstream (its bead) | step | reason):\n", StalledHeader, len(stalled))
		for _, x := range stalled {
			fmt.Fprintf(&b, "- %s | %s (%s) | %s | %s\n", x.Bead, x.Workstream, x.WorkstreamBead, x.Step, x.Reason)
		}
	}
	if held := facts.Held; len(held) > 0 {
		fmt.Fprintf(&b, "\n%s (%d; on hold, so nothing here asks for action; workstream (its bead) | what the hold quiets | reason):\n", HeldHeader, len(held))
		for _, h := range held {
			fmt.Fprintf(&b, "- %s\n", h.line())
		}
	}
	if len(s.Wrong) > 0 {
		fmt.Fprintf(&b, "\n%s (%d):\n", MismatchHeader, len(s.Wrong))
		for _, w := range s.Wrong {
			text := w.Agent
			if text == "" {
				text = w.Text
			}
			fmt.Fprintf(&b, "- %s\n", text)
		}
	}
	fmt.Fprintf(&b, "\nNEEDS YOU (%d)\n", len(s.NeedsYou))
	for _, a := range s.NeedsYou {
		fmt.Fprintf(&b, "- %s | %s | %s\n", a.BeadID, a.Workstream, truncate(titleOf(a.Title, a.BeadID), 80))
	}
	suspects := facts.Suspects
	fmt.Fprintf(&b, "\nSanity audit: %d suspect%s\n", len(suspects), map[bool]string{true: "s"}[len(suspects) != 1])
	for _, x := range suspects {
		fmt.Fprintf(&b, "%s | %s | %s\n", x.Bead, x.ManagerRole, x.Why)
	}
	return b.String()
}

// CheckinFacts is what the check-in reports. A workstream whose root bead is on hold was
// put down on purpose: what would call for action there is counted on its one line instead
// (its stalled places, workflow errors and suspects). Review blockers and advisories ask
// nothing of Commander and NEEDS YOU cards can be answered, so they count as ever.
type CheckinFacts struct {
	Findings                     []DiagramFinding
	Errors, Blockers, Advisories int
	Stalled                      []StalledItem
	Suspects                     []Suspect
	Held                         []HeldWorkstream
}

// HeldWorkstream is the check-in's one line for a workstream on hold.
type HeldWorkstream struct {
	Title, Bead, Reason       string
	Stalled, Errors, Suspects int
}

func (h HeldWorkstream) line() string {
	quiet := fmt.Sprintf("%d stopped or stalled", h.Stalled)
	if h.Errors > 0 {
		quiet += fmt.Sprintf(", %d workflow error%s", h.Errors, map[bool]string{true: "s"}[h.Errors != 1])
	}
	if h.Suspects > 0 {
		quiet += fmt.Sprintf(", %d sanity-audit suspect%s", h.Suspects, map[bool]string{true: "s"}[h.Suspects != 1])
	}
	out := fmt.Sprintf("%s (%s) | %s", h.Title, h.Bead, quiet)
	// A comment's text, on a line the report reads as one entry.
	if reason := strings.Join(strings.Fields(h.Reason), " "); reason != "" {
		out += " | " + truncate(reason, 160)
	}
	return out
}

// Checkin splits the workflow findings, stalled places and suspects by hold. With no hold
// it is exactly what WorkflowFindings, StalledWork and AuditSnapshot say.
func Checkin(s Snapshot, nowSec int64, opt AuditOptions) CheckinFacts {
	var out CheckinFacts
	at := map[string]int{}
	byRole := map[string]int{}
	for _, ws := range s.Workstreams {
		if ws.Hold {
			at[ws.BeadID] = len(out.Held)
			byRole[ws.ManagerRole] = len(out.Held)
			out.Held = append(out.Held, HeldWorkstream{Title: titleOf(ws.Title, ws.BeadID), Bead: ws.BeadID, Reason: ws.HoldReason})
		}
	}
	for _, row := range displayOf(s, nowSec).Workstreams {
		if row.Diagram == nil {
			continue
		}
		i, held := at[row.BeadID]
		for _, f := range row.Diagram.Findings {
			switch FindingKind(f) {
			case "error":
				if held {
					out.Held[i].Errors++
					continue
				}
				out.Errors++
			case "blocker":
				out.Blockers++
			case "advisory":
				out.Advisories++
			}
			out.Findings = append(out.Findings, f)
		}
	}
	for _, x := range StalledWork(s, nowSec) {
		if i, held := at[x.WorkstreamBead]; held {
			out.Held[i].Stalled++
			continue
		}
		out.Stalled = append(out.Stalled, x)
	}
	for _, x := range AuditSnapshot(s, nowSec, opt) {
		if i, held := byRole[x.ManagerRole]; held {
			out.Held[i].Suspects++
			continue
		}
		out.Suspects = append(out.Suspects, x)
	}
	return out
}

// WorkflowFindings is the same per-workstream list that graph check reads.
// Each workstream already includes its tracks; do not count them twice.
func WorkflowFindings(s Snapshot, nowSec int64) []DiagramFinding {
	var out []DiagramFinding
	for _, row := range displayOf(s, nowSec).Workstreams {
		if row.Diagram != nil {
			out = append(out, row.Diagram.Findings...)
		}
	}
	return out
}

func FindingCounts(s Snapshot, nowSec int64) (errors, blockers, advisories int) {
	for _, f := range WorkflowFindings(s, nowSec) {
		switch FindingKind(f) {
		case "error":
			errors++
		case "blocker":
			blockers++
		case "advisory":
			advisories++
		}
	}
	return
}

// StalledItem is one place the check-in asks Commander to act on.
type StalledItem struct {
	Bead, Workstream, WorkstreamBead, Step, Reason string
}

// StalledWork names each stopped or stalled place. A row's line is its most urgent, and
// Needs you, waiting and working all outrank stalled, so it cannot be read off the rows:
// a stalled track is one place with the steps under it, and the rest of a workstream is
// judged by the board's own derivation without what outranks it (stalled.go). A line the
// board reads stalled counts here with no rule of its own.
func StalledWork(s Snapshot, nowSec int64) []StalledItem {
	rows := displayOf(s, nowSec).Workstreams
	b := newBoard(s, nowSec)
	var out []StalledItem
	for _, row := range rows {
		var items []StalledItem
		said := map[string]bool{}
		add := func(bead, step string, l line) {
			said[l.text] = true
			items = append(items, StalledItem{bead, row.Title, row.BeadID, step, reasonOf(l)})
		}
		stalledTrack := map[string]bool{}
		for _, t := range row.AllTracks() {
			if t.Kind == "stalled" {
				stalledTrack[t.BeadID] = true
				add(t.BeadID, truncate(titleOf(t.Title, t.BeadID), 80), line{text: t.Text, why: t.Why})
			}
		}
		if i := slices.IndexFunc(b.s.Workstreams, func(w SnapWorkstream) bool { return w.Project == row.Project && w.BeadID == row.BeadID }); i >= 0 {
			for _, p := range b.stalledPlaces(b.s.Workstreams[i], stalledTrack) {
				add(p.bead, p.title, p.line)
			}
		}
		// The row's own line, unless a place above already says it.
		if row.Kind == "stalled" && !said[row.Text] {
			add(row.BeadID, "-", line{text: row.Text, why: row.Why})
		}
		out = append(out, items...)
	}
	return out
}

func reasonOf(l line) string {
	if l.why != "" {
		return l.text + " · why: " + l.why
	}
	return l.text
}

const (
	StalledHeader  = "Stopped or stalled"
	HeldHeader     = "Held"
	MismatchHeader = "Screen mismatches"
)
