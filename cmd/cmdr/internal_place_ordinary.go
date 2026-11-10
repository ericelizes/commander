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
	g, err := loadGraph(ctx, bc, dir, id)
	if err != nil {
		return "", fmt.Errorf("ordinary placement custody evidence: %w", err)
	}
	edits := map[string]holding.Edit{id: func(b *holding.Bead) { b.Parent = parent }}
	if left := holding.NewlyUnheld(g, id, edits); len(left) > 0 {
		return fail(fmt.Sprintf("move would leave work unheld: %v", left))
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
