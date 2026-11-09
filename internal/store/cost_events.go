package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CostEvent records a discrete telemetry point — operator spawn, watchdog
// tick, kill, ceiling-hit, orphan-reaped, etc. Aggregated for the Lead view's
// project metrics + the Commander view's burn-rate sparkline.
type CostEvent struct {
	ID          int64
	ProjectID   int64
	TaskBeadID  sql.NullString
	AgentName   string
	EventType   string // 'spawn' | 'tick' | 'kill' | 'ceiling-hit' | 'orphan-reaped' ...
	Reason      sql.NullString
	DeltaUSD    float64
	DeltaTokens int64
	OccurredAt  int64
}

// Event types settle records when an Operator's process ends.
const (
	EventOperatorExited = "operator-exited" // reason: first line of its final report
	EventOperatorDied   = "operator-died"   // reason: why
	// EventOperatorRuntime is written at spawn (reason: claude|codex) and
	// EventOperatorShipped when `cmdr internal-ship` merged the run's branch.
	EventOperatorRuntime = "operator-runtime"
	EventOperatorShipped = "operator-shipped"
)

func (s *Store) InsertCostEvent(ctx context.Context, e CostEvent) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO cost_events(
			project_id, task_bead_id, agent_name, event_type, reason,
			delta_usd, delta_tokens, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ProjectID, e.TaskBeadID, e.AgentName, e.EventType, e.Reason,
		e.DeltaUSD, e.DeltaTokens, e.OccurredAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert cost_event: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) GetCostEvent(ctx context.Context, id int64) (*CostEvent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, event_type, reason,
		       delta_usd, delta_tokens, occurred_at
		FROM cost_events WHERE id = ?
	`, id)
	var e CostEvent
	err := row.Scan(&e.ID, &e.ProjectID, &e.TaskBeadID, &e.AgentName, &e.EventType,
		&e.Reason, &e.DeltaUSD, &e.DeltaTokens, &e.OccurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan cost_event: %w", err)
	}
	return &e, nil
}

// ListCostEventsByProject returns events for a project, newest first.
// limit <= 0 means no limit.
func (s *Store) ListCostEventsByProject(ctx context.Context, projectID int64, limit int) ([]CostEvent, error) {
	q := `
		SELECT id, project_id, task_bead_id, agent_name, event_type, reason,
		       delta_usd, delta_tokens, occurred_at
		FROM cost_events WHERE project_id = ? ORDER BY occurred_at DESC, id DESC
	`
	args := []any{projectID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list cost_events: %w", err)
	}
	defer rows.Close()
	var out []CostEvent
	for rows.Next() {
		var e CostEvent
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.TaskBeadID, &e.AgentName, &e.EventType,
			&e.Reason, &e.DeltaUSD, &e.DeltaTokens, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SumUSDByWindow returns total delta_usd for events occurring in the last
// `hours` hours, anchored at `now` (unix seconds). Used by the Commander view
// to render rolling burn-rate. Pass hours=24 for the last day, etc.
func (s *Store) SumUSDByWindow(ctx context.Context, now int64, hours int) (float64, error) {
	if hours <= 0 {
		return 0, fmt.Errorf("store: SumUSDByWindow: hours must be > 0")
	}
	since := now - int64(hours)*3600
	var sum sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(delta_usd), 0)
		FROM cost_events WHERE occurred_at >= ?
	`, since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("store: sum usd: %w", err)
	}
	return sum.Float64, nil
}

// SumUSDByProjectWindow returns total delta_usd for events scoped to a
// project within the last `hours` hours, anchored at `now` (unix seconds).
// Mirrors SumUSDByWindow but project-filtered for the Lead view's per-project
// burn-rate card. Pass hours=24 for the last day, etc.
func (s *Store) SumUSDByProjectWindow(ctx context.Context, now int64, projectID int64, hours int) (float64, error) {
	if hours <= 0 {
		return 0, fmt.Errorf("store: SumUSDByProjectWindow: hours must be > 0")
	}
	since := now - int64(hours)*3600
	var sum sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(delta_usd), 0)
		FROM cost_events WHERE project_id = ? AND occurred_at >= ?
	`, projectID, since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("store: sum usd by project: %w", err)
	}
	return sum.Float64, nil
}

// SumUSDByHour returns total delta_usd per hour for the last `hours` hours,
// oldest bucket first and the current hour last (always `hours` entries), for
// the Fleet sparkline.
func (s *Store) SumUSDByHour(ctx context.Context, now int64, hours int) ([]float64, error) {
	if hours <= 0 {
		return nil, fmt.Errorf("store: SumUSDByHour: hours must be > 0")
	}
	since := now - int64(hours)*3600
	rows, err := s.db.QueryContext(ctx, `
		SELECT (? - occurred_at) / 3600 AS ago, COALESCE(SUM(delta_usd), 0)
		FROM cost_events WHERE occurred_at >= ? AND occurred_at <= ?
		GROUP BY ago
	`, now, since, now)
	if err != nil {
		return nil, fmt.Errorf("store: sum usd by hour: %w", err)
	}
	defer rows.Close()
	out := make([]float64, hours)
	for rows.Next() {
		var ago int
		var usd float64
		if err := rows.Scan(&ago, &usd); err != nil {
			return nil, err
		}
		if ago >= 0 && ago < hours {
			out[hours-1-ago] = usd
		}
	}
	return out, rows.Err()
}

// ListCostEventsSince returns events of the given types that happened at or
// after since (unix s), oldest first.
func (s *Store) ListCostEventsSince(ctx context.Context, since int64, types ...string) ([]CostEvent, error) {
	if len(types) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")
	args := []any{since}
	for _, t := range types {
		args = append(args, t)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, task_bead_id, agent_name, event_type, reason,
		       delta_usd, delta_tokens, occurred_at
		FROM cost_events WHERE occurred_at >= ? AND event_type IN (`+marks+`)
		ORDER BY occurred_at, id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list cost_events since: %w", err)
	}
	defer rows.Close()
	var out []CostEvent
	for rows.Next() {
		var e CostEvent
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.TaskBeadID, &e.AgentName, &e.EventType,
			&e.Reason, &e.DeltaUSD, &e.DeltaTokens, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("store: scan cost_event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
