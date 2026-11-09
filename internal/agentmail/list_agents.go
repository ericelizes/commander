package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ListAgents invokes:
//
//	am agents list --project <p> --json
//
// Tolerates either a bare array or an envelope with an "agents" field.
func (c *cmdClient) ListAgents(ctx context.Context, project string) ([]Agent, error) {
	if project == "" {
		return nil, fmt.Errorf("agentmail: ListAgents requires project")
	}

	args := []string{
		"agents", "list",
		"--project", project,
		"--json",
	}

	stdout, stderr, err := c.exec(ctx, "am", args...)
	if err != nil {
		return nil, fmt.Errorf("am agents list: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var agents []Agent
		if err := json.Unmarshal([]byte(trimmed), &agents); err != nil {
			return nil, fmt.Errorf("am: parse agents array: %w", err)
		}
		return agents, nil
	}

	var envelope struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, fmt.Errorf("am: parse agents envelope: %w", err)
	}
	return envelope.Agents, nil
}
