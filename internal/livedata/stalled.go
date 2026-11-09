package livedata

import (
	"slices"

	"github.com/example-user/commander/internal/store"
)

// heldElsewhere is the wait a copy gives a step that something outside the judged steps
// holds: a blocker, a card under it, a wait on its parent. It is never shown.
const heldElsewhere = "held elsewhere"

// stalledPlace is one place the board's derivation reads stalled; title "-" is the level itself.
type stalledPlace struct {
	bead, title string
	line        line
}

// stalledPlaces judges a workstream's own work and each of its tracks not already
// stalled. The row's line stops at its most urgent and a line names one thing, so each
// open step is read alone through workstreamLine, deepest first (an open parent and child
// are one place), and then the whole level, for what no step says (a stuck Manager, a
// pitch owed on the workstream bead). A reacting Manager is picking all of it up.
func (b board) stalledPlaces(ws SnapWorkstream, stalledTrack map[string]bool) []stalledPlace {
	if managerReacting(b.s, ws.ManagerRole) {
		return nil
	}
	b.steps = map[string]SnapStep{}
	for _, s := range flattenSteps(ws.Steps) {
		b.steps[s.BeadID] = s
	}
	b.openGates = map[string]SnapGateNode{}
	for _, g := range ws.GateNodes {
		if g.Status != "passed" {
			b.openGates[g.BeadID] = g
		}
	}
	withoutNested := func(v SnapWorkstream) SnapWorkstream {
		v.Steps = slices.DeleteFunc(slices.Clone(v.Steps), func(s SnapStep) bool { return s.Track })
		return v
	}
	out := b.stalledLevel(withoutNested(ws), "-")
	var walk func(steps []SnapStep)
	walk = func(steps []SnapStep) {
		for _, t := range openTracksIn(steps) {
			// A parent of nested workstreams has no work of its own to read.
			if !stalledTrack[t.BeadID] && len(openTracksIn(t.Children)) == 0 {
				out = append(out, b.stalledLevel(withoutNested(trackWorkstream(ws, t)), truncate(titleOf(t.Title, t.BeadID), 80))...)
			}
			walk(t.Children)
		}
	}
	walk(ws.Steps)
	return out
}

// stalledLevel is the stalled places of a workstream or track: its steps, then its own
// line unless a step said the same.
func (b board) stalledLevel(ws SnapWorkstream, title string) []stalledPlace {
	manager, hasManager := b.s.Agents[ws.ManagerRole]
	ws.Steps = b.withoutCards(ws.Steps, ws.Project)
	var out []stalledPlace
	if t, isTrack := b.steps[ws.BeadID]; isTrack && b.holdsChildren(t) {
		ws.Steps = heldSteps(ws.Steps)
		out = stoppedOwner(t)
	}
	out = append(out, b.stalledSteps(ws, ws.Steps)...)
	whole := ws
	whole.Steps = b.heldFromOutside(ws.Steps)
	l := b.workstreamLine(whole, manager, hasManager, 0)
	if l.kind != "stalled" || slices.ContainsFunc(out, func(p stalledPlace) bool { return p.line.text == l.text }) {
		return out
	}
	if _, hidden := b.hiddenOperator(ws); hidden {
		return out
	}
	return append(out, stalledPlace{ws.BeadID, title, l})
}

// stalledSteps reads each open step alone, its children first. A live Operator holds what
// is under it, and a step that is held, by a wait or a blocker, holds its children.
func (b board) stalledSteps(ws SnapWorkstream, steps []SnapStep) []stalledPlace {
	var out []stalledPlace
	for _, x := range steps {
		if stepClosed(x) || parked(x) || x.Superseded || operatorOn(x) {
			continue
		}
		kids := x.Children
		var stopped []stalledPlace
		if b.holdsChildren(x) {
			kids = heldSteps(kids)
			stopped = stoppedOwner(x)
		}
		if under := b.stalledSteps(ws, kids); len(stopped)+len(under) > 0 {
			out = append(append(out, stopped...), under...)
			continue
		}
		alone := trackWorkstream(ws, x)
		alone.NowStep, alone.Steps = "", b.heldFromOutside([]SnapStep{x})
		// A stuck Manager is said once, by the level.
		manager, hasManager := b.s.Agents[ws.ManagerRole]
		if manager.State == store.AgentStuck {
			manager.State = store.AgentIdle
		}
		if l := b.workstreamLine(alone, manager, hasManager, 0); l.kind == "stalled" {
			out = append(out, stalledPlace{x.BeadID, truncate(titleOf(x.Title, x.BeadID), 80), l})
		}
	}
	return out
}

