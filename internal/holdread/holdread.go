// Package holdread reads, fresh and never from a display snapshot, the part of a project a
// write can leave unheld, and simulates a write on it (invariant I4 of the guarded holding
// layer). The CLI's verbs and cmdrd's write door both judge through it, with holding.Held.
package holdread

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
)

// Bead is a bead as the one predicate reads it.
func Bead(b *beads.Bead, parent string, blockers []string) holding.Bead {
	hb := holding.Bead{ID: b.ID, Parent: parent, Status: b.Status, BlockedBy: blockers, Labels: slices.Clone(b.Labels), Queued: b.ExecutionReady && b.PlanningCommander != "", QuestionWaiting: b.QuestionWaiting, HumanAnswerPending: b.HumanAnswerPending}
	Derive(&hb)
	return hb
}

// Derive recomputes what Held reads from a bead's status and labels (after a simulated label edit).
func Derive(hb *holding.Bead) {
	state, owner := beads.StepState(hb.Status, hb.Labels)
	hb.Gate = slices.Contains(hb.Labels, beads.GateLabel) || slices.Contains(hb.Labels, "kind:gate")
	hb.NeedsUser = state == beads.StateNeedsUser
	hb.HumanHold = slices.Contains(hb.Labels, "hold")
	hb.Superseded = slices.Contains(hb.Labels, "superseded") || slices.Contains(hb.Labels, "retired")
	hb.Owner, hb.Waits, hb.OperatorWaits, hb.Untyped, hb.Until = owner, 0, nil, "", false
	now := time.Now()
	for _, w := range beads.WaitsOf(hb.Labels) {
		if w.Kind == beads.WaitUser {
			hb.HumanHold = true
		}
		if !beads.WaitHolds(w, now) {
			continue // an overdue handoff: the Manager's move, held by nothing
		}
		if w.Kind == beads.WaitOperator {
			hb.OperatorWaits = append(hb.OperatorWaits, "operator-"+strings.TrimPrefix(w.Ref, "operator-"))
			continue
		}
		hb.Waits++
		if w.Untyped() && hb.Untyped == "" {
			hb.Untyped = w.Ref
		}
	}
	for _, l := range hb.Labels {
		if strings.HasPrefix(l, "until:") {
			hb.Until = true
		}
	}
}

