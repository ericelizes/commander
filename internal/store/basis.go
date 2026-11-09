package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Basis is what a decision card was raised against. Spec is the fingerprint of
// its bead's title+description+acceptance (beads.SpecHash); Gen is the pitch
// version; Raised is when the card (or its last "still right" check) was made.
// Named stores named bead status|state|spec hash (legacy entries omit spec); PRs stores tracked repo#number states.
// Answers stores answered pitch versions on the same step and its user gates.
type Basis struct {
	Spec    string            `json:"spec,omitempty"`
	Gen     int               `json:"gen,omitempty"`
	Raised  int64             `json:"raised,omitempty"`
	Named   map[string]string `json:"named,omitempty"`
	PRs     map[string]string `json:"prs,omitempty"`
	Answers map[string]int    `json:"answers,omitempty"`
	// AgeChecks counts the "still right" checks made on age alone since the
	// spec last changed (the verb refuses a second one in a row).
	AgeChecks int `json:"age_checks,omitempty"`
}

// ParseBasis reads a stored basis; "" and bad JSON are (nil, false).
func ParseBasis(s string) (*Basis, bool) {
	if s == "" {
		return nil, false
	}
	var b Basis
	if json.Unmarshal([]byte(s), &b) != nil {
		return nil, false
	}
	return &b, true
}

func (b *Basis) json() string {
	if b == nil {
		return ""
	}
	out, _ := json.Marshal(b)
	return string(out)
}

// RebaselineCard rewrites the basis of the card's latest version in place (the
// card keeps its text): a Manager confirmed it is still right.
func (s *Store) RebaselineCard(ctx context.Context, projectID int64, beadID string, b Basis) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM pitches WHERE project_id = ? AND bead_id = ?`, projectID, beadID).Scan(&version); err != nil || version == 0 {
		return fmt.Errorf("store: %s has no card to re-baseline", beadID)
	}
	b.Gen = version
	if b.Raised == 0 {
		b.Raised = time.Now().Unix()
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE pitches SET basis = ? WHERE project_id = ? AND bead_id = ? AND version = ?`, b.json(), projectID, beadID, version); err != nil {
		return fmt.Errorf("store: re-baseline %s: %w", beadID, err)
	}
	return nil
}
