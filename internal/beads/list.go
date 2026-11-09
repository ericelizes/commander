package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// List invokes `br list --json` from projectDir with the given filters
// and parses the JSON output into []Bead. Returns a non-nil empty slice
// when br reports no matches.
func (c *cmdClient) List(ctx context.Context, projectDir string, opts ListOptions) ([]Bead, error) {
	args := []string{"list", "--json"}

	for _, l := range opts.Labels {
		args = append(args, "--label", l)
	}
	for _, l := range opts.LabelAny {
		args = append(args, "--label-any", l)
	}
	if opts.Status != "" {
		args = append(args, "--status", opts.Status)
	}
	if opts.Type != "" {
		args = append(args, "--type", opts.Type)
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	if len(opts.IDs) > 0 {
		if isBD() || doltProject(projectDir) {
			args = append(args, "--id", strings.Join(opts.IDs, ",")) // a single flag: bd keeps only the last --id
		} else { // br reads a comma list as one id
			for _, id := range opts.IDs {
				args = append(args, "--id", id)
			}
		}
	}

	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return nil, fmt.Errorf("br list: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	out := strings.TrimSpace(string(stdout))
	if out == "" || out == "null" || out == "[]" {
		return []Bead{}, nil
	}

	var beads []Bead
	if err := json.Unmarshal([]byte(out), &beads); err == nil {
		if beads == nil {
			return []Bead{}, nil
		}
		return beads, nil
	}

	var envelope struct {
		Issues []Bead `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return nil, fmt.Errorf("br list: parse json: %w", err)
	}
	if envelope.Issues == nil {
		return []Bead{}, nil
	}
	return envelope.Issues, nil
}

// doltProject is a project under a .beads/dolt: bd answers there whatever the
// binary is called (scripts/br-bd routes on this, a wrapper has another name), and
// br cannot read it. Checks the parents too, as a worktree's commands reach its checkout.
func doltProject(dir string) bool {
	for dir != "" {
		if st, err := os.Stat(filepath.Join(dir, ".beads", "dolt")); err == nil && st.IsDir() {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	return false
}
