package livedata

import (
	"regexp"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// GateOver is a user approval gate that governs a step: approving it starts
// the step (Workflow unlocks), or did when the gate is already approved.
type GateOver struct {
	Gate     string
	Approved bool // the gate is closed: the user approved it
}

// UserGatesOver lists the user gates that govern step, in plan order. An approved gate covers what approving it started: the steps its
// run reaches through the next gate, the closed steps it started included,
// so a step behind a later gate is not covered. Gates are never covered
// themselves: each is its own decision.
func UserGatesOver(s Snapshot, step string) []GateOver {
	var out []GateOver
	for _, ws := range s.Workstreams {
		byID, parent, order := indexSteps(ws.Steps)
		target, ok := byID[step]
		if !ok || target.Gate {
			continue
		}
		for _, id := range order {
			g := byID[id]
			if !g.Gate || !hasUser(g.Approvers) || g.Superseded || parked(g) {
				continue
			}
			approved := stepClosed(g)
			for _, x := range unlockRun(byID, parent, order, id, approved) {
				if x == step {
					out = append(out, GateOver{Gate: id, Approved: approved})
					break
				}
			}
		}
	}
	return out
}

// CoveringApprovals are the approved user gates whose approval covers step.
func CoveringApprovals(s Snapshot, step string) []string {
	var out []string
	for _, g := range UserGatesOver(s, step) {
		if g.Approved {
			out = append(out, g.Gate)
		}
	}
	return out
}

// prRefRE: only words that name a PR. A bare "#N" is not one: cards say "#2"
// and "#3.1" for sidebar workstream numbers.
var prRefRE = regexp.MustCompile(`(?i)\b(?:PR|pull request)\s*#?(\d+)\b|/pull/(\d+)\b`)

// PRRefs are the PR numbers a card names in its words, plus the bead's pr:
// labels (<owner/repo>#N keeps only N). The door and the board share it.
func PRRefs(d *store.Decision, labels []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range PRLabels(labels) {
		out[n] = true
	}
	if d == nil {
		return out
	}
	text := append([]string{d.Question, d.Context}, d.Steps...)
	for _, o := range d.Options {
		text = append(text, o.Label, o.Implications)
	}
	return addPRWords(out, strings.Join(text, "\n"))
}

func addPRWords(out map[string]bool, text string) map[string]bool {
	for _, m := range prRefRE.FindAllStringSubmatch(text, -1) {
		for _, n := range m[1:] {
			if n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// PRLabels are the PR numbers of a bead's pr:<n> labels.
func PRLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if n, ok := strings.CutPrefix(l, "pr:"); ok && n != "" {
			_, num := splitPRRef(n)
			out = append(out, num)
		}
	}
	return out
}

// TicketLabels returns the Linear ids of ticket:<ID> labels (TEAM-4321), upper-cased:
// how a workstream or track names a ticket without it being in the title.
func TicketLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if n, ok := strings.CutPrefix(l, "ticket:"); ok && n != "" {
			out = append(out, strings.ToUpper(n))
		}
	}
	return out
}

// hasToken reports whether id occurs in text as a whole bead id: not inside a
// longer one (w.1.2 is not in w.1.20 or x-w.1.2).
func hasToken(text, id string) bool {
	for i := 0; id != "" && i <= len(text)-len(id); {
		j := strings.Index(text[i:], id)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(id)
		if (j == 0 || !idChar(text[j-1])) && (end == len(text) || !idChar(text[end]) || text[end] == '.' && (end+1 == len(text) || !idChar(text[end+1]))) {
			return true
		}
		i = j + 1
	}
	return false
}

func idChar(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// adoptHeldCards finds the user gates whose ask is already a card on an
// ancestor (a track): the card names the gate or the gate's PR. Such a gate
// is not waiting on a pitch; it reads as Needs you, with the card on the
// ancestor (heldCard), until the card is moved down.
func (b *board) adoptHeldCards() {
	up := map[string]string{}
	var walk func(parent string, steps []SnapStep)
	walk = func(parent string, steps []SnapStep) {
		for _, st := range steps {
			if parent != "" {
				up[st.BeadID] = parent
			}
			walk(st.BeadID, st.Children)
		}
	}
	for _, w := range b.s.Workstreams {
		walk(w.BeadID, w.Steps)
	}
	for key, p := range b.noPitch {
		if p.Proposed != "" || !p.Gate {
			continue
		}
		gate, ok := b.byID[p.BeadID]
		if !ok {
			continue
		}
		gatePRs := addPRWords(map[string]bool{}, gate.Title+"\n"+gate.Review)
		for _, n := range gate.PR {
			gatePRs[n] = true
		}
		for anc, seen := up[p.BeadID], 0; anc != "" && seen < 64; anc, seen = up[anc], seen+1 {
			for _, n := range b.s.NeedsYou {
				if n.Project != p.Project || n.BeadID != anc || n.Decision == nil {
					continue
				}
				text := []string{n.Decision.Question, n.Decision.Context}
				for _, o := range n.Decision.Options {
					text = append(text, o.Label, o.Implications)
				}
				joined := strings.Join(text, "\n")
				named := hasToken(joined, p.BeadID) || (len(gate.Title) > 8 && strings.Contains(strings.ToLower(joined), strings.ToLower(gate.Title)))
				for pr := range addPRWords(map[string]bool{}, joined) {
					if gatePRs[pr] {
						named = true
					}
				}
				if named {
					b.heldCard[key] = anc
					delete(b.noPitch, key)
				}
			}
		}
	}
}
