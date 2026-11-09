package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Objective mirrors the objectives table.
//
// DescriptionPath and ClosedAt are nullable; sql.Null* lets callers
// distinguish "open" from "closed at unix=0".
type Objective struct {
	ID              int64
	Title           string
	DescriptionPath sql.NullString
	Status          string // "open" by default, "closed", etc.
	CreatedAt       int64
	ClosedAt        sql.NullInt64
}

func (s *Store) InsertObjective(ctx context.Context, o Objective) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO objectives(title, description_path, status, created_at, closed_at)
		VALUES (?, ?, ?, ?, ?)
	`, o.Title, o.DescriptionPath, o.Status, o.CreatedAt, o.ClosedAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert objective %q: %w", o.Title, err)
	}
	return res.LastInsertId()
}

func (s *Store) GetObjective(ctx context.Context, id int64) (*Objective, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, title, description_path, status, created_at, closed_at
		FROM objectives WHERE id = ?
	`, id)
	return scanObjective(row)
}

// ListObjectives returns objectives ordered by id. status="" returns all;
// otherwise filters by status (typically "open" or "closed").
func (s *Store) ListObjectives(ctx context.Context, status string) ([]Objective, error) {
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, title, description_path, status, created_at, closed_at
			FROM objectives ORDER BY id
		`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, title, description_path, status, created_at, closed_at
			FROM objectives WHERE status = ? ORDER BY id
		`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list objectives: %w", err)
	}
	defer rows.Close()
	var out []Objective
	for rows.Next() {
		var o Objective
		if err := rows.Scan(&o.ID, &o.Title, &o.DescriptionPath, &o.Status, &o.CreatedAt, &o.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CloseObjective sets status="closed" and closed_at=ts. Returns sql.ErrNoRows
// equivalent (nil, no rows touched is silent — callers can check via Get).
func (s *Store) CloseObjective(ctx context.Context, id int64, ts int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE objectives SET status = 'closed', closed_at = ? WHERE id = ?
	`, ts, id)
	if err != nil {
		return fmt.Errorf("store: close objective %d: %w", id, err)
	}
	return nil
}

func scanObjective(row *sql.Row) (*Objective, error) {
	var o Objective
	err := row.Scan(&o.ID, &o.Title, &o.DescriptionPath, &o.Status, &o.CreatedAt, &o.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan objective: %w", err)
	}
	return &o, nil
}
