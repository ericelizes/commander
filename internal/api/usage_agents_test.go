package api

import (
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestAccountTokensGroupsByAccountKeepingTheTopAgents(t *testing.T) {
	rows := []store.AgentUsage{ // largest first, as UsageByAgent returns them
		{Account: "codex", Runtime: "codex", Role: "manager-a", Model: "gpt-6-astra", TodayTokens: 50, WeekTokens: 900},
		{Account: "default", Runtime: "claude", Role: "commander", Model: "claude-sonnet-5-5", TodayTokens: 10, WeekTokens: 500, WeekUSD: 2},
		{Account: "codex", Runtime: "codex", Role: "operator-b", Model: "gpt-6.1-sol", WeekTokens: 300},
		{Account: "codex", Runtime: "codex", Role: "operator-c", Model: "gpt-6.1-sol", WeekTokens: 100},
	}
	got := accountTokens(rows, 2)
	if len(got) != 2 || got[0].Key != "codex" || got[1].Key != "claude:default" {
		t.Fatalf("accounts = %+v", got)
	}
	if c := got[0]; c.WeekTokens != 1300 || c.TodayTokens != 50 || len(c.Agents) != 2 || c.Agents[1].Role != "operator-b" {
		t.Errorf("codex = %+v", c)
	}
	if d := got[1]; d.WeekTokens != 500 || d.WeekUSD != 2 || d.Agents[0].Model != "claude-sonnet-5-5" {
		t.Errorf("claude = %+v", d)
	}
}
