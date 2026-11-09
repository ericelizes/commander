package beads

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// IsWorkstream: labelled a workstream, at any depth.
func IsWorkstream(labels []string) bool {
	return slices.Contains(labels, WorkstreamLabel) || slices.Contains(labels, TrackLabel)
}

// IsNested: a workstream under another workstream. An old kind:track bead counts
// without a parent too, so one whose workstream is gone is not promoted to a row.
func IsNested(labels []string, parent string) bool {
	return slices.Contains(labels, TrackLabel) || (slices.Contains(labels, WorkstreamLabel) && parent != "")
}

// Placement is a bead about to sit under a parent.
type Placement struct {
	Workstream bool
	// Self is the bead being moved or reopened, not counted among the parent's children.
	Self string
	// TakingSteps: the new workstream takes the parent's open steps, so they do not conflict.
	TakingSteps bool
}

// OpenChildren are parent's children not finished, parked ones included: a split
// takes those along too. Operator task beads are agent-internal, not plan steps,
// and are left out.
func OpenChildren(ctx context.Context, c Client, dir, parent string) ([]Bead, error) {
	edges, err := c.Dependents(ctx, dir, parent)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range edges {
		if e.Type == "parent-child" {
			ids = append(ids, e.IssueID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	children, err := c.List(ctx, dir, ListOptions{IDs: ids, Status: "all"})
	if err != nil {
		return nil, err
	}
	var out []Bead
	for _, b := range children {
		if b.Status == "closed" || b.Status == "tombstone" || Superseded(b) || slices.Contains(b.Labels, "kind:task") {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// CheckPlacement enforces the shape of the tree: a workstream sits at the top or
// directly under a workstream, and a workstream's open children are all
// workstreams or all steps.
func CheckPlacement(ctx context.Context, c Client, dir, parent string, p Placement) error {
	if parent == "" {
		return nil
	}
	b, err := FreshShow(ctx, c, dir, parent)
	if errors.Is(err, ErrNotFound) || (err == nil && b == nil) {
		return nil // the write that follows reports a missing parent
	}
	if err != nil {
		return fmt.Errorf("could not read %s to check where this goes (%v); nothing written, retry in a minute", parent, err)
	}
	if p.Workstream && !IsWorkstream(b.Labels) {
		d, err := FreshDetails(ctx, c, dir, []string{parent})
		if err != nil {
			return fmt.Errorf("could not read %s to check where this goes (%v); nothing written, retry in a minute", parent, err)
		}
		if d[parent].Parent != "" {
			return fmt.Errorf("%s is a step: a workstream sits at the top or directly under another workstream", parent)
		}
	}
	children, err := OpenChildren(ctx, c, dir, parent)
	if err != nil {
		return fmt.Errorf("could not read the children of %s (%v); nothing written, retry in a minute", parent, err)
	}
	var steps, workstreams []string
	for _, k := range children {
		switch {
		// Parked is not open: it is checked when it comes back.
		case k.ID == p.Self || k.Status == StatusDeferred:
		case IsWorkstream(k.Labels):
			workstreams = append(workstreams, k.ID)
		default:
			steps = append(steps, k.ID)
		}
	}
	if p.Workstream && len(steps) > 0 && !p.TakingSteps {
		return fmt.Errorf("%s has open steps (%s), and a workstream's open children are all workstreams or all steps: create the workstream with --take-open-steps to move them into it", parent, strings.Join(steps, ", "))
	}
	if !p.Workstream && len(workstreams) > 0 {
		return fmt.Errorf("%s has open workstreams under it (%s), and a workstream's open children are all workstreams or all steps: put this under one of them, or make it a workstream with --workstream", parent, strings.Join(workstreams, ", "))
	}
	return nil
}
