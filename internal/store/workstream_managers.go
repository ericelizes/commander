package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// WorkstreamManager is one Manager (a Claude chat session) owning one workstream.
type WorkstreamManager struct {
	ProjectID        int64
	WorkstreamBeadID string
	AmRole           string
	SessionFile      string
	CreatedAt        int64
}

// WorkstreamManagerRole is the mail role of a workstream's Manager. It is keyed
// by the bead id, which is stable across re-ranking and retitling.
func WorkstreamManagerRole(workstreamBeadID string) string {
	return "manager-" + workstreamBeadID
}

// UpsertWorkstreamManager records (or updates) the Manager for a workstream.
func (s *Store) UpsertWorkstreamManager(ctx context.Context, m WorkstreamManager) error {
	if m.WorkstreamBeadID == "" || m.SessionFile == "" {
		return fmt.Errorf("store: UpsertWorkstreamManager requires workstream bead id and session file")
	}
	if m.AmRole == "" {
		m.AmRole = WorkstreamManagerRole(m.WorkstreamBeadID)
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workstream_managers(project_id, workstream_bead_id, am_role, session_file, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, workstream_bead_id) DO UPDATE SET
			am_role = excluded.am_role, session_file = excluded.session_file
	`, m.ProjectID, m.WorkstreamBeadID, m.AmRole, m.SessionFile, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: upsert workstream manager %s: %w", m.WorkstreamBeadID, err)
	}
	return nil
}

// GetWorkstreamManager returns the Manager row, or (nil, nil) when absent.
func (s *Store) GetWorkstreamManager(ctx context.Context, projectID int64, beadID string) (*WorkstreamManager, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT project_id, workstream_bead_id, am_role, session_file, created_at
		FROM workstream_managers WHERE project_id = ? AND workstream_bead_id = ?
	`, projectID, beadID)
	return scanWorkstreamManager(row)
}

// GetWorkstreamManagerByRole resolves a mail role to its Manager, or (nil, nil).
func (s *Store) GetWorkstreamManagerByRole(ctx context.Context, role string) (*WorkstreamManager, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT project_id, workstream_bead_id, am_role, session_file, created_at
		FROM workstream_managers WHERE am_role = ?
	`, role)
	return scanWorkstreamManager(row)
}

// ListWorkstreamManagers returns every Manager, oldest first.
func (s *Store) ListWorkstreamManagers(ctx context.Context) ([]WorkstreamManager, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, workstream_bead_id, am_role, session_file, created_at
		FROM workstream_managers ORDER BY created_at, workstream_bead_id
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list workstream managers: %w", err)
	}
	defer rows.Close()
	var out []WorkstreamManager
	for rows.Next() {
		var m WorkstreamManager
		if err := rows.Scan(&m.ProjectID, &m.WorkstreamBeadID, &m.AmRole, &m.SessionFile, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanWorkstreamManager(row *sql.Row) (*WorkstreamManager, error) {
	var m WorkstreamManager
	err := row.Scan(&m.ProjectID, &m.WorkstreamBeadID, &m.AmRole, &m.SessionFile, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan workstream manager: %w", err)
	}
	return &m, nil
}

// RenameAmRole moves a role -> am-name mapping to a new role, keeping the am
// name, so mail already addressed to that agent keeps arriving. A missing old
// role is a no-op.
func (s *Store) RenameAmRole(ctx context.Context, amProjectKey, oldRole, newRole string) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE am_agents SET role = ? WHERE am_project_key = ? AND role = ?
	`, newRole, amProjectKey, oldRole); err != nil {
		return fmt.Errorf("store: rename am role %s -> %s: %w", oldRole, newRole, err)
	}
	return nil
}

// UnregisterNestedManager removes only the exact registration, retaining every
// runtime, mail mapping, run, rank and file. The caller must verify fresh ancestry
// and authorization. Audit and deletion commit together, including on retries.
func (s *Store) UnregisterNestedManager(ctx context.Context, projectID int64, beadID, rootID, actor string) (bool, error) {
	if beadID == "" || rootID == "" || beadID == rootID || actor == "" {
		return false, fmt.Errorf("non-root bead, root and actor required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO manager_unregistrations(project_id,bead_id,root_id,actor,manager_role,session_file,created_at) SELECT project_id,workstream_bead_id,?,?,am_role,session_file,? FROM workstream_managers WHERE project_id=? AND workstream_bead_id=? AND am_role=?`, rootID, actor, time.Now().Unix(), projectID, beadID, WorkstreamManagerRole(beadID))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workstream_managers WHERE project_id=? AND workstream_bead_id=? AND am_role=?`, projectID, beadID, WorkstreamManagerRole(beadID)); err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}
