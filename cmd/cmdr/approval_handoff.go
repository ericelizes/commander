package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/wsmgr"
)

// holdApprovedWork records dispatch custody before closing the last blocker.
// A real successor (Operator, next wait or user decision) clears the receipt.
func holdApprovedWork(ctx context.Context, bc beads.Client, dir, gate string) error {
	all, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return err
	}
	var ids []string
	for _, b := range all {
		if b.ID != gate && b.Status != "closed" && b.Status != beads.StatusDeferred && !slices.Contains(b.Labels, beads.GateLabel) && !slices.Contains(b.Labels, "kind:gate") && !slices.Contains(b.Labels, "kind:task") && b.DependencyCount > 0 {
			ids = append(ids, b.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	details, err := beads.FreshDetails(ctx, bc, dir, ids)
	if err != nil {
		return err
	}
	writes := beads.NewStateWrite(bc)
	now := time.Now()
	for _, id := range ids {
		d, ok := details[id]
		if !ok {
			return fmt.Errorf("missing dependency details for %s", id)
		}
		released := false
		for _, dep := range d.Deps {
			if dep.Type == "blocks" && dep.DependsOnID == gate {
				released = true
			}
		}
		if !released {
			continue
		}
		held := false
		for _, dep := range d.Deps {
			if dep.Type != "blocks" || dep.DependsOnID == gate {
				continue
			}
			blocker, err := beads.FreshShow(ctx, bc, dir, dep.DependsOnID)
			if err != nil {
				return err
			}
			if blocker == nil {
				return fmt.Errorf("missing blocker %s", dep.DependsOnID)
			}
			if blocker.Status != "closed" && blocker.Status != beads.StatusDeferred && !slices.Contains(blocker.Labels, "superseded") && !slices.Contains(blocker.Labels, "retired") {
				held = true
			}
		}
		if held {
			continue
		}
		b, err := beads.FreshShow(ctx, bc, dir, id)
		if err != nil {
			return err
		}
		if b == nil {
			return fmt.Errorf("missing released step %s", id)
		}
		if b.Status == "closed" || b.Status == beads.StatusDeferred {
			continue
		}
		state, owner := beads.StepState(b.Status, b.Labels)
		if state == beads.StateNeedsUser || len(beads.WaitsOf(b.Labels)) > 0 || (owner != "" && !strings.HasPrefix(owner, "manager-")) {
			continue
		}
		// The nearest owner dispatches it; a step still naming an owner above it
		// (from before its nested Manager) moves to the nearest one.
		owners, err := wsmgr.OwnerChain(ctx, bc, dir, id)
		if err != nil {
			return err
		}
		// The owner: label holds the dashed spelling (labels forbid dots), a
		// nested Manager's role the dotted one: compare either way.
		manager := wsmgr.Role(owners[0])
		if owner != "" && !roleAmong(owner, []string{manager}) {
			if !slices.ContainsFunc(owners[1:], func(o string) bool { return roleAmong(owner, []string{wsmgr.Role(o)}) }) {
				return fmt.Errorf("released step %s names a different Manager %s", id, owner)
			}
			if err = writes.SetLabel(ctx, dir, id, beads.OwnerLabelPrefix+owner, false); err != nil {
				return err
			}
		}
		if err = writes.SetLabel(ctx, dir, id, beads.OwnerLabelPrefix+beads.OwnerLabelName(manager), true); err != nil {
			return err
		}
		wait := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerDispatchWait}
		if err = writes.SetLabel(ctx, dir, id, wait.Label(), true); err != nil {
			return err
		}
	}
	return writes.Flush(ctx)
}
