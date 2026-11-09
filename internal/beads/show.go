package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Show invokes `br show <issueID>` from projectDir and parses the
// human-readable output into a *Bead. Best-effort extraction of ID,
// Title, Status, Type, Labels — fields br does not emit are left zero.
func (c *cmdClient) Show(ctx context.Context, projectDir, issueID string) (*Bead, error) {
	if issueID == "" {
		return nil, fmt.Errorf("beads: Show requires issueID")
	}

	js, jsonOK := c.showJSONDescription(ctx, projectDir, issueID)
	stdout, stderr, err := c.run(ctx, projectDir, beadsBinary(), "show", issueID)
	if err != nil {
		text := strings.TrimSpace(string(stderr))
		if saysNotFound(text, issueID) {
			return nil, fmt.Errorf("br show: %w: %s", ErrNotFound, text)
		}
		return nil, fmt.Errorf("br show: %w: %s", err, text)
	}

	b := parseShowOutput(string(stdout))
	if jsonOK {
		if js.Title != nil {
			b.Title = *js.Title
		}
		if js.Status != nil {
			b.Status = normalizeStatus(*js.Status)
		}
		b.Description, b.AcceptanceCriteria, b.CloseReason, b.ParentID = js.Description, js.Acceptance, js.CloseReason, js.Parent
		b.Labels, b.Type, b.AssignedAgent = js.Labels, js.Type, js.Assignee
	}
	if b.ID == "" {
		// Fall back to the requested ID; br's text format varies and we
		// don't want to fail the caller just because we couldn't find
		// the literal ID line.
		b.ID = issueID
	}
	return b, nil
}

// ErrNotFound is a Show that the backend answered with "no such bead". Any other
// failed read says nothing about whether the bead exists.
var ErrNotFound = errors.New("bead not found")

// saysNotFound matches the exact sentence of each backend about this very id: bd's
// "Issue X not found", br's "Issue not found: X". Other text is not read as gone.
func saysNotFound(stderr, id string) bool {
	return strings.Contains(stderr, "Issue "+id+" not found") || strings.Contains(stderr, "Issue not found: "+id)
}

// showJSON is what Show takes from `br show <id> --json` over the text form.
type showJSON struct {
	ID          string  `json:"id"`
	Title       *string `json:"title"`
	Status      *string `json:"status"`
	Description string  `json:"description"`
	Acceptance  string  `json:"acceptance_criteria"`
	// CloseReason: the text form has none, and a step closed as superseded is
	// known by it (Superseded; the gate unlink door judges on a fresh Show).
	CloseReason string `json:"close_reason"`
	// Parent: bd and br both say "parent"; neither text form has a line for it.
	Parent string `json:"parent"`
	// Labels, Type, Assignee: bd's text form has the description above its LABELS
	// line and no metadata block, so a body line "Labels: x" reads as the field.
	// A bead with no labels has no "labels" key. "owner" is the creator, not the assignee.
	Labels   []string `json:"labels"`
	Type     string   `json:"issue_type"`
	Assignee string   `json:"assignee"`
}

// showJSONDescription reads the description, acceptance criteria and close
// reason from `br show <id> --json`, which is exact (no section headings to
// guess at). ok is false when the JSON read fails, so the caller keeps the
// text parser's description.
func (c *cmdClient) showJSONDescription(ctx context.Context, projectDir, issueID string) (showJSON, bool) {
	stdout, _, err := c.run(ctx, projectDir, beadsBinary(), "show", issueID, "--json")
	if err != nil {
		return showJSON{}, false
	}
	// br may print a banner before the JSON; start at the first [ or {.
	i := bytes.IndexAny(stdout, "[{")
	if i < 0 {
		return showJSON{}, false
	}
	data := stdout[i:]
	var one showJSON
	if data[0] == '[' {
		var many []showJSON
		if json.Unmarshal(data, &many) != nil || len(many) == 0 {
			return showJSON{}, false
		}
		one = many[0]
		for _, m := range many {
			if m.ID == issueID {
				one = m
			}
		}
	} else if json.Unmarshal(data, &one) != nil {
		return showJSON{}, false
	}
	one.Description, one.Acceptance, one.CloseReason = strings.TrimSpace(one.Description), strings.TrimSpace(one.Acceptance), strings.TrimSpace(one.CloseReason)
	return one, true
}

// parseShowOutput is a small line-by-line scanner. br's `show` output
// is a header line plus `Key: value` body lines, with optional
// formatting glyphs (bullets, status circles). We tolerate leading
// non-letter runes per line.
func parseShowOutput(out string) *Bead {
	b := &Bead{}
	var desc []string
	inBody, seenBody := false, false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if inBody {
			if showSections[line] {
				inBody = false
			} else {
				desc = append(desc, strings.TrimRight(raw, " \t\r"))
				continue
			}
		}
		if line == "" {
			// The first blank line after the metadata block starts the description.
			if b.ID != "" && !seenBody && (b.Labels != nil || b.Type != "" || b.AssignedAgent != "") {
				inBody, seenBody = true, true
			}
			continue
		}

		// Header form: `<glyph> <id> · <title>   [<status>]`
		if strings.Contains(line, "·") && b.ID == "" {
			b.ID, b.Title, b.Status = parseHeader(line)
			continue
		}

		// Body form: `Key: value`
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(strings.TrimLeft(line[:colon], "-•* "))
		val := strings.TrimSpace(line[colon+1:])
		switch strings.ToLower(key) {
		case "id":
			if b.ID == "" {
				b.ID = val
			}
		case "title":
			if b.Title == "" {
				b.Title = val
			}
		case "status":
			if b.Status == "" {
				b.Status = val
			}
		case "type":
			b.Type = val
		case "labels":
			b.Labels = splitLabels(val)
		case "parent", "parent_id", "parent id":
			b.ParentID = val
		case "owner", "assigned", "assigned_agent", "assigned agent":
			b.AssignedAgent = val
		}
	}
	if len(desc) > 0 {
		b.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	}
	return b
}

// showSections are the headings after the free-text description in `br show`.
var showSections = map[string]bool{
	"Dependencies:": true, "Dependents:": true, "Comments:": true, "Children:": true,
}

// parseHeader extracts ID, title, and (optional bracketed) status from
// the first line of `br show` output.
func parseHeader(line string) (id, title, status string) {
	// Strip leading glyph (e.g. `○ `, `● `, `✓ `).
	line = strings.TrimLeftFunc(line, func(r rune) bool { return !isWord(r) })

	// Pull off bracketed status `[● P0 · OPEN]` if present.
	if lb := strings.LastIndex(line, "["); lb >= 0 {
		if rb := strings.LastIndex(line, "]"); rb > lb {
			status = normalizeStatus(line[lb+1 : rb])
			line = strings.TrimSpace(line[:lb])
		}
	}

	// Split `<id> · <title>`.
	if i := strings.Index(line, "·"); i >= 0 {
		id = strings.TrimSpace(line[:i])
		title = strings.TrimSpace(line[i+len("·"):])
	} else {
		id = strings.TrimSpace(line)
	}
	return id, title, status
}

// normalizeStatus turns the bracket text `P4 · CLOSED` (priority, then status)
// into the status word as br stores it: `closed`, `in_progress`.
func normalizeStatus(s string) string {
	if i := strings.LastIndex(s, "·"); i >= 0 {
		s = s[i+len("·"):]
	}
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))
}

func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
