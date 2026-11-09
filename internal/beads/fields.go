package beads

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Fields are the plain fields of a bead an update may set; nil = leave alone.
type Fields struct {
	Title       *string
	Description *string
	Notes       *string
	Acceptance  *string
	Priority    *int
}

// Empty reports whether f sets nothing.
func (f Fields) Empty() bool {
	return f.Title == nil && f.Description == nil && f.Notes == nil && f.Acceptance == nil && f.Priority == nil
}

func (f Fields) args() ([]string, error) {
	var out []string
	if f.Title != nil {
		if strings.TrimSpace(*f.Title) == "" {
			return nil, fmt.Errorf("beads: the title cannot be empty")
		}
		out = append(out, "--title", *f.Title)
	}
	if f.Description != nil {
		out = append(out, "--description", *f.Description)
	}
	if f.Notes != nil {
		out = append(out, "--notes", *f.Notes)
	}
	if f.Acceptance != nil {
		out = append(out, "--acceptance", *f.Acceptance)
	}
	if f.Priority != nil {
		if *f.Priority < 0 || *f.Priority > 4 {
			return nil, fmt.Errorf("beads: priority must be 0-4")
		}
		out = append(out, "--priority", strconv.Itoa(*f.Priority))
	}
	return out, nil
}

// UpdateFields invokes `br update <issueID> <field flags>` from projectDir.
func (c *cmdClient) UpdateFields(ctx context.Context, projectDir, issueID string, f Fields) error {
	if issueID == "" || f.Empty() {
		return fmt.Errorf("beads: UpdateFields requires issueID and at least one field")
	}
	args, err := f.args()
	if err != nil {
		return err
	}
	_, stderr, err := c.runUpdate(ctx, projectDir, issueID, args...)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// RemoveDep invokes `br dep remove <issueID> <dependsOn>` from projectDir.
func (c *cmdClient) RemoveDep(ctx context.Context, projectDir, issueID, dependsOn string) error {
	if issueID == "" || dependsOn == "" {
		return fmt.Errorf("beads: RemoveDep requires both ids")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), "dep", "remove", issueID, dependsOn)
	if err != nil {
		return fmt.Errorf("br dep remove: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// CloseWithReason invokes `br close <issueID> --reason <reason>` from projectDir.
func (c *cmdClient) CloseWithReason(ctx context.Context, projectDir, issueID, reason string) error {
	if issueID == "" {
		return fmt.Errorf("beads: CloseWithReason requires issueID")
	}
	args := []string{"close", issueID, "--reason", reason}
	if isBD() {
		args = append(args, "--force")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return fmt.Errorf("br close: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
