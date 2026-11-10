package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AgentRun is one row per active or terminal operator. Created at spawn,
// updated by the cost watchdog as JSON events stream in (UpdateUsage), and
// closed via MarkClosed when the operator exits (cleanly or via ceiling kill).
//
// Either ContainerID (code-type) or TmuxPaneID (knowledge-type) is populated;
// the other stays NULL. The orphan reaper keys off ContainerID.
type AgentRun struct {
	ID                  int64
	ProjectID           int64
	TaskBeadID          string
	AgentName           string
	OpencodeSessionID   string
	ContainerID         sql.NullString
	TmuxPaneID          sql.NullString
	SpawnTime           int64
	TokensUsed          int64
	UsdUsed             float64
	ConsecutiveFailures int64
	Status              string         // 'running' | 'killed-ceiling' | 'closed' | 'exited'
	WorktreePath        sql.NullString // NULL = recorded before the column; see spawn.RunWorktree
}

// InsertAgentRun records a fresh operator. tokens_used and usd_used start at
// 0; status defaults to 'running'.
func (s *Store) InsertAgentRun(ctx context.Context, r AgentRun) (int64, error) {
	if r.Status == "" {
		r.Status = "running"
	}
	if s.postgres {
		return s.insertClaimedRun(ctx, r)
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runs(
			project_id, task_bead_id, agent_name, opencode_session_id,
			container_id, tmux_pane_id, spawn_time,
			tokens_used, usd_used, consecutive_failures, status, worktree_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id
	`, r.ProjectID, r.TaskBeadID, r.AgentName, r.OpencodeSessionID,
		r.ContainerID, r.TmuxPaneID, r.SpawnTime,
		r.TokensUsed, r.UsdUsed, r.ConsecutiveFailures, r.Status, r.WorktreePath).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: insert agent_run: %w", err)
	}
	return id, nil
}

// GetAgentRun returns (nil, nil) if absent.
func (s *Store) GetAgentRun(ctx context.Context, id int64) (*AgentRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE id = ?
	`, id)
	return scanAgentRun(row)
}

// GetAgentRunByContainer is the orphan-reaper lookup path.
// Returns (nil, nil) if no row matches.
func (s *Store) GetAgentRunByContainer(ctx context.Context, containerID string) (*AgentRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE container_id = ?
	`, containerID)
	return scanAgentRun(row)
}

// ListAgentRunsByProject returns runs scoped to a project. status="" returns
// all; non-empty filters by status (commonly 'running' for the Lead view).
func (s *Store) ListAgentRunsByProject(ctx context.Context, projectID int64, status string) ([]AgentRun, error) {
	q := `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	q += " ORDER BY spawn_time"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list agent_runs: %w", err)
	}
	defer rows.Close()
	var out []AgentRun
	for rows.Next() {
		r, err := scanAgentRunRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateUsage replaces the running tokens/usd totals on a run. Called by the
// cost watchdog on every step_finish event from the operator's nd-JSON stream.
func (s *Store) UpdateUsage(ctx context.Context, runID int64, tokens int64, usd float64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET tokens_used = ?, usd_used = ? WHERE id = ?
	`, tokens, usd, runID)
	if err != nil {
		return fmt.Errorf("store: update usage on agent_run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: update usage: agent_run %d not found", runID)
	}
	return nil
}

// IncrementFailures bumps consecutive_failures by 1 and returns the new value.
// The watchdog uses this to apply the failure-ceiling kill.
func (s *Store) IncrementFailures(ctx context.Context, runID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runs SET consecutive_failures = consecutive_failures + 1 WHERE id = ?
	`, runID); err != nil {
		return 0, fmt.Errorf("store: increment failures on %d: %w", runID, err)
	}
	var n int64
	if err := tx.QueryRowContext(ctx, `
		SELECT consecutive_failures FROM agent_runs WHERE id = ?
	`, runID).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("store: increment failures: agent_run %d not found", runID)
		}
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit increment: %w", err)
	}
	return n, nil
}

// MarkClosed transitions a run to a terminal status with a reason recorded
// implicitly by the caller via cost_events (we don't store reason on the run
// itself — it lives in cost_events.reason for queryability).
func (s *Store) MarkClosed(ctx context.Context, runID int64, status string) error {
	if status == "running" {
		return fmt.Errorf("store: MarkClosed: status %q is not terminal", status)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET status = ? WHERE id = ?
	`, status, runID)
	if err != nil {
		return fmt.Errorf("store: mark closed agent_run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: mark closed: agent_run %d not found", runID)
	}
	return nil
}

// CountAgentRunsByStatus returns counts of runs grouped by status. Empty map
// is returned (not nil) when zero rows match.
func (s *Store) CountAgentRunsByStatus(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM agent_runs GROUP BY status
	`)
	if err != nil {
		return nil, fmt.Errorf("store: count agent_runs: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var s string
		var n int64
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// rowScanner is the subset of *sql.Rows / *sql.Row that Scan needs. Used so
// scanAgentRun can serve both single-row and multi-row callers.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgentRun(row *sql.Row) (*AgentRun, error) {
	r, err := scanAgentRunRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan agent_run: %w", err)
	}
	return r, nil
}

func scanAgentRunRow(row rowScanner) (*AgentRun, error) {
	var r AgentRun
	err := row.Scan(
		&r.ID, &r.ProjectID, &r.TaskBeadID, &r.AgentName, &r.OpencodeSessionID,
		&r.ContainerID, &r.TmuxPaneID, &r.SpawnTime,
		&r.TokensUsed, &r.UsdUsed, &r.ConsecutiveFailures, &r.Status, &r.WorktreePath,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
