package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/holdread"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/wsmgr"
)

func runOrdinaryPlace(project, id, parent string, dry bool, out, stderr *os.File) int {
	p, err := resolveProject(project)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	bc := newBeadsClientFn()
	old, err := placeOrdinary(ctx, bc, p.Path, id, parent, os.Getenv("COMMANDER_ROLE"), dry)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	_ = json.NewEncoder(out).Encode(map[string]any{"project": project, "bead_id": id, "previous_parent": old, "parent": parent, "ordinary": true, "dry_run": dry})
	return 0
}

// A deliberately narrow parent-only edit: no ownership transfer, registration,
// card publication, provenance rewrite, labels or dependency changes.
func placeOrdinary(ctx context.Context, bc beads.Client, dir, id, parent, role string, dry bool) (string, error) {
	fail := func(s string) (string, error) { return "", fmt.Errorf("ordinary placement: %s; nothing written", s) }
	if callerIsOperator(role) {
		return fail("Operators must ask their Manager")
	}
	if id == "" || parent == "" || id == parent {
		return fail("distinct bead and parent required")
	}
	d, err := beads.FreshDetails(ctx, bc, dir, []string{id, parent})
	if err != nil {
		return "", err
	}
	b, err := beads.FreshShow(ctx, bc, dir, id)
	if err != nil {
		return "", err
	}
	p, err := beads.FreshShow(ctx, bc, dir, parent)
	if err != nil {
		return "", err
	}
	if b == nil || p == nil || b.ID != id || p.ID != parent || d[id].Parent == "" {
		return fail("existing nested bead and parent required")
	}
	if beads.IsWorkstream(b.Labels) || slices.Contains(b.Labels, "kind:operational") || slices.Contains(b.Labels, "kind:task") {
		return fail("workstreams, operational beads and Operator tasks are excluded")
	}
	for _, l := range b.Labels {
		if strings.HasPrefix(l, "spawn-attempt:") {
			return fail("Operator provenance cannot move")
		}
	}
	if !beads.IsWorkstream(p.Labels) || b.Status == "closed" || p.Status == "closed" || b.Status == "tombstone" || p.Status == "tombstone" || beads.Superseded(*b) || beads.Superseded(*p) {
		return fail("open ordinary bead and workstream parent required")
	}
	oldOwner, err := wsmgr.StrictOwner(ctx, bc, dir, id)
	if err != nil {
		return "", err
	}
	newOwner, err := wsmgr.StrictOwner(ctx, bc, dir, parent)
	if err != nil {
		return "", err
	}
	if oldOwner != newOwner {
		return fail("ownership transfer is not supported")
	}
	owners, err := ownerRoles(ctx, bc, dir, id)
	if err != nil {
		return "", err
	}
	if role != "" && role != "commander" && !roleAmong(role, owners) {
		return fail("only an owning Manager, Commander or user may reparent")
	}
	if err := beads.CheckPlacement(ctx, bc, dir, parent, beads.Placement{Self: id}); err != nil {
		return "", err
	}
	// Adding id -> parent must not close a path through either real parents or
	// blocking waits. Read every reachable node fresh, with a hard bound.
	seen := map[string]bool{}
	pending := []string{parent}
	for len(pending) > 0 {
		x := pending[0]
		pending = pending[1:]
		if x == id {
			return fail("resulting dependency cycle")
		}
		if seen[x] {
			continue
		}
		seen[x] = true
		if len(seen) > 128 {
			return fail("dependency closure exceeds 128 beads")
		}
		ds, e := beads.FreshDetails(ctx, bc, dir, []string{x})
		if e != nil {
			return "", e
		}
		v, ok := ds[x]
		if !ok {
			return fail("incomplete dependency evidence")
		}
		if v.Parent != "" {
			pending = append(pending, v.Parent)
		}
		for _, edge := range v.Deps {
			if edge.Type == "blocks" {
				pending = append(pending, edge.DependsOnID)
			}
		}
	}
	g, err := loadOrdinaryMoveGraph(ctx, bc, dir, id, parent)
	if err != nil {
		return "", fmt.Errorf("ordinary placement custody evidence: %w", err)
	}
	edits := map[string]holding.Edit{id: func(b *holding.Bead) { b.Parent = parent }}
	after := g.With(edits)
	// Every loaded descendant and ancestor is judged explicitly. Closure's
	// ancestor walk must not suppress descent from the moving bead.
	for x, before := range g.Beads {
		_, wasHeld, _ := holding.Held(before, g)
		_, held, why := holding.Held(after.Beads[x], after)
		if (wasHeld || before.Status == holding.StatusClosed) && !held && why != holding.WhyClosed {
			return fail(fmt.Sprintf("move would leave work unheld: %s", x))
		}
	}
	old := d[id].Parent
	if dry || old == parent {
		return old, nil
	}
	// The normal write door retains caller attribution and its own fresh checks.
	if err := bc.SetParent(ctx, dir, id, parent); err != nil {
		return "", err
	}
	return old, nil
}

