package beads

import (
	"context"
	"errors"
	"strings"
	"time"

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
		live, e := qualifiedCustodyLive(ctx, q, before)
		if e != nil {
			return nil, qualifiedFailure("custody", "runtime", e)
		}
		old, next := before.holdingGraph(live), after.holdingGraph(live)
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

func (v *qualifiedView) holdingGraph(live func(string) (string, bool)) *holding.Graph {
	g := holding.NewGraph(live)
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

// Runtime facts affect custody without inventing a working turn. Missing or
// expired observations remain unknown; a Manager name alone never holds work.
func qualifiedCustodyLive(ctx context.Context, q store.Querier, v *qualifiedView) (func(string) (string, bool), error) {
	type fact struct {
		state string
		known bool
	}
	facts := map[string]fact{}
	roles := map[string]bool{}
	for _, b := range v.nodes {
		h := holding.Bead{Status: b.Status, Labels: b.Labels}
		DeriveHolding(&h)
		if h.Owner != "" {
			roles[h.Owner] = true
		}
		for _, r := range h.OperatorWaits {
			roles[r] = true
		}
	}
	args := []any{}
	for r := range roles {
		args = append(args, r)
	}
	live := func(r string) (string, bool) {
		if holding.IsManager(r) {
			return "idle", true
		}
		f := facts[r]
		return f.state, f.known
	}
	if len(args) == 0 {
		return live, nil
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	rows, e := q.QueryContext(ctx, `SELECT DISTINCT ON (r.agent_name) r.agent_name,r.status,COALESCE(o.alive,FALSE),COALESCE(o.checked_at,0),COALESCE(b.active_run_id=r.id,FALSE) FROM agent_runs r LEFT JOIN run_observations o ON o.run_id=r.id LEFT JOIN bead_tasks b ON b.project_id=r.project_id AND b.id=r.task_bead_id WHERE r.agent_name IN (`+in+`) ORDER BY r.agent_name,r.spawn_time DESC,r.id DESC`, args...)
	if e != nil {
		return nil, e
	}
	floor := time.Now().Add(-45 * time.Second).Unix()
	for rows.Next() {
		var r, status string
		var alive, current bool
		var at int64
		if e = rows.Scan(&r, &status, &alive, &at, &current); e != nil {
			rows.Close()
			return nil, e
		}
		if status != "running" || !current {
			facts[r] = fact{"", true}
		} else if at >= floor {
			state := ""
			if alive {
				state = "present"
			}
			facts[r] = fact{state, true}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = q.QueryContext(ctx, `SELECT role,alive,checked_at FROM native_presence WHERE role IN (`+in+`)`, args...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var r string
		var alive bool
		var at int64
		if e = rows.Scan(&r, &alive, &at); e != nil {
			rows.Close()
			return nil, e
		}
		if !facts[r].known && at >= floor {
			state := ""
			if alive {
				state = "present"
			}
			facts[r] = fact{state, true}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return live, nil
}
