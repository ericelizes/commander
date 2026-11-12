package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/wsmgr"
)

// Active orchestration has logical planning owners. Old Manager aliases in an
// execution's real ancestry route there without copying or starting retired
// processes. Other explicitly addressed conversations remain as addressed.
func routeActiveExecutionMail(ctx context.Context, st amAgentStore, from string, recipients []string) ([]string, bool, error) {
	control, ok := st.(interface {
		ActiveOrchestration(context.Context) (bool, error)
	})
	if !ok {
		return recipients, false, nil
	}
	active, err := control.ActiveOrchestration(ctx)
	if err != nil || !active {
		return recipients, false, err
	}
	if !strings.HasPrefix(from, "operator-") || !slices.ContainsFunc(recipients, func(r string) bool { return r == "commander" || strings.HasPrefix(r, "manager-") }) {
		return recipients, true, nil
	}
	run, dir := operatorRunAndDir(ctx, st, from)
	if run == nil || dir == "" {
		return nil, true, fmt.Errorf("cannot resolve execution mail's current planning owner")
	}
	bc := newBeadsClientFn()
	aliases := map[string]bool{}
	seen := map[string]bool{}
	owner := ""
	id := run.TaskBeadID
	for id != "" {
		if seen[id] || len(seen) >= 128 {
			return nil, true, fmt.Errorf("execution mail ancestry is cyclic or too deep")
		}
		seen[id] = true
		b, e := beads.FreshShow(ctx, bc, dir, id)
		if e != nil {
			return nil, true, fmt.Errorf("read execution mail ownership: %w", e)
		}
		if b == nil {
			return nil, true, fmt.Errorf("execution mail ancestry is unavailable")
		}
		if owner == "" {
			owner = b.PlanningCommander
		}
		aliases[spawn.CanonicalRole(wsmgr.Role(id))] = true
		id = b.ParentID
		if id == "" {
			id = b.Parent
		}
	}
	if owner == "" {
		return nil, true, fmt.Errorf("execution has no current planning owner")
	}
	out := []string{}
	for _, r := range recipients {
		if r == "commander" || aliases[spawn.CanonicalRole(r)] {
			r = owner
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, true, nil
}
