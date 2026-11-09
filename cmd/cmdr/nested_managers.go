package main

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/wsmgr"
)

// startNestedManager opens the Manager of a workstream just made nested (created
// or converted with --workstream), as a new top-level one gets its own, and tells
// the owner above it who owns that subtree now, unless the caller is that owner.
// Nothing happens while the project's nested Managers are off. It returns the
// new Manager's role, "" when none was started.
func startNestedManager(ctx context.Context, bc beads.Client, projectName, dir, bead string, warn func(string)) string {
	if !beads.NestedManagersOn(dir) {
		return ""
	}
	owners, err := wsmgr.OwnerChain(ctx, bc, dir, bead)
	if err != nil {
		warn(fmt.Sprintf("its nested Manager was not started (could not read who owns it: %v); start it with cmdr internal-open-workstream --project %s --bead %s", err, projectName, bead))
		return ""
	}
	if owners[0] != bead {
		return ""
	}
	role := wsmgr.Role(bead)
	opener, cleanup, err := newWorkstreamOpener()
	if err != nil {
		warn(fmt.Sprintf("its Manager could not start: %v", err))
		return ""
	}
	defer cleanup()
	if _, err := opener.Open(ctx, projectName, bead, wsmgr.OpenOptions{}); err != nil {
		warn(fmt.Sprintf("its Manager could not start: %v", err))
		return ""
	}
	if len(owners) < 2 {
		return role
	}
	parent := wsmgr.Role(owners[1])
	if os.Getenv("COMMANDER_ROLE") == parent {
		return role
	}
	st, client, key, done, err := mailDeps()
	if err != nil {
		warn(fmt.Sprintf("%s was not told: %v", parent, err))
		return role
	}
	defer done()
	body := fmt.Sprintf("%s is now a nested workstream under yours with its own Manager, %s. Its beads and Operators are that Manager's: dispatch to it by mail and read its row with cmdr board --role %s.", bead, role, parent)
	if err := sendRoleMail(ctx, st, client, key, "commander", []string{parent}, "Nested workstream "+bead+" has its own Manager", body, agentmail.SendOptions{Importance: "high"}); err != nil {
		warn(fmt.Sprintf("%s was not told: %v", parent, err))
	}
	return role
}

// ownerRoles are the Manager roles allowed to act on id: its nearest owner and
// every owner above it (route to the nearest, authorize the nearest or any
// ancestor). With the project's nested Managers off it is the root's alone.
// Fresh reads; a failed read is an error.
func ownerRoles(ctx context.Context, bc beads.Client, dir, id string) ([]string, error) {
	owners, err := wsmgr.OwnerChain(ctx, bc, dir, id)
	if err != nil {
		return nil, err
	}
	roles := make([]string, len(owners))
	for i, o := range owners {
		roles[i] = wsmgr.Role(o)
	}
	return roles, nil
}

// roleAmong: role is one of roles, in any spelling of an agent role.
func roleAmong(role string, roles []string) bool {
	return slices.ContainsFunc(roles, func(r string) bool { return spawn.CanonicalRole(r) == spawn.CanonicalRole(role) })
}
