package spawn

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

// WorkstreamCap returns the Operator cap of the workstream root: its
// max-operators:<n> label when set (n > 0), else def.
func WorkstreamCap(labels []string, def int) int {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, beads.MaxOperatorsLabelPrefix); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return def
}

// CheckWorkstreamCap refuses one more Operator under beadID (a step or task)
// when its workstream already runs its cap of them. def is the default cap
// (0 = none); the workstream bead's max-operators:<n> label overrides it.
// Running Operators count by the workstream their task bead belongs to.
//
// With the project's nested Managers on, a max-operators label on the bead's
// owner or any owner above it (the root included) caps that owner's whole
// subtree, its own steps and its nested workstreams' alike. The default bounds
// the root's whole subtree too when the root has no label, so turning nesting
// on never multiplies a tree's bound, and the nearest nested owner with no
// label also gets the default over the Operators it owns itself.
func CheckWorkstreamCap(ctx context.Context, runs RunLister, c beads.Client, projectID int64, dir, beadID string, def int) error {
	chain := beads.Lineage(ctx, c, dir, nil, beadID)
	if len(chain) == 0 {
		return nil
	}
	root := chain[len(chain)-1]
	nested := beads.NestedManagersOn(dir)
	var got map[string]beads.Bead
	owners := []string{root}
	if nested {
		var err error
		if got, err = listChain(ctx, c, dir, chain); err != nil {
			return err
		}
		owners = beads.OwnerChain(chain, got)
	} else {
		b, err := c.Show(ctx, dir, root)
		if err != nil {
			return fmt.Errorf("read workstream %s: %w", root, err)
		}
		got = map[string]beads.Bead{}
		if b != nil {
			got[root] = *b
		}
	}
	type limit struct {
		owner   string
		n       int
		subtree bool // every run under owner, not only the ones it owns itself
	}
	var limits []limit
	for i, o := range owners {
		if n := WorkstreamCap(got[o].Labels, 0); n > 0 {
			limits = append(limits, limit{o, n, true})
		} else if o == root && def > 0 {
			limits = append(limits, limit{o, def, true})
		} else if i == 0 && def > 0 {
			limits = append(limits, limit{o, def, false})
		}
	}
	if len(limits) == 0 {
		return nil
	}
	running, err := runs.ListRunningRuns(ctx)
	if err != nil {
		return fmt.Errorf("list running operators: %w", err)
	}
	var tasks []string
	agents := map[string]string{}
	for _, r := range running {
		if r.ProjectID == projectID && r.TaskBeadID != "" {
			tasks = append(tasks, r.TaskBeadID)
			agents[r.TaskBeadID] = r.AgentName
		}
	}
	known := knownParents(ctx, c, dir, tasks)
	lineage := map[string][]string{}
	var all []string
	for _, task := range tasks {
		lineage[task] = beads.Lineage(ctx, c, dir, known, task)
		all = append(all, lineage[task]...)
	}
	if nested && len(all) > 0 {
		more, err := listChain(ctx, c, dir, all)
		if err != nil {
			return err
		}
		for id, b := range more {
			got[id] = b
		}
	}
	for _, l := range limits {
		var mine []string
		for _, task := range tasks {
			lin := lineage[task]
			switch {
			case len(lin) == 0:
			case l.subtree:
				if slices.Contains(lin, l.owner) {
					mine = append(mine, agents[task])
				}
			case beads.NearestOwner(lin, got) == l.owner:
				mine = append(mine, agents[task])
			}
		}
		if len(mine) >= l.n {
			return fmt.Errorf("workstream %s already has %d running operators (max %d): %s; wait for one to finish, "+
				"or raise its cap: cmdr internal-set-objective --project <p> --bead %s --max-operators <n>",
				l.owner, len(mine), l.n, strings.Join(mine, ", "), l.owner)
		}
	}
	return nil
}

// listChain reads the beads ids name (labels and status), one list process.
func listChain(ctx context.Context, c beads.Client, dir string, ids []string) (map[string]beads.Bead, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	list, err := c.List(ctx, dir, beads.ListOptions{IDs: ids, Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("read workstreams over %s: %w", ids[0], err)
	}
	out := make(map[string]beads.Bead, len(list))
	for _, b := range list {
		out[b.ID] = b
	}
	return out, nil
}

// knownParents reads the parents of ids and their dotted prefixes (the likely
// ancestors) in one batched call, so walking each id to its root costs no more.
func knownParents(ctx context.Context, c beads.Client, dir string, ids []string) beads.Parents {
	seen := map[string]bool{}
	var batch []string
	for _, id := range ids {
		for p := id; p != "" && !seen[p]; p = beads.ParentOf(p) {
			seen[p] = true
			batch = append(batch, p)
		}
	}
	known := beads.Parents{}
	if len(batch) == 0 {
		return known
	}
	details, err := beads.FreshDetails(ctx, c, dir, batch)
	if err != nil {
		return known
	}
	for id, d := range details {
		known[id] = d.Parent
	}
	return known
}
