package main

import (
	"context"
	"os"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

const activePlanningContext = "[current orchestration]\nYou are the planning Commander for your recorded jurisdiction. Manage the shared bead tree directly; do not delegate planning to workstream Managers. Use cmdr plan apply --file PATH with current read revisions. Ordinary leaf updates support both --description and --acceptance; missing briefs/criteria are your planning work, not a missing scheduler interface or a new human decision. Reread cmdr execution status after the edit, then mark the new revision ready. Preserve all holds, scope, approvals and immutable workflow/check definitions. Execution and progress require actual run evidence."

// Long-lived conversations may predate the active orchestration prompt. Supply
// current protocol on their next prompt, without restarting an urgent chat or
// creating another model turn. This note grants no authority.
func planningContext(role string) string {
	if actual := os.Getenv("COMMANDER_ROLE"); actual != "" && actual != role {
		return ""
	}
	if beads.HasExecutionAttribution(os.Environ()) {
		return ""
	}
	path, err := stateDBPath()
	if err != nil {
		return ""
	}
	dsn, err := store.PostgresDSN(path)
	if err != nil || dsn == "" {
		return ""
	}
	st, err := store.OpenPostgres(dsn)
	if err != nil {
		return ""
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	active, err := st.ActiveOrchestration(ctx)
	if err != nil || !active {
		return ""
	}
	owns, _, err := st.PlanningIdentityState(ctx, role)
	if err != nil || !owns {
		return ""
	}
	execution, err := st.NativeExecution(ctx, role)
	if err != nil || execution != nil {
		return ""
	}
	return activePlanningContext
}
