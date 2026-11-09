package agentmail

import (
	"context"
	"fmt"
	"strings"
)

// Register invokes:
//
//	am agents register --project <p> --program <prog> --model <m> --name <n> --json
//
// Idempotent per the am contract: re-registering an existing name updates
// rather than duplicating.
//
// CRITICAL: program is "opencode-ai" for commander, NOT "claude-code".
// register_test.go has a regression guard for this discrepancy.
func (c *cmdClient) Register(ctx context.Context, project, program, model, name string) error {
	if project == "" {
		return fmt.Errorf("agentmail: Register requires project")
	}
	if program == "" {
		return fmt.Errorf("agentmail: Register requires program")
	}
	if model == "" {
		return fmt.Errorf("agentmail: Register requires model")
	}
	if name == "" {
		return fmt.Errorf("agentmail: Register requires name")
	}

	args := []string{
		"agents", "register",
		"--project", project,
		"--program", program,
		"--model", model,
		"--name", name,
		"--json",
	}

	_, stderr, err := c.exec(ctx, "am", args...)
	if err != nil {
		return fmt.Errorf("am agents register: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
