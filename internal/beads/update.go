package beads

import (
	"context"
	"fmt"
	"strings"
)

// Rename invokes `br update <issueID> --title <title>` from projectDir.
func (c *cmdClient) Rename(ctx context.Context, projectDir, issueID, title string) error {
	if issueID == "" || title == "" {
		return fmt.Errorf("beads: Rename requires issueID and title")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), "update", issueID, "--title", title)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// Close invokes `br close <issueID>` from projectDir.
func (c *cmdClient) Close(ctx context.Context, projectDir, issueID string) error {
	if issueID == "" {
		return fmt.Errorf("beads: Close requires issueID")
	}
	args := []string{"close", issueID}
	if isBD() {
		args = append(args, "--force") // bd refuses to close an issue still blocked; br closes it
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return fmt.Errorf("br close: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// Delete invokes `br delete <issueID> --reason <reason> --force` from projectDir.
func (c *cmdClient) Delete(ctx context.Context, projectDir, issueID, reason string) error {
	if issueID == "" {
		return fmt.Errorf("beads: Delete requires issueID")
	}
	args := []string{"delete", issueID, "--reason", reason, "--force"}
	if isBD() {
		args = []string{"delete", issueID, "--force"} // bd delete takes no reason and leaves no tombstone
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return fmt.Errorf("br delete: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// SetStatus invokes `br update <issueID> --status <status>` from projectDir.
// Terminal states go through Close instead.
func (c *cmdClient) SetStatus(ctx context.Context, projectDir, issueID, status string) error {
	if issueID == "" || status == "" {
		return fmt.Errorf("beads: SetStatus requires issueID and status")
	}
	_, stderr, err := c.runUpdate(ctx, projectDir, issueID, "--status", status)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// SetParent reparents a bead via `br update --parent` ("" removes the parent).
func (c *cmdClient) SetParent(ctx context.Context, projectDir, issueID, parent string) error {
	if issueID == "" {
		return fmt.Errorf("beads: SetParent requires issueID")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), "update", issueID, "--parent", parent)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// SetLabel adds (on) or removes (!on) a label via `br update`.
func (c *cmdClient) SetLabel(ctx context.Context, projectDir, issueID, label string, on bool) error {
	if issueID == "" || label == "" {
		return fmt.Errorf("beads: SetLabel requires issueID and label")
	}
	flag := "--add-label"
	if !on {
		flag = "--remove-label"
	}
	_, stderr, err := c.runUpdate(ctx, projectDir, issueID, flag, label)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// ParentOf returns the parent bead ID encoded in a child's dotted ID
// ("proj-ab1.2" -> "proj-ab1"), or "" for a top-level bead. `br list --json`
// does not report the parent, so the ID is the only source in list output.
func ParentOf(id string) string {
	i := strings.LastIndex(id, ".")
	if i < 0 {
		return ""
	}
	return id[:i]
}

// runUpdate retries only idempotent field assignments, never create or comments.
func (c *cmdClient) runUpdate(ctx context.Context, dir, id string, fields ...string) ([]byte, []byte, error) {
	args := append([]string{"update", id}, fields...)
	out, stderr, err := c.run(ctx, dir, beadsBinary(), args...)
	if err != nil && ctx.Err() == nil {
		message := strings.ToLower(err.Error() + " " + string(stderr))
		if strings.Contains(message, "signal: killed") || strings.Contains(message, "busy") || strings.Contains(message, "locked") {
			return c.run(ctx, dir, beadsBinary(), args...)
		}
	}
	return out, stderr, err
}