// Load reads id, its parent chain, everything under it and waiting on it (and under those), and
// the status of its parent's other children and of every blocker: the closure a write to id can
// disturb. live answers whether an owner runs (nil: every owner is taken as live).
//
// Reads are batched: one fresh list of the ids (ListOptions.IDs) plus one FreshDetails per level
// of the closure, and only a status read (one list) for what is judged by status alone: the
// parent's other children (an open one holds the parent) and the blockers. So a close under a
// parent with hundreds of children costs a handful of br calls, not two per sibling. A closure of
// more than max fully read beads is not read on: ErrTooLarge, which the caller must not take as
// "nothing left unheld".
func Load(ctx context.Context, bc beads.Client, dir, id string, live func(string) (string, bool), max int) (*holding.Graph, error) {
	var g *holding.Graph
	err := beads.ReadCustodySnapshot(ctx, bc, dir, func(ctx context.Context) error {
		var err error
		g, err = load(ctx, bc, dir, id, live, max)
		return err
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

func load(ctx context.Context, bc beads.Client, dir, id string, live func(string) (string, bool), max int) (*holding.Graph, error) {
	l := &loader{ctx: ctx, bc: bc, dir: dir, g: holding.NewGraph(live), max: max}
	if err := l.full([]string{id}); err != nil {
		return nil, err
	}
	if _, ok := l.g.Beads[id]; !ok {
		return nil, fmt.Errorf("bead %s not found", id)
	}
	// Ancestors: the parent's last open child may be this bead.
	for cur, n := id, 0; n < 32; n++ {
		p := l.g.Beads[cur].Parent
		if p == "" {
			break
		}
		if err := l.full([]string{p}); err != nil {
			return nil, err
		}
		if _, ok := l.g.Beads[p]; !ok {
			break
		}
		cur = p
	}
	// What waits on or sits under id, and under those: one batched read per level.
	seen := map[string]bool{}
	for level := []string{id}; len(level) > 0; {
		var next, read []string
		for _, x := range level {
			if seen[x] {
				continue
			}
			seen[x] = true
			deps, err := l.bc.Dependents(ctx, dir, x)
			if err != nil {
				return nil, err
			}
			for _, d := range deps {
				switch d.Type {
				case "parent-child":
					l.g.Children[x] = appendNew(l.g.Children[x], d.IssueID)
					next, read = append(next, d.IssueID), append(read, d.IssueID)
				case "blocks":
					l.g.Dependents[x] = appendNew(l.g.Dependents[x], d.IssueID)
					read = append(read, d.IssueID)
					if x == id {
						next = append(next, d.IssueID)
					}
				}
			}
		}
		if err := l.full(read); err != nil {
			return nil, err
		}
		level = next
	}
	// The parent's other children keep the parent held while one is open: their status is enough
	// (none of them is judged; one that waits on id was read in full above).
	if p := l.g.Beads[id].Parent; p != "" {
		deps, err := l.bc.Dependents(ctx, dir, p)
		if err != nil {
			return nil, err
		}
		var kids []string
		for _, d := range deps {
			if d.Type == "parent-child" {
				l.g.Children[p] = appendNew(l.g.Children[p], d.IssueID)
				kids = append(kids, d.IssueID)
			}
		}
		if err := l.status(kids, p); err != nil {
			return nil, err
		}
	}
	// A bead is held back by its blockers (and its ancestors'): their status decides.
	var blockers []string
	for _, b := range l.g.Beads {
		blockers = append(blockers, b.BlockedBy...)
	}
	if err := l.status(blockers, ""); err != nil {
		return nil, err
	}
	return l.g, nil
}

// ErrTooLarge: the closure has more than max beads that need a full read; it was not judged.
var ErrTooLarge = errors.New("closure too large to judge")

type loader struct {
	ctx  context.Context
	bc   beads.Client
	dir  string
	g    *holding.Graph
	max  int
	read int // beads read in full
}

// unread is ids not in the graph yet, sorted and once each.
func (l *loader) unread(ids []string) []string {
	ids = slices.DeleteFunc(slices.Clone(ids), func(x string) bool {
		_, ok := l.g.Beads[x]
		return ok || x == ""
	})
	slices.Sort(ids)
	return slices.Compact(ids)
}

// list reads ids in one fresh list. An id the list leaves out is a short read until its own read
// says it is gone (beads.ErrNotFound): then it is simply no bead.
func (l *loader) list(ids []string) (map[string]beads.Bead, error) {
	out := map[string]beads.Bead{}
	if len(ids) == 0 {
		return out, nil
	}
	bs, err := l.bc.List(l.ctx, l.dir, beads.ListOptions{Status: "all", IDs: ids})
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		if slices.Contains(ids, b.ID) {
			out[b.ID] = b
		}
	}
	for _, x := range ids {
		if _, ok := out[x]; ok {
			continue
		}
		b, err := beads.FreshShow(l.ctx, l.bc, l.dir, x)
		// Typed provenance failures must precede the legacy local absence exception.
		if beads.IsCrossProjectError(err) {
			return nil, err
		}
		if errors.Is(err, beads.ErrNotFound) || (err == nil && b == nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[x] = *b
	}
	return out, nil
}

// full reads ids with their edges (one list, one FreshDetails).
func (l *loader) full(ids []string) error {
	ids = l.unread(ids)
	if len(ids) == 0 {
		return nil
	}
	if l.read+len(ids) > l.max {
		return fmt.Errorf("%w: more than %d beads", ErrTooLarge, l.max)
	}
	l.read += len(ids)
	bs, err := l.list(ids)
	if err != nil {
		return err
	}
	ds, err := beads.FreshDetails(l.ctx, l.bc, l.dir, ids)
	if err != nil {
		return err
	}
	for _, x := range ids {
		b, ok := bs[x]
		if !ok {
			continue
		}
		d, ok := ds[x]
		if !ok {
			return fmt.Errorf("no details read for %s", x)
		}
		var blockers []string
		for _, dep := range d.Deps {
			if dep.Type == "blocks" && dep.IssueID != dep.DependsOnID {
				blockers = append(blockers, dep.DependsOnID)
			}
		}
		l.g.Beads[x] = Bead(&b, d.Parent, blockers)
		l.g.Titles[x] = b.Title
	}
	return nil
}

// status reads ids for their status and labels only (one list): beads Held reads but never judges.
func (l *loader) status(ids []string, parent string) error {
	ids = l.unread(ids)
	bs, err := l.list(ids)
	if err != nil {
		return err
	}
	for _, x := range ids {
		if b, ok := bs[x]; ok {
			l.g.Beads[x] = Bead(&b, parent, nil)
			l.g.Titles[x] = b.Title
		}
	}
	return nil
}

func appendNew(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}
