package store

import (
	"context"
	"fmt"
)

// BoardState is when a board row began showing a state.
type BoardState struct {
	State string // kind|text
	Since int64  // unix s
}

// BoardSince returns every board row's state and when it began, by row key.
func (s *Store) BoardSince(ctx context.Context) (map[string]BoardState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, state, since FROM board_since`)
	if err != nil {
		return nil, fmt.Errorf("board since: %w", err)
	}
	defer rows.Close()
	out := map[string]BoardState{}
	for rows.Next() {
		var k string
		var b BoardState
		if err := rows.Scan(&k, &b.State, &b.Since); err != nil {
			return nil, fmt.Errorf("board since: %w", err)
		}
		out[k] = b
	}
	return out, rows.Err()
}

// SetBoardSince records row key's state and when it began; an empty state drops the row.
func (s *Store) SetBoardSince(ctx context.Context, key string, b BoardState) error {
	var err error
	if b.State == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM board_since WHERE key = ?`, key)
	} else {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO board_since(key, state, since) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET state = excluded.state, since = excluded.since`, key, b.State, b.Since)
	}
	if err != nil {
		return fmt.Errorf("set board since %s: %w", key, err)
	}
	return nil
}