// loadOrdinaryMoveGraph reads all actual descendants, both parent chains and
// blocker facts. Siblings need only fresh status for HasOpenChild. Unlike the
// general write loader, missing facts, cycles and bounds always refuse a move.
func loadOrdinaryMoveGraph(ctx context.Context, bc beads.Client, dir, id, parent string) (*holding.Graph, error) {
	g := holding.NewGraph(func(string) (string, bool) { return "", true })
	full := map[string]bool{}
	subtree := map[string]bool{}
	type request struct {
		id   string
		down bool
	}
	queue := []request{{id, true}, {parent, false}}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if full[r.id] && (!r.down || subtree[r.id]) {
			continue
		}
		if !full[r.id] {
			if _, exists := g.Beads[r.id]; !exists && len(g.Beads) >= maxClosure {
				return nil, fmt.Errorf("custody closure exceeds %d beads", maxClosure)
			}
			b, err := beads.FreshShow(ctx, bc, dir, r.id)
			if err != nil {
				return nil, err
			}
			ds, err := beads.FreshDetails(ctx, bc, dir, []string{r.id})
			if err != nil {
				return nil, err
			}
			d, ok := ds[r.id]
			if b == nil || b.ID != r.id || !ok {
				return nil, fmt.Errorf("missing custody facts for %s", r.id)
			}
			var blockers []string
			for _, e := range d.Deps {
				if e.Type == "blocks" {
					blockers = append(blockers, e.DependsOnID)
					queue = append(queue, request{e.DependsOnID, false})
				}
			}
			g.Beads[r.id] = holdread.Bead(b, d.Parent, blockers)
			full[r.id] = true
			if d.Parent != "" {
				queue = append(queue, request{d.Parent, false})
			}
		}
		deps, err := bc.Dependents(ctx, dir, r.id)
		if err != nil {
			return nil, err
		}
		for _, e := range deps {
			if e.Type != "parent-child" {
				continue
			}
			if r.down {
				queue = append(queue, request{e.IssueID, true})
			}
			if _, ok := g.Beads[e.IssueID]; !ok {
				if len(g.Beads) >= maxClosure {
					return nil, fmt.Errorf("custody closure exceeds %d beads", maxClosure)
				}
				b, err := beads.FreshShow(ctx, bc, dir, e.IssueID)
				if err != nil {
					return nil, err
				}
				if b == nil || b.ID != e.IssueID {
					return nil, fmt.Errorf("missing child %s", e.IssueID)
				}
				g.Beads[e.IssueID] = holdread.Bead(b, r.id, nil)
			}
			g.Children[r.id] = append(g.Children[r.id], e.IssueID)
		}
		if r.down {
			subtree[r.id] = true
		}
	}
	// Full child reads must agree with reverse parent edges. Detect every cycle
	// in the required ancestry rather than silently truncating the holding walk.
	for p, kids := range g.Children {
		for _, k := range kids {
			if full[k] && g.Beads[k].Parent != p {
				return nil, fmt.Errorf("inconsistent parent facts for %s", k)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(x string) error {
		if state[x] == 1 {
			return fmt.Errorf("cyclic custody evidence at %s", x)
		}
		if state[x] == 2 {
			return nil
		}
		if !full[x] {
			return fmt.Errorf("incomplete custody evidence at %s", x)
		}
		state[x] = 1
		b := g.Beads[x]
		refs := append([]string(nil), b.BlockedBy...)
		if b.Parent != "" {
			refs = append(refs, b.Parent)
		}
		for _, ref := range refs {
			if err := visit(ref); err != nil {
				return err
			}
		}
		state[x] = 2
		return nil
	}
	for x := range full {
		if err := visit(x); err != nil {
			return nil, err
		}
	}
	// Managers have no implicit custody. Query live runs only when a required
	// bead names an Operator; an unavailable or partial store cannot imply life.
	needsLive := false
	for x := range full {
		b := g.Beads[x]
		if (b.Owner != "" && !holding.IsManager(b.Owner)) || len(b.OperatorWaits) > 0 {
			needsLive = true
		}
	}
	if needsLive {
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			return nil, fmt.Errorf("custody live owners: %w", err)
		}
		defer cleanup()
		projects, err := st.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		running := map[string]bool{}
		for _, p := range projects {
			runs, err := st.ListAgentRunsByProject(ctx, p.ID, "running")
			if err != nil {
				return nil, err
			}
			for _, r := range runs {
				running[spawn.CanonicalRole(r.AgentName)] = true
			}
		}
		g.Live = func(owner string) (string, bool) {
			if !holding.IsManager(owner) && running[spawn.CanonicalRole(owner)] {
				return "working", true
			}
			return "", true
		}
	}
	return g, nil
}
