package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/rolemail"
	"github.com/example-user/commander/internal/spawn"
)

// existingAgentCaller refuses a caller this route is not for: it speaks outside
// the fleet under the sender's AM name, so only Managers, Commander and the user
// use it, and an agent only as itself. A policy guard, not a boundary: agents
// share the OS user and can change these variables.
func existingAgentCaller(from string) error {
	if callerIsOperator(from) {
		return fmt.Errorf("--to-existing-agent is refused for an Operator: mail your Manager")
	}
	role := os.Getenv("COMMANDER_ROLE")
	if role != "" && spawn.CanonicalRole(role) != spawn.CanonicalRole(from) {
		return fmt.Errorf("--to-existing-agent: %s cannot send as %s", role, from)
	}
	if from != "commander" && from != "user" && !strings.HasPrefix(from, "manager-") {
		return fmt.Errorf("--to-existing-agent is for Managers, Commander and the user, not %s", from)
	}
	return nil
}

// sendExistingAgentMail deliberately does not use role recipients, registration,
// wake signals, rerouting or process interruption. Both identities must exist.
func sendExistingAgentMail(ctx context.Context, st amAgentStore, m amMailer, key, fromRole, name, subject, body string, opts agentmail.SendOptions) (agentmail.ExistingAgent, error) {
	var zero agentmail.ExistingAgent
	lister, ok := st.(rolemail.Lister)
	if !ok {
		return zero, fmt.Errorf("existing-agent route requires local registration collision checks")
	}
	rows, err := lister.ListAmAgents(ctx, key)
	if err != nil {
		return zero, fmt.Errorf("check existing-agent collisions: %w", err)
	}
	for _, row := range rows {
		if strings.EqualFold(row.Role, name) || strings.EqualFold(row.AmName, name) {
			return zero, fmt.Errorf("existing agent %q collides with registered role %q; use --to with that role", name, row.Role)
		}
	}
	lookup, ok := m.(interface {
		LookupExistingAgent(context.Context, string, string) (agentmail.ExistingAgent, error)
	})
	if !ok {
		return zero, agentmail.ErrWhoisUnsupported
	}
	from, err := rolemail.Resolve(ctx, st, m, key, fromRole, false)
	if err != nil {
		return zero, fmt.Errorf("resolve existing sender: %w", err)
	}
	sender, err := lookup.LookupExistingAgent(ctx, key, from)
	if err != nil {
		return zero, fmt.Errorf("verify sender: %w", err)
	}
	recipient, err := lookup.LookupExistingAgent(ctx, key, name)
	if err != nil {
		return zero, fmt.Errorf("verify recipient: %w", err)
	}
	if sender.ProjectID != recipient.ProjectID || sender.ID == recipient.ID {
		return zero, fmt.Errorf("existing recipient has a conflicting server identity or project")
	}
	opts.NoAutoContact = true // this route never approves a contact link
	if err := m.SendMessage(ctx, key, sender.Name, []string{recipient.Name}, subject, body, opts); err != nil {
		return zero, err
	}
	return recipient, nil
}
