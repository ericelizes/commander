package beads

import (
	"context"
	"fmt"
	"strings"
)

// Init runs `br init` in projectDir so the project has a beads workspace and can
// hold objectives/tasks. Without this, `br create` fails with "Beads not
// initialized" — so activation calls Init when adding a project.
//
// Idempotent: `br init` errors if the workspace already exists; we treat
// "already initialized" as success so re-activation is a no-op.
func (c *cmdClient) Init(ctx context.Context, projectDir string) error {
	if projectDir == "" {
		return fmt.Errorf("beads: Init requires projectDir")
	}
	args := []string{"init"}
	if isBD() {
		// Server mode for concurrent writers. Never let bd write AGENTS.md,
		// CLAUDE.md, hooks or .gitignore into the project, or commit to it
		// (--setup-exclude keeps its files out of git without a commit).
		args = append(args, "--server", "--non-interactive", "--skip-agents", "--skip-hooks", "--setup-exclude")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if strings.Contains(strings.ToLower(msg), "already initialized") {
			return nil
		}
		return fmt.Errorf("br init: %w: %s", err, msg)
	}
	return nil
}
