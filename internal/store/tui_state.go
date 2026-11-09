package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TUIState is a single-row table (CHECK id=1). The TUI persists the user's
// last view + selection here so cmdr resumes where the user left off.
type TUIState struct {
	CurrentView        string // 'commander' | 'lead' | 'operator'
	SelectedProjectID  sql.NullInt64
	SelectedTaskBeadID sql.NullString
	ScrollState        sql.NullString // JSON blob owned by the TUI
}

// GetTUIState returns the persisted state. If no row exists yet, returns the
// schema defaults (current_view='commander', everything else NULL) — never
// nil, never sql.ErrNoRows.
func (s *Store) GetTUIState(ctx context.Context) (*TUIState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT current_view, selected_project_id, selected_task_bead_id, scroll_state
		FROM tui_state WHERE id = 1
	`)
	var t TUIState
	err := row.Scan(&t.CurrentView, &t.SelectedProjectID, &t.SelectedTaskBeadID, &t.ScrollState)
	if errors.Is(err, sql.ErrNoRows) {
		return &TUIState{CurrentView: "commander"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan tui_state: %w", err)
	}
	return &t, nil
}

// SaveTUIState upserts the singleton row. INSERT ON CONFLICT DO UPDATE is
// the canonical pattern for a single-row table — it's race-safe under WAL
// concurrency.
func (s *Store) SaveTUIState(ctx context.Context, t TUIState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tui_state(id, current_view, selected_project_id, selected_task_bead_id, scroll_state)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			current_view          = excluded.current_view,
			selected_project_id   = excluded.selected_project_id,
			selected_task_bead_id = excluded.selected_task_bead_id,
			scroll_state          = excluded.scroll_state
	`, t.CurrentView, t.SelectedProjectID, t.SelectedTaskBeadID, t.ScrollState)
	if err != nil {
		return fmt.Errorf("store: save tui_state: %w", err)
	}
	return nil
}
