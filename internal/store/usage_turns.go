package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UsageTurn is one finished turn of an agent, with the tokens it used.
type UsageTurn struct {
	TurnKey      string
	Source       string // chat | operator
	Role         string
	Runtime      string // claude | codex
	Account      string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheCreate  int64
	USD          float64
	OccurredAt   int64
}

// InsertUsageTurn records a turn; one already recorded (same key) is ignored.
func (s *Store) InsertUsageTurn(ctx context.Context, u UsageTurn) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_turns(turn_key, source, role, runtime, account, model,
			input_tokens, output_tokens, cache_read, cache_create, usd, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING
	`, u.TurnKey, u.Source, u.Role, u.Runtime, u.Account, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheCreate, u.USD, u.OccurredAt)
	if err != nil {
		return fmt.Errorf("store: insert usage turn: %w", err)
	}
	return nil
}

// AgentUsage is one agent's token use on one account and model.
type AgentUsage struct {
	Account     string  `json:"account"`
	Runtime     string  `json:"runtime"`
	Role        string  `json:"role"`
	Model       string  `json:"model"`
	TodayTokens int64   `json:"today_tokens"` // input + output + cache read + cache write
	WeekTokens  int64   `json:"week_tokens"`
	WeekUSD     float64 `json:"week_usd"`
	WeekTurns   int64   `json:"week_turns"`
}

// UsageByAgent sums token use per account, agent and model since weekSince,
// with the part at or after todaySince (both unix s), largest first.
func (s *Store) UsageByAgent(ctx context.Context, todaySince, weekSince int64) ([]AgentUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account, runtime, role, model,
		       COALESCE(SUM(CASE WHEN occurred_at >= ? THEN input_tokens + output_tokens + cache_read + cache_create END), 0),
		       COALESCE(SUM(input_tokens + output_tokens + cache_read + cache_create), 0),
		       COALESCE(SUM(usd), 0), COUNT(*)
		FROM usage_turns WHERE occurred_at >= ?
		GROUP BY account, runtime, role, model
		ORDER BY 6 DESC
	`, todaySince, weekSince)
	if err != nil {
		return nil, fmt.Errorf("store: usage by agent: %w", err)
	}
	defer rows.Close()
	var out []AgentUsage
	for rows.Next() {
		var a AgentUsage
		if err := rows.Scan(&a.Account, &a.Runtime, &a.Role, &a.Model, &a.TodayTokens, &a.WeekTokens, &a.WeekUSD, &a.WeekTurns); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UsageScanState is how far a log has been read and the reader's carried state.
func (s *Store) UsageScanState(ctx context.Context, path string) (offset int64, state string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT "offset", state FROM usage_scan WHERE path = ?`, path).Scan(&offset, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("store: usage scan state: %w", err)
	}
	return offset, state, nil
}

// SetUsageScanState records how far a log has been read.
func (s *Store) SetUsageScanState(ctx context.Context, path string, offset int64, state string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_scan(path, "offset", state) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET "offset" = excluded."offset", state = excluded.state
	`, path, offset, state)
	if err != nil {
		return fmt.Errorf("store: set usage scan state: %w", err)
	}
	return nil
}

// SetLatestRunUsage records an Operator's model and token totals on its
// newest run (by agent name).
func (s *Store) SetLatestRunUsage(ctx context.Context, agentName, model string, tokens int64, usd float64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET model = ?, tokens_used = ?, usd_used = ?
		WHERE id = (SELECT id FROM agent_runs WHERE agent_name = ? ORDER BY spawn_time DESC, id DESC LIMIT 1)
	`, model, tokens, usd, agentName)
	if err != nil {
		return fmt.Errorf("store: set run usage %s: %w", agentName, err)
	}
	return nil
}