// stoppedOwner names a stopped Operator on a held step with open children. The board reads
// an Operator only on a leaf, so here nothing else would say it: the hold quiets the children.
func stoppedOwner(s SnapStep) []stalledPlace {
	if l, ok := goneLine([]SnapStep{s}); ok && hasOpen(s.Children) {
		return []stalledPlace{{s.BeadID, truncate(titleOf(s.Title, s.BeadID), 80), l}}
	}
	return nil
}

// holdsChildren: s has a wait that holds, or an open blocker outside it.
func (b board) holdsChildren(s SnapStep) bool {
	return b.waitHolds(b.heldFromOutside([]SnapStep{s})[0])
}

// heldSteps gives each step with no wait of its own the hidden wait. Facts (a stopped
// Operator, a review acted on, an overdue review) still count: the board reads them first.
func heldSteps(steps []SnapStep) []SnapStep {
	steps = slices.Clone(steps)
	for i := range steps {
		if steps[i].Waiting == "" {
			steps[i].Waiting = heldElsewhere
		}
	}
	return steps
}

// withoutCards drops the steps holding the user's move and keeps the work under them, held
// by that card. A parent left with no open work of its own but the card is held the same way.
// A ready ask whose pitch is owed stays: that is the Manager's move.
func (b board) withoutCards(steps []SnapStep, project string) []SnapStep {
	var out []SnapStep
	for _, s := range steps {
		key := project + "\x00" + s.BeadID
		_, pitchOwed := b.noPitch[key]
		_, heldCard := b.heldCard[key]
		if !stepClosed(s) && !parked(s) && (b.needsIDs[s.BeadID] || heldCard || (onUser(s) && !pitchOwed)) {
			out = append(out, heldSteps(b.withoutCards(s.Children, project))...)
			continue
		}
		kids := b.withoutCards(s.Children, project)
		if s.Waiting == "" && hasOpen(s.Children) && !hasOpen(kids) {
			s.Waiting = heldElsewhere
		}
		s.Children = kids
		out = append(out, s)
	}
	return out
}

func hasOpen(steps []SnapStep) bool {
	return slices.ContainsFunc(steps, func(s SnapStep) bool { return !stepClosed(s) && !parked(s) })
}

// heldFromOutside gives a wait to each step with an open blocker outside these steps: it
// is not ready, so nothing here is for the Manager to move until that is done. The blocker
// is judged on its own, and holds nothing if it waits on these steps itself.
func (b board) heldFromOutside(steps []SnapStep) []SnapStep {
	inside := map[string]bool{}
	for _, s := range flattenSteps(steps) {
		inside[s.BeadID] = true
	}
	var mark func([]SnapStep) []SnapStep
	mark = func(level []SnapStep) []SnapStep {
		level = slices.Clone(level)
		for i, s := range level {
			if s.Waiting == "" && slices.ContainsFunc(s.BlockedBy, func(id string) bool {
				return b.holdsFrom(id, inside)
			}) {
				level[i].Waiting = heldElsewhere
			}
			level[i].Children = mark(s.Children)
		}
		return level
	}
	return mark(steps)
}

// holdsFrom: id is open, outside inside, and does not wait on anything open in it. Those
// steps are what id waits for, so waiting on id would hide the work everything needs.
func (b board) holdsFrom(id string, inside map[string]bool) bool {
	next, open := b.awaits(id)
	if inside[id] || !open {
		return false
	}
	seen := map[string]bool{id: true}
	var reaches func(string) bool
	reaches = func(at string) bool {
		if seen[at] {
			return false
		}
		seen[at] = true
		after, open := b.awaits(at)
		return open && (inside[at] || slices.ContainsFunc(after, reaches))
	}
	return !slices.ContainsFunc(next, reaches)
}

// awaits: whether id is open, and what it cannot finish before: a gate's stage, a step's
// blockers and children. A person's approval waits on nobody's work, its stage's included.
func (b board) awaits(id string) ([]string, bool) {
	if g, ok := b.openGates[id]; ok {
		if g.Verifier == store.VerifierUser {
			return nil, true
		}
		return append(slices.Clone(g.BlockedBy), g.Stage), true
	}
	s, ok := b.byID[id]
	if !ok || stepClosed(s) || parked(s) || s.Superseded {
		return nil, false
	}
	next := slices.Clone(s.BlockedBy)
	for _, c := range s.Children {
		next = append(next, c.BeadID)
	}
	return next, true
}
