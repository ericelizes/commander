package store

import (
	"context"
	"fmt"
	"time"
)

// Agent states recorded by the Claude hooks.
const (
	AgentWorking = "working"
	AgentWaiting = "waiting" // between turns, waiting on background sub-agents (detail: "N sub-agents")
	AgentStuck   = "stuck"   // unread mail the wake loop could not deliver (input box not empty)
	AgentIdle    = "idle"
)

// AgentState is what one agent pane is doing right now.
type AgentState struct {
	Role    string
	State   string
	Detail  string
	Since   int64
	Updated int64
}

// SetAgentState records an agent's state. Since moves only when the state
// changes, so "idle 14m" keeps counting across repeated Stop events; a changed
// detail alone does not reset it.
func (s *Store) SetAgentState(ctx context.Context, role, state, detail string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_state(role, state, detail, since, updated) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(role) DO UPDATE SET
		  since = CASE WHEN agent_state.state = excluded.state THEN agent_state.since ELSE excluded.since END,
		  state = excluded.state, detail = excluded.detail, updated = excluded.updated
	`, role, state, detail, now, now)
	if err != nil {
		return fmt.Errorf("store: set agent_state %s: %w", role, err)
	}
	return nil
}

// ListAgentStates returns every recorded agent state.
func (s *Store) ListAgentStates(ctx context.Context) ([]AgentState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role, state, detail, since, updated FROM agent_state`)
	if err != nil {
		return nil, fmt.Errorf("store: list agent_state: %w", err)
	}
	defer rows.Close()
	var out []AgentState
	for rows.Next() {
		var a AgentState
		if err := rows.Scan(&a.Role, &a.State, &a.Detail, &a.Since, &a.Updated); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
