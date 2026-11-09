package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SetState invokes `br set-state <issueID> <dimension>=<value> [--reason <reason>]`
// from projectDir.
//
// Note the contract asymmetry: invocation uses `=`, but br stores the
// resulting label as `<dimension>:<value>`. See share/contracts.md.
func (c *cmdClient) SetState(ctx context.Context, projectDir, issueID, dimension, value, reason string) error {
	if issueID == "" {
		return fmt.Errorf("beads: SetState requires issueID")
	}
	if dimension == "" || value == "" {
		return fmt.Errorf("beads: SetState requires dimension and value")
	}

	args := []string{"set-state", issueID, dimension + "=" + value}
	if reason != "" {
		args = append(args, "--reason", reason)
	}

	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		// br >= 0.7 dropped `set-state`: do what it did with labels (one
		// "<dimension>:<value>" label, replacing the dimension's previous one).
		if strings.Contains(string(stderr), "unrecognized subcommand") {
			return c.setStateByLabels(ctx, projectDir, issueID, dimension, value)
		}
		return fmt.Errorf("br set-state: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (c *cmdClient) setStateByLabels(ctx context.Context, projectDir, issueID, dimension, value string) error {
	out, stderr, err := c.run(ctx, projectDir, beadsBinary(), "label", "list", issueID, "--json")
	if err != nil {
		return fmt.Errorf("br label list: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	var labels []string
	if err := json.Unmarshal(out, &labels); err != nil {
		return fmt.Errorf("br label list: parse %q: %w", strings.TrimSpace(string(out)), err)
	}
	want := dimension + ":" + value
	for _, l := range labels {
		if strings.HasPrefix(l, dimension+":") && l != want {
			if err := c.SetLabel(ctx, projectDir, issueID, l, false); err != nil {
				return err
			}
		}
	}
	return c.SetLabel(ctx, projectDir, issueID, want, true)
}
