package livedata

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/example-user/commander/internal/nextactor"
)

// DescribeFunc writes a row's one-liner from its deterministic state: ok false
// while it is being written (the deterministic line then stays).
type DescribeFunc func(key, actor, reason, opener, facts string) (text, signal string, ok bool)

// applyDescriptions gives each active-mode row and track its model-written
// description for its current state and facts, when one is ready.
func applyDescriptions(describe DescribeFunc, snap *Snapshot, d *Display) {
	if describe == nil || snap.OrchestrationMode != "active" {
		describeSkipped.Do(func() {
			log.Printf("livedata: descriptions skipped (describer wired: %v, mode: %q)", describe != nil, snap.OrchestrationMode)
		})
		return
	}
	byKey := map[string]*SnapWorkstream{}
	for i := range snap.Workstreams {
		ws := &snap.Workstreams[i]
		byKey[ws.Project+":"+ws.BeadID] = ws
	}
	for i := range d.Workstreams {
		r := &d.Workstreams[i]
		ws := byKey[r.Key]
		if r.Actor == nil || ws == nil {
			continue
		}
		r.Text = HumanTimes(describeRow(describe, r.Key, *r.Actor, rowFacts(*snap, ws.Title, "", ws.Steps, *r.Actor), r.Text), time.Now())
		var tracks func([]BoardTrack)
		tracks = func(ts []BoardTrack) {
			for j := range ts {
				t := &ts[j]
				if t.Actor != nil {
					steps := []SnapStep{}
					if st, ok := stepByID(ws.Steps, t.BeadID); ok {
						steps = st.Children
					}
					t.Text = HumanTimes(describeRow(describe, t.Key, *t.Actor, rowFacts(*snap, ws.Title, t.Title, steps, *t.Actor), t.Text), time.Now())
				}
				tracks(t.Children)
			}
		}
		tracks(r.Tracks)
	}
}

func describeRow(describe DescribeFunc, key string, row nextactor.Row, facts, fallback string) string {
	if row.Lead.Actor == nextactor.Done {
		return fallback // "Complete": the app shows the Complete button
	}
	actor := string(row.Lead.Actor)
	if waitsOnAnotherStep(row.Lead) {
		actor = "waiting_on_step"
	}
	text, signal, ok := describe(key, actor, row.Lead.Reason, Opener(row.Lead), facts)
	if !ok {
		if via := row.Lead.Via; len(via) > 0 && row.Lead.Actor != nextactor.User && row.Lead.Actor != nextactor.Stuck {
			return "Waiting for: " + via[len(via)-1]
		}
		return placeholder(fallback)
	}
	if signal != "" && row.Lead.Actor == nextactor.Agent {
		noteDisagreement(key, signal)
	}
	return text
}

// placeholder is the rule-based line shown until the model's line is ready:
// no blocking chain and no "Needs you:" label, like the written lines.
func placeholder(text string) string {
	text = withoutChain(text)
	for _, p := range [][2]string{{"Working on “", "Agent: "}, {"Starting an agent on “", "Starting an agent: "}, {"Waiting for “", "Waiting for: "}} {
		if rest, ok := strings.CutPrefix(text, p[0]); ok {
			text = p[1] + rest
		}
	}
	text = strings.NewReplacer("“", "", "”", "").Replace(text)
	if rest := strings.TrimPrefix(text, "Needs you: "); rest != text && rest != "" {
		r := []rune(rest)
		r[0] = unicode.ToUpper(r[0])
		text = string(r)
	}
	return text
}

// withoutChain drops the blocking chain from a rule-based line: "After “X”:
// working on “Y”" reads "Working on “Y”"; "Needs you: Z (blocking “X”)" reads
// "Needs you: Z".
func withoutChain(text string) string {
	if strings.HasPrefix(text, "After “") {
		if i := strings.Index(text, "”: "); i > 0 {
			rest := []rune(text[i+len("”: "):])
			if len(rest) > 0 {
				rest[0] = unicode.ToUpper(rest[0])
			}
			text = string(rest)
		}
	}
	if i := strings.Index(text, " (blocking “"); i > 0 {
		if j := strings.Index(text[i:], "”)"); j > 0 {
			text = text[:i] + text[i+j+len("”)"):]
		}
	}
	return text
}

// Opener is the fixed start of a row's description, set by its state.
func Opener(r nextactor.Result) string {
	if waitsOnAnotherStep(r) {
		return "Waiting:"
	}
	switch r.Actor {
	case nextactor.User:
		return "Needs you:"
	case nextactor.Stuck:
		return "Stuck:"
	case nextactor.Agent:
		return "Working:"
	case nextactor.Commander:
		return "Commander:"
	case nextactor.External:
		who := strings.TrimSpace(r.Who)
		if who == "" {
			return "Waiting:"
		}
		return "Waiting on " + clip(who, 40) + ":"
	}
	switch strings.TrimPrefix(r.Reason, "blocked:") {
	case "paused":
		return "Paused:"
	case "withdrawn":
		return "Withdrawn:"
	case "done":
		return "Done:"
	}
	return "Quiet:"
}

