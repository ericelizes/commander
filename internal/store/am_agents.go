package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UpsertAmAgent records the am server-assigned name for a (am project, role)
// pair. Idempotent: re-upserting the same role updates the name. See
// docs/agent-mail-routing.md for why role->name mapping exists.
func (s *Store) UpsertAmAgent(ctx context.Context, amProjectKey, role, amName string) error {
	if amProjectKey == "" || role == "" || amName == "" {
		return fmt.Errorf("store: UpsertAmAgent requires amProjectKey, role, amName")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO am_agents(am_project_key, role, am_name, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(am_project_key, role) DO UPDATE SET am_name = excluded.am_name
	`, amProjectKey, role, amName, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store: upsert am agent %s/%s: %w", amProjectKey, role, err)
	}
	return nil
}

// GetAmAgent resolves the am server-assigned name for a (am project, role)
// pair. Returns ("", nil) when no mapping exists yet.
func (s *Store) GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `
		SELECT am_name FROM am_agents WHERE am_project_key = ? AND role = ?
	`, amProjectKey, role).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get am agent %s/%s: %w", amProjectKey, role, err)
	}
	return name, nil
}

// AmAgent is one role -> am-name mapping row.
type AmAgent struct {
	Role   string
	AmName string
}

// ListAmAgents returns every role mapped under amProjectKey, ordered by role.
func (s *Store) ListAmAgents(ctx context.Context, amProjectKey string) ([]AmAgent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT role, am_name FROM am_agents WHERE am_project_key = ? ORDER BY role
	`, amProjectKey)
	if err != nil {
		return nil, fmt.Errorf("store: list am agents %s: %w", amProjectKey, err)
	}
	defer rows.Close()
	var out []AmAgent
	for rows.Next() {
		var a AmAgent
		if err := rows.Scan(&a.Role, &a.AmName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
