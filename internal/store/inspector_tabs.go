package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// InspectorTab is one tab in a window's inspector. ProjectID 0 and
// WorkstreamBeadID "" are the Commander window.
type InspectorTab struct {
	ID               int64
	ProjectID        int64
	WorkstreamBeadID string
	Position         int
	Title            string
	Kind             string // url | cmd
	Target           string
	Renderer         string // "" = auto-detect
	RefreshSecs      int
	AddedBy          string
	CreatedAt        time.Time
}

const inspectorTabCols = `id, project_id, workstream_bead_id, position, title, kind, target, renderer, refresh_secs, added_by, created_at`

func scanInspectorTab(sc interface{ Scan(...any) error }) (InspectorTab, error) {
	var t InspectorTab
	var created int64
	err := sc.Scan(&t.ID, &t.ProjectID, &t.WorkstreamBeadID, &t.Position, &t.Title, &t.Kind,
		&t.Target, &t.Renderer, &t.RefreshSecs, &t.AddedBy, &created)
	t.CreatedAt = time.Unix(created, 0)
	return t, err
}

// ListTabs returns a window's tabs in position order.
func (s *Store) ListTabs(ctx context.Context, projectID int64, wsBeadID string) ([]InspectorTab, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inspectorTabCols+`
		FROM inspector_tabs WHERE project_id = ? AND workstream_bead_id = ?
		ORDER BY position, id`, projectID, wsBeadID)
	if err != nil {
		return nil, fmt.Errorf("store: list inspector tabs: %w", err)
	}
	defer rows.Close()
	var out []InspectorTab
	for rows.Next() {
		t, err := scanInspectorTab(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan inspector tab: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTab returns one tab by id, or (nil, nil).
func (s *Store) GetTab(ctx context.Context, id int64) (*InspectorTab, error) {
	t, err := scanInspectorTab(s.db.QueryRowContext(ctx,
		`SELECT `+inspectorTabCols+` FROM inspector_tabs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get inspector tab: %w", err)
	}
	return &t, nil
}

// AddTab appends a tab to the window. It is idempotent on (window, target):
// when the target is already a tab, the existing id is returned with
// created=false and nothing changes.
func (s *Store) AddTab(ctx context.Context, t InspectorTab) (id int64, created bool, err error) {
	if t.Target == "" || t.Title == "" {
		return 0, false, fmt.Errorf("store: AddTab requires a title and a target")
	}
	if t.Kind == "" {
		t.Kind = "url"
	}
	if t.RefreshSecs <= 0 {
		t.RefreshSecs = 60
	}
	if t.AddedBy == "" {
		t.AddedBy = "user"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("store: add inspector tab: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = tx.QueryRowContext(ctx, `SELECT id FROM inspector_tabs
		WHERE project_id = ? AND workstream_bead_id = ? AND target = ?`,
		t.ProjectID, t.WorkstreamBeadID, t.Target).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("store: add inspector tab: %w", err)
	}
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM inspector_tabs
		WHERE project_id = ? AND workstream_bead_id = ?`, t.ProjectID, t.WorkstreamBeadID).Scan(&next); err != nil {
		return 0, false, fmt.Errorf("store: add inspector tab: %w", err)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO inspector_tabs
		(project_id, workstream_bead_id, position, title, kind, target, renderer, refresh_secs, added_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		t.ProjectID, t.WorkstreamBeadID, next, t.Title, t.Kind, t.Target, t.Renderer,
		t.RefreshSecs, t.AddedBy, time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("store: add inspector tab: %w", err)
	}
	return id, true, tx.Commit()
}

// tabIDsInWindow lists a window's tab ids in position order.
func tabIDsInWindow(ctx context.Context, tx *sql.Tx, pid int64, ws string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM inspector_tabs
		WHERE project_id = ? AND workstream_bead_id = ? ORDER BY position, id`, pid, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func setPositions(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE inspector_tabs SET position = ? WHERE id = ?`, i+1, id); err != nil {
			return err
		}
	}
	return nil
}

// RemoveTab deletes a tab, renumbers the remaining positions and clears the
// window's active tab (and cache) if it was the removed one.
func (s *Store) RemoveTab(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: remove inspector tab: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pid int64
	var ws string
	err = tx.QueryRowContext(ctx, `SELECT project_id, workstream_bead_id FROM inspector_tabs WHERE id = ?`, id).Scan(&pid, &ws)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: remove inspector tab: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inspector_tabs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: remove inspector tab: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inspector_state SET active_tab_id = NULL, cache_json = NULL, cached_at = NULL
		WHERE project_id = ? AND workstream_bead_id = ? AND active_tab_id = ?`, pid, ws, id); err != nil {
		return fmt.Errorf("store: remove inspector tab: %w", err)
	}
	ids, err := tabIDsInWindow(ctx, tx, pid, ws)
	if err == nil {
		err = setPositions(ctx, tx, ids)
	}
	if err != nil {
		return fmt.Errorf("store: renumber inspector tabs: %w", err)
	}
	return tx.Commit()
}

// MoveTab places a tab at the 1-based position pos within its window (clamped),
// shifting the others.
func (s *Store) MoveTab(ctx context.Context, id int64, pos int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: move inspector tab: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pid int64
	var ws string
	if err := tx.QueryRowContext(ctx, `SELECT project_id, workstream_bead_id FROM inspector_tabs WHERE id = ?`, id).Scan(&pid, &ws); err != nil {
		return fmt.Errorf("store: move inspector tab %d: %w", id, err)
	}
	all, err := tabIDsInWindow(ctx, tx, pid, ws)
	if err != nil {
		return fmt.Errorf("store: move inspector tab: %w", err)
	}
	var others []int64
	for _, x := range all {
		if x != id {
			others = append(others, x)
		}
	}
	if pos < 1 {
		pos = 1
	}
	if pos > len(others)+1 {
		pos = len(others) + 1
	}
	ordered := append(append(append([]int64{}, others[:pos-1]...), id), others[pos-1:]...)
	if err := setPositions(ctx, tx, ordered); err != nil {
		return fmt.Errorf("store: move inspector tab: %w", err)
	}
	return tx.Commit()
}

// SetActiveTab records which tab a window shows. Switching to a different tab
// drops the cached card (it belongs to the previous tab).
func (s *Store) SetActiveTab(ctx context.Context, projectID int64, wsBeadID string, tabID int64) error {
	query := `
		INSERT INTO inspector_state(project_id, workstream_bead_id, active_tab_id)
		VALUES (?, ?, ?)
		ON CONFLICT(project_id, workstream_bead_id) DO UPDATE SET
			cache_json = CASE WHEN active_tab_id IS excluded.active_tab_id THEN cache_json ELSE NULL END,
			cached_at  = CASE WHEN active_tab_id IS excluded.active_tab_id THEN cached_at  ELSE NULL END,
			active_tab_id = excluded.active_tab_id`
	if s.postgres {
		// PostgreSQL has no SQLite-style IS comparison between values. Qualify
		// incumbent columns to distinguish them from the excluded row.
		query = `
			INSERT INTO inspector_state(project_id, workstream_bead_id, active_tab_id)
			VALUES (?, ?, ?)
			ON CONFLICT(project_id, workstream_bead_id) DO UPDATE SET
				cache_json = CASE WHEN inspector_state.active_tab_id IS NOT DISTINCT FROM excluded.active_tab_id THEN inspector_state.cache_json ELSE NULL END,
				cached_at = CASE WHEN inspector_state.active_tab_id IS NOT DISTINCT FROM excluded.active_tab_id THEN inspector_state.cached_at ELSE NULL END,
				active_tab_id = excluded.active_tab_id`
	}
	_, err := s.db.ExecContext(ctx, query, projectID, wsBeadID, tabID)
	if err != nil {
		return fmt.Errorf("store: set active inspector tab: %w", err)
	}
	return nil
}

// GetActiveTab returns the window's active tab id, or 0 when none is recorded.
func (s *Store) GetActiveTab(ctx context.Context, projectID int64, wsBeadID string) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT active_tab_id FROM inspector_state
		WHERE project_id = ? AND workstream_bead_id = ?`, projectID, wsBeadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: get active inspector tab: %w", err)
	}
	return id.Int64, nil
}

// SaveTabCache stores the rendered card JSON for the window's active tab.
func (s *Store) SaveTabCache(ctx context.Context, projectID int64, wsBeadID string, cacheJSON string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO inspector_state(project_id, workstream_bead_id, cache_json, cached_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, workstream_bead_id) DO UPDATE SET
			cache_json = excluded.cache_json, cached_at = excluded.cached_at`,
		projectID, wsBeadID, cacheJSON, at.Unix())
	if err != nil {
		return fmt.Errorf("store: save inspector cache: %w", err)
	}
	return nil
}

// GetTabCache returns the cached card JSON and when it was cached; ("", zero)
// when nothing is cached.
func (s *Store) GetTabCache(ctx context.Context, projectID int64, wsBeadID string) (string, time.Time, error) {
	var js sql.NullString
	var at sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT cache_json, cached_at FROM inspector_state
		WHERE project_id = ? AND workstream_bead_id = ?`, projectID, wsBeadID).Scan(&js, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("store: get inspector cache: %w", err)
	}
	if !js.Valid {
		return "", time.Time{}, nil
	}
	return js.String, time.Unix(at.Int64, 0), nil
}
