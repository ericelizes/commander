package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/example-user/commander/internal/ownercompat"
)

// Create invokes `br create <title> [flags...]` from projectDir and
// returns the new bead ID.
//
// With Silent=true, br writes only the ID on stdout. Without it, br
// writes a `Created issue: <id> — <title>` line; we extract the ID
// from there.
func (c *cmdClient) Create(ctx context.Context, projectDir, title string, opts CreateOptions) (string, error) {
	if title == "" {
		return "", fmt.Errorf("beads: Create requires title")
	}

	args := []string{"create", title}
	if opts.Parent != "" {
		args = append(args, "--parent", opts.Parent)
	}
	if len(opts.Labels) > 0 {
		args = append(args, "--labels", strings.Join(opts.Labels, ","))
	}
	if opts.Priority > 0 {
		args = append(args, "--priority", strconv.Itoa(opts.Priority))
	}
	if opts.Type != "" {
		args = append(args, "--type", opts.Type)
	}
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	if opts.AcceptanceCriteria != "" {
		args = append(args, "--acceptance", opts.AcceptanceCriteria)
	}
	if opts.Silent {
		args = append(args, "--silent")
	}
	noInherit := opts.Parent != "" && c.supportsNoInherit(ctx, projectDir)
	if noInherit {
		args = append(args, noInheritFlag)
	}

	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), args...)
	if err != nil {
		return "", fmt.Errorf("br create: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	out := strings.TrimSpace(string(stdout))
	id := out
	if !opts.Silent {
		id = parseCreatedID(out)
		if id == "" {
			return "", fmt.Errorf("br create: could not parse bead ID from output: %q", out)
		}
	}
	if opts.Parent != "" && !noInherit {
		// The bead exists now: on a failed strip the id still goes back with the
		// error so the caller can record or remove it. One retry for a busy br.
		err := c.stripInheritedLabels(ctx, projectDir, id, opts.Labels)
		if err != nil {
			err = c.stripInheritedLabels(ctx, projectDir, id, opts.Labels)
		}
		if err != nil {
			return id, err
		}
	}
	return id, nil
}

const noInheritFlag = "--no-inherit-labels"

var noInheritSupport sync.Map // beads binary -> bool, read once from `create --help`

// supportsNoInherit reports whether the beads binary takes --no-inherit-labels
// (upstream bd; br rejects it), by its help text, cached per binary.
func (c *cmdClient) supportsNoInherit(ctx context.Context, dir string) bool {
	if PostgresConfigured() {
		return true
	}
	bin := beadsBinary()
	if v, ok := noInheritSupport.Load(bin); ok {
		return v.(bool)
	}
	stdout, stderr, err := c.run(ctx, dir, bin, "create", "--help")
	if err != nil {
		return false // not cached: a failed probe is retried on the next create
	}
	has := strings.Contains(string(stdout), noInheritFlag) || strings.Contains(string(stderr), noInheritFlag)
	noInheritSupport.Store(bin, has)
	return has
}

// stateLabelPrefixes and stateLabels are labels of a parent's own state or
// role. Upstream bd copies a parent's labels onto a child created under it;
// these must never travel, so a child keeps one only if the caller passed it.
var (
	stateLabelPrefixes = []string{
		"owner:", "needs-", "waiting:", "wait-checked:", "parked", "decision:",
		"gate-name:", "gate-of:", "gate-status:", "verifier:", "approver:", "auto-respawn:", "crash-respawn:", "kind:",
		"max-operators:", "viz:", "prod-gate:", "wait-nudged", ProposedPrefix,
	}
	stateLabels = []string{"blocked", "hold", "gate", "needs-user", ownercompat.LegacyNeedsLabel, "superseded", "retired", "review"}
)

func isStateLabel(l string) bool {
	for _, s := range stateLabels {
		if l == s {
			return true
		}
	}
	for _, p := range stateLabelPrefixes {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// stripInheritedLabels removes, in one update, the state labels a new child
// carries that the caller did not ask for. kind:* is covered: a task is
// kind:task only, an objective kind:objective only, a track only when asked.
func (c *cmdClient) stripInheritedLabels(ctx context.Context, dir, id string, asked []string) error {
	stdout, _, err := c.run(ctx, dir, beadsBinary(), "show", id, "--json")
	if err != nil {
		return fmt.Errorf("br show %s (inherited labels): %w", id, err)
	}
	i := bytes.IndexAny(stdout, "[{")
	if i < 0 {
		return nil
	}
	var have struct {
		Labels []string `json:"labels"`
	}
	if stdout[i] == '[' {
		var many []struct {
			ID     string   `json:"id"`
			Labels []string `json:"labels"`
		}
		if json.Unmarshal(stdout[i:], &many) != nil {
			return nil
		}
		for _, m := range many {
			if m.ID == id || have.Labels == nil {
				have.Labels = m.Labels
			}
		}
	} else if json.Unmarshal(stdout[i:], &have) != nil {
		return nil
	}
	keep := map[string]bool{}
	for _, l := range asked {
		keep[l] = true
	}
	var fields []string
	for _, l := range have.Labels {
		if !keep[l] && isStateLabel(l) {
			fields = append(fields, "--remove-label", l)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	if _, stderr, err := c.runUpdate(ctx, dir, id, fields...); err != nil {
		return fmt.Errorf("br update (strip inherited labels): %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// parseCreatedID scans br's chatty `create` output for `Created issue: <id>`.
// The remainder of the line (em-dash + title) is ignored.
func parseCreatedID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		const marker = "Created issue:"
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len(marker):])
		// Trim title suffix after em-dash, en-dash, or hyphen-with-spaces.
		for _, sep := range []string{" — ", " – ", " - "} {
			if i := strings.Index(rest, sep); i >= 0 {
				rest = rest[:i]
				break
			}
		}
		return strings.TrimSpace(rest)
	}
	return ""
}
