package store

import (
	"context"
	"fmt"
)

// ListRunningRuns returns every agent_run with status = 'running'. Used by the
// watchdog at start time to re-attach event-stream consumers to operators
// that survived a commander restart (and by the orphan reaper to cross-check
// docker state against the database).
func (s *Store) ListRunningRuns(ctx context.Context) ([]AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE status = 'running' ORDER BY spawn_time
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list running runs: %w", err)
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

// ListFinishedRuns returns every agent_run no longer running, oldest first.
func (s *Store) ListFinishedRuns(ctx context.Context) ([]AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE status != 'running' ORDER BY spawn_time
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list finished runs: %w", err)
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

// CountRunsByProject counts runs for one project filtered by status. status=""
// counts all rows for the project.
func (s *Store) CountRunsByProject(ctx context.Context, projectID int64, status string) (int, error) {
	q := `SELECT COUNT(*) FROM agent_runs WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count runs by project: %w", err)
	}
	return n, nil
}

// GetAgentRunByTaskBeadID returns the running operator targeting taskBeadID,
// or (nil, nil) when none exists. Used by the Operator view's live-data
// loader to find the run id behind a Task drill-in. Multiple operators
// could in principle target the same Task — we return the first running
// match to keep the Operator view focused on the active worker.
func (s *Store) GetAgentRunByTaskBeadID(ctx context.Context, taskBeadID string) (*AgentRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs
		WHERE task_bead_id = ? AND status = 'running'
		ORDER BY spawn_time DESC
		LIMIT 1
	`, taskBeadID)
	return scanAgentRun(row)
}

// ListAgentRunsByTaskBeadID returns runs targeting taskBeadID. status=""
// returns all matching rows; non-empty filters by that status.
func (s *Store) ListAgentRunsByTaskBeadID(ctx context.Context, taskBeadID, status string) ([]AgentRun, error) {
	q := `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs
		WHERE task_bead_id = ?`
	args := []any{taskBeadID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY spawn_time DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list agent_runs by task bead id: %w", err)
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

// CountAllRuns counts agent_runs filtered by status across every project.
// status="" counts every row.
func (s *Store) CountAllRuns(ctx context.Context, status string) (int, error) {
	q := `SELECT COUNT(*) FROM agent_runs`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count all runs: %w", err)
	}
	return n, nil
}

// GetAgentRunByAgentName returns the newest run of the Operator with this mail
// role (operator-<slug>), or (nil, nil) when it has none. Bead ids carry dots
// (t01.17.10) but Operators derive their role from the branch or tmux name,
// where dots became dashes, so an exact miss retries with dots and dashes
// treated alike.
func (s *Store) GetAgentRunByAgentName(ctx context.Context, agentName string) (*AgentRun, error) {
	const cols = `SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs `
	run, err := scanAgentRun(s.db.QueryRowContext(ctx, cols+`
		WHERE agent_name = ?
		ORDER BY spawn_time DESC
		LIMIT 1
	`, agentName))
	if run != nil || err != nil {
		return run, err
	}
	return scanAgentRun(s.db.QueryRowContext(ctx, cols+`
		WHERE REPLACE(agent_name, '.', '-') = REPLACE(?, '.', '-')
		ORDER BY spawn_time DESC
		LIMIT 1
	`, agentName))
}

// ListRunsSince returns every agent_run spawned at or after since (unix s),
// oldest first.
func (s *Store) ListRunsSince(ctx context.Context, since int64) ([]AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, opencode_session_id,
		       container_id, tmux_pane_id, spawn_time,
		       tokens_used, usd_used, consecutive_failures, status, worktree_path
		FROM agent_runs WHERE spawn_time >= ? ORDER BY spawn_time, id
	`, since)
	if err != nil {
		return nil, fmt.Errorf("store: list runs since: %w", err)
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
