package api

import (
	"net/http"
	"time"

	"github.com/example-user/commander/internal/store"
)

// usageAgentsTop is how many agents each account lists.
const usageAgentsTop = 8

// AccountTokens is one account's token use: today, the last 7 days, and the
// agents that used the most of it this week, each with its model.
type AccountTokens struct {
	Key         string             `json:"key"` // claude:<account> or codex, as UsageKey
	TodayTokens int64              `json:"today_tokens"`
	WeekTokens  int64              `json:"week_tokens"`
	WeekUSD     float64            `json:"week_usd"`
	Agents      []store.AgentUsage `json:"agents"`
}

// serveUsageAgents answers which agents and models used each account's tokens.
func (s *Server) serveUsageAgents(w http.ResponseWriter, r *http.Request) {
	if s.opts.UsageByAgent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "usage not available"})
		return
	}
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
	rows, err := s.opts.UsageByAgent(r.Context(), today, now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": accountTokens(rows, usageAgentsTop)})
}

// accountTokens groups rows (largest first) by account, keeping each account's
// top n agents.
func accountTokens(rows []store.AgentUsage, n int) []AccountTokens {
	byKey := map[string]*AccountTokens{}
	var order []string
	for _, row := range rows {
		key := "codex"
		if row.Runtime != "codex" {
			key = "claude:" + row.Account
		}
		a, ok := byKey[key]
		if !ok {
			a = &AccountTokens{Key: key, Agents: []store.AgentUsage{}}
			byKey[key] = a
			order = append(order, key)
		}
		a.TodayTokens += row.TodayTokens
		a.WeekTokens += row.WeekTokens
		a.WeekUSD += row.WeekUSD
		if len(a.Agents) < n {
			a.Agents = append(a.Agents, row)
		}
	}
	out := make([]AccountTokens, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}
