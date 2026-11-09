package store

import (
	"context"
	"fmt"
)

// ChatCost is one finished turn of a native chat (Commander or a Manager).
type ChatCost struct {
	Role         string
	SessionID    string
	TotalUSD     float64 // cumulative session cost after the turn
	DeltaUSD     float64
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheCreate  int64
	OccurredAt   int64
}

// RoleCost is a role's spend: since a time, this session, and all time.
type RoleCost struct {
	Role         string
	TodayUSD     float64
	SessionUSD   float64 // the session of its latest turn
	TotalUSD     float64
	InputTokens  int64
	OutputTokens int64
}

// InsertChatCost records a turn. A turn already recorded (same session and
// cumulative total, e.g. a replay or a backfill) is ignored; it reports whether
// a row was added.
func (s *Store) InsertChatCost(ctx context.Context, c ChatCost) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chat_costs(
			role, session_id, total_usd, delta_usd,
			input_tokens, output_tokens, cache_read, cache_create, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.Role, c.SessionID, c.TotalUSD, c.DeltaUSD,
		c.InputTokens, c.OutputTokens, c.CacheRead, c.CacheCreate, c.OccurredAt)
	if err != nil {
		return false, fmt.Errorf("store: insert chat_cost: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ChatCostsByRole sums every role's spend; today counts turns at or after
// since (unix s).
func (s *Store) ChatCostsByRole(ctx context.Context, since int64) ([]RoleCost, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT role,
		       COALESCE(SUM(CASE WHEN occurred_at >= ? THEN delta_usd END), 0),
		       COALESCE(SUM(delta_usd), 0),
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE((SELECT SUM(delta_usd) FROM chat_costs c2
		                 WHERE c2.role = c.role AND c2.session_id = (
		                   SELECT session_id FROM chat_costs c3 WHERE c3.role = c.role
		                   ORDER BY occurred_at DESC, id DESC LIMIT 1)), 0)
		FROM chat_costs c GROUP BY role ORDER BY role
	`, since)
	if err != nil {
		return nil, fmt.Errorf("store: chat costs by role: %w", err)
	}
	defer rows.Close()
	var out []RoleCost
	for rows.Next() {
		var r RoleCost
		if err := rows.Scan(&r.Role, &r.TodayUSD, &r.TotalUSD, &r.InputTokens, &r.OutputTokens, &r.SessionUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SumChatUSDSince is the chats' total delta_usd at or after since (unix s).
func (s *Store) SumChatUSDSince(ctx context.Context, since int64) (float64, error) {
	var sum float64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(delta_usd), 0) FROM chat_costs WHERE occurred_at >= ?`, since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("store: sum chat usd: %w", err)
	}
	return sum, nil
}

// ChatUSDByHour is the chats' spend per hour for the last hours hours, oldest
// first, like SumUSDByHour.
func (s *Store) ChatUSDByHour(ctx context.Context, now int64, hours int) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT (? - occurred_at) / 3600 AS ago, COALESCE(SUM(delta_usd), 0)
		FROM chat_costs WHERE occurred_at >= ? AND occurred_at <= ? GROUP BY ago
	`, now, now-int64(hours)*3600, now)
	if err != nil {
		return nil, fmt.Errorf("store: chat usd by hour: %w", err)
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
