package beads

import (
	"context"
	"errors"

	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/store"
)

// pgGuardedOps holds graph/identity serialization across the actual mutation and
// custody comparison. It also covers requests without Intent and operations that
// the HTTP simulation cannot model. Failure rolls back every operation and event.
func pgGuardedOps(ctx context.Context, q store.Querier, dir string, ops [][]string) ([][]byte, error) {
	if _, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031823)`); err != nil {
		return nil, err
	}
	pid, err := projectID(ctx, q, dir)
	if err != nil {
		return nil, err
	}
	before, err := loadQualifiedView(ctx, q, pid, false)
	if err != nil {
		return nil, err
	}
	outputs := [][]byte{}
	for _, op := range ops {
		out, e := pgWrite(ctx, q, pid, op)
		if e != nil {
			return nil, e
		}
		outputs = append(outputs, out)
	}
	var seeds []qualifiedNode
	if before != nil {
		for n := range before.nodes {
			if n.Project != pid {
				seeds = append(seeds, n)
			}
		}
	}
	after, err := loadQualifiedSeeded(ctx, q, pid, before != nil, seeds)
	if err != nil {
		return nil, err
	}
	if before != nil {
		old, next := before.holdingGraph(), after.holdingGraph()
		for id, n := range before.ids {
			if n.Project == pid {
				continue
			}
			was := old.Beads[id]
			now, ok := next.Beads[id]
			if !ok {
				return nil, qualifiedFailure("receipt", id, errors.New("foreign source disappeared during mutation"))
			}
			_, heldBefore, _ := holding.Held(was, old)
			_, heldAfter, why := holding.Held(now, next)
			if (heldBefore || was.Status == holding.StatusClosed) && !heldAfter && why != holding.WhyClosed {
				return nil, qualifiedFailure("receipt", id, errors.New("mutation requires foreign source custody; its owning Manager must establish a local holder first"))
			}
		}
	}
	return outputs, nil
}

func (v *qualifiedView) holdingGraph() *holding.Graph {
	g := holding.NewGraph(nil)
	for id, n := range v.ids {
		b := v.nodes[n]
		h := holding.Bead{ID: id, Parent: b.ParentID, Status: b.Status, Labels: b.Labels}
		DeriveHolding(&h)
		g.Beads[id] = h
		g.Titles[id] = b.Title
		if h.Parent != "" {
			g.Children[h.Parent] = append(g.Children[h.Parent], id)
		}
	}
	for _, e := range v.edges {
		if e.Kind == "blocks" {
			b := g.Beads[e.Source.ID]
			b.BlockedBy = append(b.BlockedBy, e.Target.ID)
			g.Beads[e.Source.ID] = b
			g.Dependents[e.Target.ID] = append(g.Dependents[e.Target.ID], e.Source.ID)
		}
	}
	return g
}
