package beads

import (
	"context"
	"fmt"
	"strings"
)

// Link invokes `br link <fromID> <toID> --type <linkType>` from projectDir.
//
// **Positional args** — br accepts positional `from to`. This wrapper keeps
// that contract explicit.
// verified discrepancy from the original plan; see share/contracts.md.
//
// linkType defaults to "blocks" when empty.
func (c *cmdClient) Link(ctx context.Context, projectDir, fromID, toID, linkType string) error {
	if fromID == "" || toID == "" {
		return fmt.Errorf("beads: Link requires both fromID and toID")
	}
	if linkType == "" {
		linkType = "blocks"
	}

	args := []string{"link", fromID, toID, "--type", linkType}
	_, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return fmt.Errorf("br link: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}