// rowFacts is what the describer may say, as plain lines. It leaves out
// durations so a row is not rewritten just because a minute passed.
func rowFacts(snap Snapshot, workstream, track string, steps []SnapStep, row nextactor.Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workstream: %s\n", workstream)
	if track != "" {
		fmt.Fprintf(&b, "Track: %s\n", track)
	}
	lead := row.Lead
	title := lead.Bead
	if st, ok := stepByID(steps, lead.Bead); ok {
		title = st.Title
	} else if ws, ok := findWorkstreamStep(snap, lead.Bead); ok {
		title = ws.Title
	}
	fmt.Fprintf(&b, "Next move: %s (%s) on %q", lead.Actor, lead.Reason, title)
	if lead.Who != "" {
		fmt.Fprintf(&b, ", by %s", lead.Who)
	}
	b.WriteString("\n")
	if len(lead.Via) > 0 {
		fmt.Fprintf(&b, "It blocks, in order: %s\n", strings.Join(lead.Via, " <- "))
		if lead.Actor != nextactor.User && lead.Actor != nextactor.Stuck {
			// The move belongs to another step: this line's own work waits on it.
			fmt.Fprintf(&b, "Nothing runs for this line itself: it waits for %q to finish.\n", lead.Via[len(lead.Via)-1])
		}
	}
	also := []string{}
	if row.Agents > 0 {
		also = append(also, fmt.Sprintf("%d agents working", row.Agents))
	}
	for _, a := range []nextactor.Actor{nextactor.User, nextactor.Stuck, nextactor.External} {
		if n := row.Counts[a]; n > 0 && a != lead.Actor {
			also = append(also, fmt.Sprintf("%d %s", n, map[nextactor.Actor]string{nextactor.User: "need the user", nextactor.Stuck: "stuck", nextactor.External: "waiting outside"}[a]))
		}
	}
	if len(also) > 0 {
		fmt.Fprintf(&b, "Also in this workstream: %s\n", strings.Join(also, ", "))
	}
	for _, a := range snap.NeedsYou {
		if a.BeadID != lead.Bead {
			continue
		}
		if a.Decision != nil {
			fmt.Fprintf(&b, "Decision asked: %s\n", a.Decision.Question)
			if a.Decision.Context != "" {
				fmt.Fprintf(&b, "Decision context: %s\n", clip(a.Decision.Context, 600))
			}
			for _, o := range a.Decision.Options {
				fmt.Fprintf(&b, "Option: %s\n", clip(fmt.Sprintf("%+v", o), 200))
			}
		} else if a.Ask != "" {
			fmt.Fprintf(&b, "Asked: %s\n", a.Ask)
		}
	}
	// The rule-based line carries hold reasons, asks and waits through a chain.
	// Stuck, agent and Commander lines hold ticking durations: left out, so a
	// minute passing does not rewrite the row.
	if lead.Actor == nextactor.User || lead.Actor == nextactor.External || lead.Actor == nextactor.None {
		fmt.Fprintf(&b, "Rule-based line: %s\n", lead.Text)
	}
	n := 0
	for s := range stepValues(steps) {
		if s.Activity == nil || stepClosed(s) || n >= 5 {
			continue
		}
		fmt.Fprintf(&b, "Agent activity on %q: %s (%s)\n", s.Title, clip(s.Activity.Text, 300), s.Activity.Kind)
		n++
	}
	if lead.Actor == nextactor.Commander || strings.HasPrefix(lead.Reason, "commander_idle") {
		if a, ok := snap.Agents[lead.Who]; ok && a.Detail != "" {
			fmt.Fprintf(&b, "Commander is doing: %s\n", clip(a.Detail, 200))
		}
	}
	return HumanTimes(b.String(), time.Now())
}

func stepByID(steps []SnapStep, id string) (SnapStep, bool) {
	for s := range stepValues(steps) {
		if s.BeadID == id {
			return s, true
		}
	}
	return SnapStep{}, false
}

func findWorkstreamStep(snap Snapshot, id string) (SnapStep, bool) {
	for _, ws := range snap.Workstreams {
		if ws.BeadID == id {
			return SnapStep{BeadID: ws.BeadID, Title: ws.Title}, true
		}
		if s, ok := stepByID(ws.Steps, id); ok {
			return s, true
		}
	}
	return SnapStep{}, false
}

// disagreements: rows the rules call working while the classifier reads the
// agents' activity as blocked or waiting for review. Logged once an hour each.
var describeSkipped sync.Once

var disagreements = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func noteDisagreement(key, signal string) {
	disagreements.Lock()
	defer disagreements.Unlock()
	k := key + "|" + signal
	if at, ok := disagreements.at[k]; ok && time.Since(at) < time.Hour {
		return
	}
	disagreements.at[k] = time.Now()
	log.Printf("state check: %s reads working by the rules, but its agent activity says %s", key, signal)
}
