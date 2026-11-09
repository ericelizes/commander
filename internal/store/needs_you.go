package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxAskLen is the longest ask the sidebar will show.
const MaxAskLen = 30

// NeedsYou is what the user must do for one step flagged needs-user.
type NeedsYou struct {
	ProjectID int64
	BeadID    string
	Ask       string // short, verb-first: "Run 4 staging cmds"
	Note      string // the full note: commands, links, context
	Decision  string // the decision card as JSON (see Decision); "" = none
	Since     int64
	// Basis is what the card was raised against: written with it (nil = not
	// recorded), read back from the card's latest pitch version.
	Basis *Basis
}

// latestBasis is the basis of a card's latest pitch version.
const latestBasis = `(SELECT p.basis FROM pitches p WHERE p.project_id = needs_you.project_id AND p.bead_id = needs_you.bead_id ORDER BY p.version DESC LIMIT 1)`

// ValidateAsk checks a needs-user ask: non-empty and at most MaxAskLen runes.
func ValidateAsk(ask string) error {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return errors.New("an ask is required: what the user must do, verb first, e.g. \"Approve: import rollout\"")
	}
	if n := len([]rune(ask)); n > MaxAskLen {
		return fmt.Errorf("the ask is %d characters; keep it to %d or fewer (put the detail in --note)", n, MaxAskLen)
	}
	return nil
}

// UpsertNeedsYou records (or replaces) the ask for a flagged step.
func (s *Store) UpsertNeedsYou(ctx context.Context, n NeedsYou) error {
	if n.BeadID == "" {
		return errors.New("store: UpsertNeedsYou requires a bead id")
	}
	if n.Since == 0 {
		n.Since = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO needs_you(project_id, bead_id, ask, note, since, decision) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, bead_id) DO UPDATE SET ask = excluded.ask, note = excluded.note,
		  decision = CASE WHEN excluded.decision = '' THEN needs_you.decision ELSE excluded.decision END
	`, n.ProjectID, n.BeadID, strings.TrimSpace(n.Ask), n.Note, n.Since, n.Decision)
	if err != nil {
		return fmt.Errorf("store: upsert needs_you %s: %w", n.BeadID, err)
	}
	// Every card is kept: a changed one is the pitch's next version.
	return s.RecordPitch(ctx, n.ProjectID, n.BeadID, strings.TrimSpace(n.Ask), n.Decision, n.Basis)
}

// GetNeedsYou returns the ask for a step, or (nil, nil).
func (s *Store) GetNeedsYou(ctx context.Context, projectID int64, beadID string) (*NeedsYou, error) {
	var n NeedsYou
	var basis sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, bead_id, ask, note, since, decision, `+latestBasis+` FROM needs_you WHERE project_id = ? AND bead_id = ?
	`, projectID, beadID).Scan(&n.ProjectID, &n.BeadID, &n.Ask, &n.Note, &n.Since, &n.Decision, &basis)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get needs_you: %w", err)
	}
	n.Basis, _ = ParseBasis(basis.String)
	return &n, nil
}

// ListNeedsYou returns every ask, oldest first.
func (s *Store) ListNeedsYou(ctx context.Context) ([]NeedsYou, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, bead_id, ask, note, since, decision, `+latestBasis+` FROM needs_you ORDER BY since, bead_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list needs_you: %w", err)
	}
	defer rows.Close()
	var out []NeedsYou
	for rows.Next() {
		var n NeedsYou
		var basis sql.NullString
		if err := rows.Scan(&n.ProjectID, &n.BeadID, &n.Ask, &n.Note, &n.Since, &n.Decision, &basis); err != nil {
			return nil, err
		}
		n.Basis, _ = ParseBasis(basis.String)
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteNeedsYou clears a step's ask (it left needs-user or was closed).
func (s *Store) DeleteNeedsYou(ctx context.Context, projectID int64, beadID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM needs_you WHERE project_id = ? AND bead_id = ?`, projectID, beadID); err != nil {
		return fmt.Errorf("store: delete needs_you: %w", err)
	}
	return nil
}
