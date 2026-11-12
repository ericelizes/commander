package main

import (
	"context"
	"fmt"
	"os"

	"github.com/example-user/commander/internal/beads"
)

// PostgreSQL's fresh bead projection resolves inherited planning jurisdiction.
// Execution attribution is checked separately and never grants planning rights.
// Legacy backends retain their Manager ownership rules.
func authorizeWorkflowOwner(ctx context.Context, bc beads.Client, dir string, b beads.Bead, root, action string, legacyCommander bool) error {
	if callerIsOperator("") || workflowExecutionCaller() || (b.PlanningCommander != "" && beads.HasExecutionAttribution(os.Environ())) {
		return fmt.Errorf("%s belongs to the planning owner; Operators cannot perform it", action)
	}
	role := os.Getenv("COMMANDER_ROLE")
	if role == "" {
		return nil
	}
	if beads.PostgresConfigured() || b.PlanningCommander != "" {
		if b.PlanningCommander == "" || role != b.PlanningCommander {
			return fmt.Errorf("%s belongs to planning Commander %s", action, b.PlanningCommander)
		}
		return nil
	}
	if legacyCommander && role == "commander" {
		return nil
	}
	owners, err := workflowOwnerRoles(ctx, bc, dir, b.ID, root)
	if err != nil {
		return err
	}
	if !roleAmong(role, owners) {
		return fmt.Errorf("%s belongs to %s", action, owners[0])
	}
	return nil
}

func workflowExecutionCaller() bool {
	return os.Getenv("COMMANDER_RUN_ID") != "" || os.Getenv("COMMANDER_RUN_SESSION") != "" || (beads.PostgresConfigured() && beads.HasExecutionAttribution(os.Environ()))
}
