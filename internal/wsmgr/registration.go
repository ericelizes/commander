package wsmgr

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/example-user/commander/internal/beads"
)

// ErrInvalidRoute marks a role that cannot own a native Manager.
var ErrInvalidRoute = errors.New("invalid Manager route")

// StrictRoot resolves actual edges for actions. Display's dotted-id fallback is
// deliberately unavailable here: a failed read must never create a Manager.
func StrictRoot(ctx context.Context, c beads.Client, dir, id string) (string, error) {
	if c == nil || dir == "" || id == "" {
		return "", fmt.Errorf("workstream ancestry requires client, project path and bead")
	}
	seen := map[string]bool{}
	for id != "" {
		if seen[id] {
			return "", fmt.Errorf("cycle in ancestry at %s", id)
		}
		seen[id] = true
		ds, err := beads.FreshDetails(ctx, c, dir, []string{id})
		if err != nil {
			return "", fmt.Errorf("ancestry of %s: %w", id, err)
		}
		d, ok := ds[id]
		if !ok {
			return "", fmt.Errorf("missing ancestry for %s", id)
		}
		if d.Parent == "" {
			return id, nil
		}
		id = d.Parent
	}
	return "", fmt.Errorf("missing root")
}

func (m *Manager) validateTarget(ctx context.Context, t Target) error {
	_, err := m.validateOwner(ctx, t)
	return err
}

// validateOwner is validateTarget that also answers the owner above t ("" for
// a top-level workstream), from the same reads.
func (m *Manager) validateOwner(ctx context.Context, t Target) (parent string, err error) {
	owners, err := OwnerChain(ctx, m.Beads, t.Project.Path, t.BeadID)
	if err != nil {
		return "", err
	}
	if owner := owners[0]; owner != t.BeadID {
		return "", fmt.Errorf("%w: %s is nested under %s; its owning Manager is %s", ErrInvalidRoute, t.BeadID, owner, Role(owner))
	}
	if len(owners) > 1 {
		parent = owners[1]
	}
	b, err := beads.FreshShow(ctx, m.Beads, t.Project.Path, t.BeadID)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fmt.Errorf("%w: %s is not an open workstream", ErrInvalidRoute, t.BeadID)
	}
	// A nested owner carries kind:workstream (StrictOwner checked it); an old
	// kind:track bead without it never has a Manager of its own.
	track := slices.Contains(b.Labels, beads.TrackLabel) && !slices.Contains(b.Labels, beads.WorkstreamLabel)
	if b.ID != t.BeadID || b.Status == "closed" || !slices.Contains(b.Labels, "kind:objective") || slices.Contains(b.Labels, "kind:task") || slices.Contains(b.Labels, "kind:gate") || slices.Contains(b.Labels, beads.GateLabel) || track {
		return "", fmt.Errorf("%w: %s is not an open workstream", ErrInvalidRoute, t.BeadID)
	}
	return parent, nil
}

// strictChain is id and its ancestors by fresh parent edges, nearest first.
func strictChain(ctx context.Context, c beads.Client, dir, id string) ([]string, error) {
	if c == nil || dir == "" || id == "" {
		return nil, fmt.Errorf("workstream ancestry requires client, project path and bead")
	}
	var chain []string
	seen := map[string]bool{}
	for id != "" {
		if seen[id] {
			return nil, fmt.Errorf("cycle in ancestry at %s", id)
		}
		seen[id] = true
		chain = append(chain, id)
		ds, err := beads.FreshDetails(ctx, c, dir, []string{id})
		if err != nil {
			return nil, fmt.Errorf("ancestry of %s: %w", id, err)
		}
		d, ok := ds[id]
		if !ok {
			return nil, fmt.Errorf("missing ancestry for %s", id)
		}
		id = d.Parent
	}
	return chain, nil
}

// strictOwners reads the chain and the beads over it fresh. With the project's
// nested switch off only the root counts (no label read).
func strictOwners(ctx context.Context, c beads.Client, dir, id string) (chain []string, got map[string]beads.Bead, err error) {
	chain, err = strictChain(ctx, c, dir, id)
	if err != nil {
		return nil, nil, err
	}
	got = map[string]beads.Bead{}
	if !beads.NestedManagersOnCtx(ctx, dir) || len(chain) == 1 {
		return chain, got, nil
	}
	above := chain[:len(chain)-1]
	list, err := c.List(ctx, dir, beads.ListOptions{IDs: above, Status: "all"})
	if err != nil {
		return nil, nil, fmt.Errorf("workstream labels of %s: %w", id, err)
	}
	for _, b := range list {
		got[b.ID] = b
	}
	for _, a := range above {
		if _, ok := got[a]; !ok {
			// A short answer: never guess an owner from it.
			return nil, nil, fmt.Errorf("workstream labels of %s: %s not listed", id, a)
		}
	}
	return chain, got, nil
}

// StrictOwner is beads.Owner for actions: the nearest open kind:workstream bead
// over id (id first), else its root, by fresh reads. Any failed read is an
// error, so a failed read never creates or wakes a Manager.
func StrictOwner(ctx context.Context, c beads.Client, dir, id string) (string, error) {
	chain, got, err := strictOwners(ctx, c, dir, id)
	if err != nil {
		return "", err
	}
	return beads.NearestOwner(chain, got), nil
}

// OwnerChain is StrictOwner and every owner above it, nearest first, ending at
// the root: the Managers allowed to act on id (route to the nearest, authorize
// the nearest or any ancestor).
func OwnerChain(ctx context.Context, c beads.Client, dir, id string) ([]string, error) {
	chain, got, err := strictOwners(ctx, c, dir, id)
	if err != nil {
		return nil, err
	}
	return beads.OwnerChain(chain, got), nil
}
