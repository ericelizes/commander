package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExactDescriptionReader supplies authoritative bytes without Show's display
// normalization or text fallback. Callers must refuse clients lacking it.
type ExactDescriptionReader interface {
	ExactDescription(context.Context, string, string) (string, error)
}

func (c *cmdClient) ExactDescription(ctx context.Context, projectDir, issueID string) (string, error) {
	if issueID == "" {
		return "", fmt.Errorf("beads: exact description requires issueID")
	}
	out, _, err := c.run(ctx, projectDir, beadsBinary(), "show", issueID, "--json")
	if err != nil {
		return "", fmt.Errorf("beads: exact description: %w", err)
	}
	// Require one complete JSON response and one matching record. Null values
	// and ambiguous identities cannot prove an empty stored value.
	type record struct {
		ID          string          `json:"id"`
		Description json.RawMessage `json:"description"`
	}
	var records []record
	if len(out) > 0 && json.Unmarshal(out, &records) == nil {
	} else {
		var one record
		if err := json.Unmarshal(out, &one); err != nil {
			return "", fmt.Errorf("beads: invalid exact description JSON: %w", err)
		}
		records = []record{one}
	}
	if len(records) != 1 || records[0].ID != issueID {
		return "", fmt.Errorf("beads: missing or ambiguous exact description")
	}
	if len(records[0].Description) == 0 {
		return c.nativeOmittedDescription(ctx, projectDir, issueID, out)
	}
	var description *string
	if err := json.Unmarshal(records[0].Description, &description); err != nil || description == nil {
		return "", fmt.Errorf("beads: invalid exact description")
	}
	return *description, nil
}

// nativeOmittedDescription supports the observed bd 1.3.1 show contract only.
// An omission is never itself evidence of emptiness: ask the backend for an
// explicit exact field using its read-only SQL door. Unknown versions/shapes
// refuse rather than relying on display normalization or omitempty guesses.
func (c *cmdClient) nativeOmittedDescription(ctx context.Context, dir, id string, out []byte) (string, error) {
	refuse := fmt.Errorf("beads: unsupported omitted exact description")
	var rows []struct {
		ID        string    `json:"id"`
		Title     *string   `json:"title"`
		Status    string    `json:"status"`
		Priority  *int      `json:"priority"`
		IssueType string    `json:"issue_type"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if json.Unmarshal(out, &rows) != nil || len(rows) != 1 {
		return "", refuse
	}
	row := rows[0]
	if row.ID != id || row.Title == nil || row.Status == "" || row.Priority == nil || row.IssueType == "" || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		return "", refuse
	}
	// Restrict the identifier alphabet before constructing a single SELECT.
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-_.", ch)) {
			return "", refuse
		}
	}
	version, _, err := c.run(ctx, dir, beadsBinary(), "--version")
	if err != nil || strings.TrimSpace(string(version)) != "bd version 1.3.1 (c1c4b642a)" {
		return "", refuse
	}
	exact, _, err := c.run(ctx, dir, beadsBinary(), "--readonly", "sql", "SELECT id, description FROM issues WHERE id = '"+id+"'", "--json")
	if err != nil {
		return "", fmt.Errorf("beads: read-only exact description query: %w", err)
	}
	var fields []struct {
		ID          string  `json:"id"`
		Description *string `json:"description"`
	}
	if json.Unmarshal(exact, &fields) != nil || len(fields) != 1 || fields[0].ID != id || fields[0].Description == nil {
		return "", fmt.Errorf("beads: missing or ambiguous exact description query result")
	}
	return *fields[0].Description, nil
}

func (m *MemClient) ExactDescription(ctx context.Context, projectDir, issueID string) (string, error) {
	b, err := m.Show(ctx, projectDir, issueID)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fmt.Errorf("beads: missing exact description")
	}
	return b.Description, nil
}
