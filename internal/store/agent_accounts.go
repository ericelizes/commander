package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Agent kinds with their own account order.
const (
	AgentKindCommander = "commander"
	AgentKindAgent     = "agent"
)

// DefaultAgentAccounts is the account order of a kind with nothing saved:
// Commander on any Claude account, Codex as its fallback; execution agents on
// Codex, Claude as their fallback.
var DefaultAgentAccounts = map[string][]string{
	AgentKindCommander: {"claude", "codex"},
	AgentKindAgent:     {"codex", "claude"},
}

// AgentAccounts is the account order of every kind: the first account with
// usage left is used, followed by any other connected account (d82). An entry is a provider ("claude" = any Claude account,
// "codex") or one account's name. A kind with nothing saved gets its default.
func (s *Store) AgentAccounts(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	for kind, order := range DefaultAgentAccounts {
		out[kind] = append([]string{}, order...)
	}
	if !s.postgres {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,accounts FROM agent_accounts`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, raw string
		if err := rows.Scan(&kind, &raw); err != nil {
			return out, err
		}
		var order []string
		if err := json.Unmarshal([]byte(raw), &order); err != nil {
			return out, err
		}
		out[kind] = order
	}
	return out, rows.Err()
}

// SetAgentAccounts saves kind's account order.
func (s *Store) SetAgentAccounts(ctx context.Context, kind string, order []string) error {
	if !s.postgres {
		return errors.New("agent accounts require PostgreSQL")
	}
	if _, ok := DefaultAgentAccounts[kind]; !ok {
		return fmt.Errorf("unknown agent kind %q (commander or agent)", kind)
	}
	clean := []string{}
	seen := map[string]bool{}
	for _, entry := range order {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		clean = append(clean, entry)
	}
	if len(clean) == 0 {
		return errors.New("an agent kind needs at least one account")
	}
	raw, _ := json.Marshal(clean)
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_accounts(kind,accounts,updated_at) VALUES(?,?::jsonb,?) ON CONFLICT(kind) DO UPDATE SET accounts=excluded.accounts,updated_at=excluded.updated_at`, kind, string(raw), time.Now().Unix())
	return err
}
