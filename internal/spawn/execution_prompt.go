package spawn

import (
	"fmt"
	promptassets "github.com/example-user/commander/prompts"
)

func (r SpawnRequest) promptRole(legacy string) string {
	if r.ExecutionClaimToken != "" {
		return promptassets.RoleExecutor
	}
	return legacy
}

func executionBootstrapPrompt(r SpawnRequest, workspace string) string {
	return fmt.Sprintf("You are the execution agent for claimed bead %s in project %q (project ID %d). %s "+
		"Read live state using `%s show %s` and the same project-root prefix for read-only parent/dependency lookups. "+
		"The bead and recorded constraints are your brief. Current planning owner at launch: %s; durable questions route to the current owner automatically. "+
		"Use the durable question interface for decisions beyond your scope, record your result on the assigned bead, then stop. "+
		"Your immutable run/session are supplied by the launcher; never edit the shared tree or close the bead.\n\n%s",
		r.TaskBeadID, r.ProjectName, r.ProjectID, workspace, BrCommand(r.ProjectPath), r.TaskBeadID, r.managerRole, r.TaskText) + prodGatePrompt(r)
}
