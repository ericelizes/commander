package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

// Check actual child edges before any custody/result writes. Deferred work is
// unfinished too. Closed children remain history; do not unfold their subtrees.
func requireParentFinished(ctx context.Context, bc beads.Client, dir, id string) error {
	bc = freshReader(bc)
	b, err := beads.FreshShow(ctx, bc, dir, id)
	if err != nil {
		return fmt.Errorf("cannot check completion of %s: %w", id, err)
	}
	if b == nil {
		return fmt.Errorf("cannot check missing bead %s", id)
	}
	for _, w := range beads.WaitsOf(b.Labels) {
		if w.Kind == beads.WaitReview {
			return fmt.Errorf("cannot close %s: Waiting for review (%s); record the required review first", id, w.Ref)
		}
	}
	edges, err := bc.Dependents(ctx, dir, id)
	if err != nil {
		return fmt.Errorf("cannot read children of %s: %w", id, err)
	}
	var ids []string
	for _, e := range edges {
		if e.Type == "parent-child" && e.DependsOnID == id {
			ids = append(ids, e.IssueID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var live []string
	for _, child := range ids {
		b, err := beads.FreshShow(ctx, bc, dir, child)
		if err != nil || b == nil || b.ID != child {
			return fmt.Errorf("cannot verify child %s of %s: %v", child, id, err)
		}
		if b.Status != "closed" && b.Status != "tombstone" {
			live = append(live, child)
		}
	}
	if len(live) > 0 {
		sort.Strings(live)
		return fmt.Errorf("cannot close %s: unfinished children (%s); finish them first (deferred work is unfinished)", id, strings.Join(live, ", "))
	}
	return nil
}
