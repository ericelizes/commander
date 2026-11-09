package store

import (
	"context"
	"fmt"
)

// RunWatch is what the minute watch last saw of a running Operator.
type RunWatch struct {
	ActiveAt  int64 // last write to its transcript (unix s); 0 when never seen
	QuietTold int64 // the ActiveAt its Manager was told it went quiet at
}

// Checkin is when a Manager next checks in on its Operators.
type Checkin struct {
	Due     int64
	Checked int64 // the watch's last look at its Operators
}

// RunWatches returns the watch fields of every running run, by run id.
func (s *Store) RunWatches(ctx context.Context) (map[int64]RunWatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, active_at, quiet_told FROM agent_runs WHERE status = 'running'`)
	if err != nil {
		return nil, fmt.Errorf("run watches: %w", err)
	}
	defer rows.Close()
	out := map[int64]RunWatch{}
	for rows.Next() {
		var id int64
		var w RunWatch
		if err := rows.Scan(&id, &w.ActiveAt, &w.QuietTold); err != nil {
			return nil, fmt.Errorf("run watches: %w", err)
		}
		out[id] = w
	}
	return out, rows.Err()
}

// SetRunWatch records what the watch saw of run id.
func (s *Store) SetRunWatch(ctx context.Context, id int64, w RunWatch) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET active_at = ?, quiet_told = ? WHERE id = ?`, w.ActiveAt, w.QuietTold, id); err != nil {
		return fmt.Errorf("set run watch %d: %w", id, err)
	}
	return nil
}

// Checkins returns every Manager's check-in, by role.
func (s *Store) Checkins(ctx context.Context) (map[string]Checkin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role, due, checked FROM manager_checkins`)
	if err != nil {
		return nil, fmt.Errorf("checkins: %w", err)
	}
	defer rows.Close()
	out := map[string]Checkin{}
	for rows.Next() {
		var role string
		var c Checkin
		if err := rows.Scan(&role, &c.Due, &c.Checked); err != nil {
			return nil, fmt.Errorf("checkins: %w", err)
		}
		out[role] = c
	}
	return out, rows.Err()
}

// SetCheckin records a Manager's next check-in and the watch's last look.
func (s *Store) SetCheckin(ctx context.Context, role string, c Checkin) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO manager_checkins(role, due, checked) VALUES (?, ?, ?)
		ON CONFLICT(role) DO UPDATE SET due = excluded.due, checked = excluded.checked`, role, c.Due, c.Checked)
	if err != nil {
		return fmt.Errorf("set checkin %s: %w", role, err)
	}
	return nil
}

// ClearCheckin drops a Manager's check-in (it has no Operators left to watch).
func (s *Store) ClearCheckin(ctx context.Context, role string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM manager_checkins WHERE role = ?`, role); err != nil {
		return fmt.Errorf("clear checkin %s: %w", role, err)
	}
	return nil
}

// WatchIssue is something the minute watch finds wrong with a workstream.
type WatchIssue struct {
	Key    string
	WS     string
	Text   string
	Since  int64
	Active bool
	Told   bool
}

// WatchIssues returns every recorded issue.
func (s *Store) WatchIssues(ctx context.Context) ([]WatchIssue, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, ws, text, since, active, told FROM watch_issues ORDER BY since`)
	if err != nil {
		return nil, fmt.Errorf("watch issues: %w", err)
	}
	defer rows.Close()
	var out []WatchIssue
	for rows.Next() {
		var i WatchIssue
		if err := rows.Scan(&i.Key, &i.WS, &i.Text, &i.Since, &i.Active, &i.Told); err != nil {
			return nil, fmt.Errorf("watch issues: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SetWatchIssues replaces every recorded issue with these.
func (s *Store) SetWatchIssues(ctx context.Context, issues []WatchIssue) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set watch issues: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM watch_issues`); err != nil {
		return fmt.Errorf("set watch issues: %w", err)
	}
	for _, i := range issues {
		if _, err := tx.ExecContext(ctx, `INSERT INTO watch_issues(key, ws, text, since, active, told) VALUES (?, ?, ?, ?, ?, ?)`,
			i.Key, i.WS, i.Text, i.Since, i.Active, i.Told); err != nil {
			return fmt.Errorf("set watch issues: %w", err)
		}
	}
	return tx.Commit()
}
