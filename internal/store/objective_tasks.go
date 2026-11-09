package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ObjectiveTask is the join row that links an objective to a bd Task across
// projects. Composite primary key: (objective_id, project_id, task_bead_id).
type ObjectiveTask struct {
	ObjectiveID           int64
	ProjectID             int64
	TaskBeadID            string
	IsDerived             bool
	DerivedFromTaskBeadID sql.NullString
	CreatedAt             int64
}

// InsertObjectiveTask inserts a join row. Idempotent: if the composite key
// already exists, this is a silent no-op (INSERT OR IGNORE). Returns
// (inserted=true) when a new row was created, (false) on duplicate.
func (s *Store) InsertObjectiveTask(ctx context.Context, t ObjectiveTask) (bool, error) {
	derived := 0
	if t.IsDerived {
		derived = 1
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO objective_tasks(
			objective_id, project_id, task_bead_id,
			is_derived, derived_from_task_bead_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, t.ObjectiveID, t.ProjectID, t.TaskBeadID, derived, t.DerivedFromTaskBeadID, t.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("store: insert objective_task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetObjectiveTask returns (nil, nil) if absent.
func (s *Store) GetObjectiveTask(ctx context.Context, objectiveID, projectID int64, taskBeadID string) (*ObjectiveTask, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT objective_id, project_id, task_bead_id, is_derived, derived_from_task_bead_id, created_at
		FROM objective_tasks
		WHERE objective_id = ? AND project_id = ? AND task_bead_id = ?
	`, objectiveID, projectID, taskBeadID)
	var t ObjectiveTask
	var derived int
	err := row.Scan(&t.ObjectiveID, &t.ProjectID, &t.TaskBeadID, &derived, &t.DerivedFromTaskBeadID, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan objective_task: %w", err)
	}
	t.IsDerived = derived != 0
	return &t, nil
}

// ListByObjective returns all join rows for the given objective.
func (s *Store) ListObjectiveTasksByObjective(ctx context.Context, objectiveID int64) ([]ObjectiveTask, error) {
	return queryObjectiveTasks(ctx, s.db, `
		SELECT objective_id, project_id, task_bead_id, is_derived, derived_from_task_bead_id, created_at
		FROM objective_tasks WHERE objective_id = ? ORDER BY created_at
	`, objectiveID)
}

// ListByProject returns join rows for every objective whose Task lives in
// project_id. Useful for the Lead view's project drill-down.
func (s *Store) ListObjectiveTasksByProject(ctx context.Context, projectID int64) ([]ObjectiveTask, error) {
	return queryObjectiveTasks(ctx, s.db, `
		SELECT objective_id, project_id, task_bead_id, is_derived, derived_from_task_bead_id, created_at
		FROM objective_tasks WHERE project_id = ? ORDER BY created_at
	`, projectID)
}

// ListObjectiveTasksByTaskBeadID returns every objective-task join row pointing
// at the given task bead id across all projects. Used by CLI task routing when
// no running operator row exists for the task.
func (s *Store) ListObjectiveTasksByTaskBeadID(ctx context.Context, taskBeadID string) ([]ObjectiveTask, error) {
	return queryObjectiveTasks(ctx, s.db, `
		SELECT objective_id, project_id, task_bead_id, is_derived, derived_from_task_bead_id, created_at
		FROM objective_tasks WHERE task_bead_id = ? ORDER BY created_at
	`, taskBeadID)
}

// CountTasksForObjective returns the number of join rows pointing at the
// given objective_id. Used by the Commander view to label each objective row
// with its linked-task count without round-tripping a full ListByObjective.
func (s *Store) CountTasksForObjective(ctx context.Context, objectiveID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM objective_tasks WHERE objective_id = ?
	`, objectiveID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count tasks for objective: %w", err)
	}
	return n, nil
}

func queryObjectiveTasks(ctx context.Context, q Querier, sqlStr string, args ...any) ([]ObjectiveTask, error) {
	rows, err := q.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query objective_tasks: %w", err)
	}
	defer rows.Close()
	var out []ObjectiveTask
	for rows.Next() {
		var t ObjectiveTask
		var derived int
		if err := rows.Scan(&t.ObjectiveID, &t.ProjectID, &t.TaskBeadID, &derived, &t.DerivedFromTaskBeadID, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.IsDerived = derived != 0
		out = append(out, t)
	}
	return out, rows.Err()
}
