package livedata

import (
	"iter"
	"strconv"

	"github.com/example-user/commander/internal/store"
)

// RowLine is a workstream's status line as the sidebar shows it. Kind is the
// app's AgentLine kind: needs (red), working (green), waiting (yellow),
// stalled / quiet / idle / stuck / none (muted).
type RowLine struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Age   string `json:"age,omitempty"`
	Since int64  `json:"since,omitempty"`
}

// Colour is the dot colour the sidebar paints for the line.
func (l RowLine) Colour() string { return kindColour(l.Kind) }

func kindColour(kind string) string {
	switch kind {
	case "needs", "stalled", "stuck":
		return "red"
	case "waiting":
		return "yellow"
	case "working":
		return "green"
	}
	return "grey"
}

// Row is one workstream row as the sidebar shows it: the Board's row in the
// shape the audit reads.
type Row struct {
	N           int     `json:"n"`
	BeadID      string  `json:"bead_id"`
	Project     string  `json:"project"`
	Title       string  `json:"title"`
	ManagerRole string  `json:"manager_role"`
	Line        RowLine `json:"line"`
	Colour      string  `json:"colour"`
	NextGate    string  `json:"next_gate,omitempty"`    // title of the next gate the row shows
	NextGateID  string  `json:"next_gate_id,omitempty"` // its bead
	Agents      int     `json:"agents"`                 // working agents (Manager in a turn + live Operators)
	Flag        int     `json:"flag"`                   // the flag count: open asks NEEDS YOU lists under it
	Done        int     `json:"done"`
	Total       int     `json:"total"`
}

func stepClosed(s SnapStep) bool { return s.Status == "closed" || s.State == "done" }

// stepAlive: a claim's process decorates it; without owner_state (older cmdrd)
// the claim is taken as live.
func stepAlive(s SnapStep) bool { return s.OwnerState == "" || s.OwnerState == "working" }

func agentBusy(a SnapAgent, ok bool) bool {
	return ok && (a.State == store.AgentWorking || (a.State == store.AgentWaiting && a.Wait == ""))
}

func flattenSteps(steps []SnapStep) []SnapStep {
	n := 0
	var count func([]SnapStep)
	count = func(ss []SnapStep) {
		n += len(ss)
		for i := range ss {
			if len(ss[i].Children) > 0 {
				count(ss[i].Children)
			}
		}
	}
	count(steps)
	out := make([]SnapStep, 0, n)
	var walk func([]SnapStep)
	walk = func(ss []SnapStep) {
		for i := range ss {
			out = append(out, ss[i])
			if len(ss[i].Children) > 0 {
				walk(ss[i].Children)
			}
		}
	}
	walk(steps)
	return out
}

// Rows is every workstream's sidebar row: the snapshot's display, else Board at nowSec.
func Rows(s Snapshot, nowSec int64) []Row {
	d := displayOf(s, nowSec)
	out := make([]Row, 0, len(d.Workstreams))
	for _, w := range d.Workstreams {
		r := Row{N: w.N, BeadID: w.BeadID, Project: w.Project, Title: w.Title, ManagerRole: w.ManagerRole,
			Line: RowLine{Kind: w.Kind, Text: w.Text, Age: ageOf(w.Since, nowSec), Since: w.Since}, Colour: kindColour(w.Kind),
			Agents: w.Working, Flag: w.Flag, Done: w.Progress.Done, Total: w.Progress.Total}
		if w.Gate != nil {
			r.NextGate, r.NextGateID = w.Gate.Title, w.Gate.BeadID
		}
		out = append(out, r)
	}
	return out
}

// displayOf is the snapshot's board: what cmdrd drew, else derived from it.
func displayOf(s Snapshot, nowSec int64) Display {
	if s.Display != nil {
		return *s.Display
	}
	return Board(s, nowSec)
}

// FmtAge is a compact duration: 40s, 14m, 3h, 2d. The app formats the same way.
func FmtAge(secs int64) string {
	switch {
	case secs < 60:
		return strconv.FormatInt(max(secs, 0), 10) + "s"
	case secs < 3600:
		return strconv.FormatInt(secs/60, 10) + "m"
	case secs < 86400:
		return strconv.FormatInt(secs/3600, 10) + "h"
	}
	return strconv.FormatInt(secs/86400, 10) + "d"
}

func ageOf(since, now int64) string {
	if since == 0 {
		return ""
	}
	return FmtAge(now - since)
}

// stepValues traverses a tree without allocating a flattened copy for read-only
// board passes. The iterator also preserves early loop exits.
func stepValues(steps []SnapStep) iter.Seq[SnapStep] {
	return func(yield func(SnapStep) bool) {
		var walk func([]SnapStep) bool
		walk = func(ss []SnapStep) bool {
			for i := range ss {
				if !yield(ss[i]) {
					return false
				}
				if len(ss[i].Children) > 0 && !walk(ss[i].Children) {
					return false
				}
			}
			return true
		}
		walk(steps)
	}
}
