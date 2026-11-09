package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AddComment invokes `br comments add <issueID> <text>` from projectDir.
func (c *cmdClient) AddComment(ctx context.Context, projectDir, issueID, text string) error {
	if issueID == "" || strings.TrimSpace(text) == "" {
		return fmt.Errorf("beads: AddComment requires issueID and text")
	}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), "comments", "add", issueID, text)
	if err != nil {
		return fmt.Errorf("br comments add: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// Comments invokes `br comments list <issueID> --json` from projectDir.
func (c *cmdClient) Comments(ctx context.Context, projectDir, issueID string) ([]Comment, error) {
	args := []string{"comments", "list", issueID, "--json"}
	if isBD() {
		args = []string{"comments", issueID, "--json"} // bd has no `comments list`
	}
	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return nil, fmt.Errorf("br comments list: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	out := strings.TrimSpace(string(stdout))
	if out == "" || out == "null" {
		return nil, nil
	}
	var cs []Comment
	if err := json.Unmarshal([]byte(out), &cs); err != nil {
		return nil, fmt.Errorf("br comments list: decode: %w", err)
	}
	return cs, nil
}
