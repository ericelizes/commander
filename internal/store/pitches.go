package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"time"
)

// Pitch is one version of a decision card, with the answer when it got one.
type Pitch struct {
	ProjectID  int64  `json:"project_id"`
	BeadID     string `json:"bead_id"`
	Version    int    `json:"version"`
	Title      string `json:"title"`
	Decision   string `json:"decision"` // the card's JSON
	CreatedAt  int64  `json:"created_at"`
	Answer     string `json:"answer,omitempty"`
	AnsweredAt int64  `json:"answered_at,omitempty"`
}

// RecordPitch keeps a card as the step's next version, unless it is the same
// card as the latest one.
func (s *Store) RecordPitch(ctx context.Context, projectID int64, beadID, title, decision string, basis *Basis) error {
	if decision == "" {
		return nil
	}
	var version int
	var last, answer string
	var lastBasis sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT version, decision, answer, basis FROM pitches WHERE project_id = ? AND bead_id = ? ORDER BY version DESC LIMIT 1`, projectID, beadID).Scan(&version, &last, &answer, &lastBasis)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: latest pitch %s: %w", beadID, err)
	}
	if last == decision && answer == "" {
		sameScope := true
		if s.postgres {
			if err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND NOT EXISTS(SELECT 1 FROM pitch_authority_bases p WHERE p.project_id=b.project_id AND p.bead_id=b.id AND p.version=? AND p.scope_epoch=b.human_scope_epoch))`, projectID, beadID, version).Scan(&sameScope); err != nil {
				return err
			}
		}
		// The same card again is no new version, unless it was re-raised
		// against a changed spec/dependency (or the old one has no basis): then it is a
		// new version with the new basis.
		old, _ := ParseBasis(lastBasis.String)
		if sameScope && (basis == nil || basis.Spec == "" || (old != nil && old.Spec == basis.Spec && maps.Equal(old.Named, basis.Named) && maps.Equal(old.PRs, basis.PRs) && maps.Equal(old.Answers, basis.Answers))) {
			return nil
		}
	}
	now := time.Now().Unix()
	var stored any
	if basis != nil {
		nb := *basis
		nb.Gen = version + 1
		if nb.Raised == 0 {
			nb.Raised = now
		}
		stored = nb.json()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO pitches(project_id, bead_id, version, title, decision, created_at, basis) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, beadID, version+1, title, decision, now, stored); err != nil {
		return fmt.Errorf("store: record pitch %s: %w", beadID, err)
	}
	return nil
}

// AnswerPitch writes the user's answer on the step's latest version.
func (s *Store) AnswerPitch(ctx context.Context, projectID int64, beadID, answer string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE pitches SET answer = ?, answered_at = ?
		WHERE project_id = ? AND bead_id = ? AND version = (SELECT MAX(version) FROM pitches WHERE project_id = ? AND bead_id = ?)`,
		answer, time.Now().Unix(), projectID, beadID, projectID, beadID)
	if err != nil {
		return fmt.Errorf("store: answer pitch %s: %w", beadID, err)
	}
	return nil
}

// ListPitches returns every version of the pitches on a bead and the beads
// under it (a workstream or a track), oldest first.
func (s *Store) ListPitches(ctx context.Context, projectID int64, under string) ([]Pitch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, bead_id, version, title, decision, created_at, answer, answered_at FROM pitches
		WHERE project_id = ? AND (? = '' OR bead_id = ? OR bead_id LIKE ? || '.%')
		ORDER BY created_at, bead_id, version`, projectID, under, under, under)
	if err != nil {
		return nil, fmt.Errorf("store: list pitches: %w", err)
	}
	defer rows.Close()
	var out []Pitch
	for rows.Next() {
		var p Pitch
		if err := rows.Scan(&p.ProjectID, &p.BeadID, &p.Version, &p.Title, &p.Decision, &p.CreatedAt, &p.Answer, &p.AnsweredAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
