package main

import (
	"context"
	"fmt"

	"github.com/example-user/commander/internal/beads"
)

// closeConfirmSteps: the user closing a workstream whose only open child is its
// Confirm done step confirms that step too.
func closeConfirmSteps(ctx context.Context, bc beads.Client, dir, id string) error {
	edges, err := bc.Dependents(ctx, dir, id)
	if err != nil {
		return nil // requireParentFinished reports what it cannot read
	}
	var gates []string
	for _, e := range edges {
		if e.Type != "parent-child" || e.DependsOnID != id {
			continue
		}
		b, err := beads.FreshShow(ctx, freshReader(bc), dir, e.IssueID)
		if err != nil || b == nil {
			return nil
		}
		if b.Status == "closed" || b.Status == "tombstone" {
			continue
		}
		if !beads.IsClosingGate(b.Labels) {
			return nil // other work is open: requireParentFinished refuses the close
		}
		gates = append(gates, b.ID)
	}
	for _, g := range gates {
		if err := bc.Close(ctx, dir, g); err != nil {
			return fmt.Errorf("could not confirm %s: %w", g, err)
		}
	}
	return nil
}
