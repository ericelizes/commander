package tui

import (
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// Invented catalog Manager pane:
// the last status line starts with the same glyph as "✻ Cooked for …", but the
// Manager is waiting on a sub-agent, not idle.
const waitingPane = `● The catalog checker is working. I'm waiting for its report.

✻ Cooked for 1m 8s

✻ Waiting for 1 background agent to finish

────────────────────────────────────────────
❯ 
────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 8 agents`

func TestClassifyPane(t *testing.T) {
	cases := []struct {
		name, pane, state, detail string
	}{
		{"waiting wins over an earlier Cooked line", waitingPane, store.AgentWaiting, "1 sub-agent"},
		{"several sub-agents", "✻ Waiting for 3 background agents to finish\n❯", store.AgentWaiting, "3 sub-agents"},
		{"running turn", "✻ Brewing… (12s · esc to interrupt)\n❯", store.AgentWorking, ""},
		{"finished turn", "✻ Cooked for 2m 3s\n❯", store.AgentIdle, ""},
		{"a later Cooked line beats an older Waiting line", "✻ Waiting for 1 background agent to finish\n✻ Cooked for 5s\n❯", store.AgentIdle, ""},
		{"nothing", "❯", store.AgentIdle, ""},
	}
	for _, c := range cases {
		if s, d := classifyPane(c.pane); s != c.state || d != c.detail {
			t.Errorf("%s: got %s/%q, want %s/%q", c.name, s, d, c.state, c.detail)
		}
	}
}

func TestMergeAgentStates_FreshIdleHookDoesNotHideWaiting(t *testing.T) {
	now := time.Unix(10_000, 0)
	hooks := map[string]store.AgentState{"m": {Role: "m", State: store.AgentIdle, Updated: now.Unix() - 2}}
	scraped := map[string]store.AgentState{"m": {Role: "m", State: store.AgentWaiting, Detail: "1 sub-agent"}}
	if got := mergeAgentStates(hooks, scraped, now)["m"]; got.State != store.AgentWaiting {
		t.Errorf("got %+v", got)
	}
}

func TestAgentNow_Waiting(t *testing.T) {
	now := time.Unix(10_000, 0)
	a := animState{nowTime: now, agents: map[string]store.AgentState{
		"m": {Role: "m", State: store.AgentWaiting, Detail: "1 sub-agent", Since: now.Unix() - 90, Updated: now.Unix()},
	}}
	text, active := agentNow("m", a)
	if !active || stripANSI(text) != "◔ waiting on 1 sub-agent · 1m" {
		t.Errorf("got %v %q", active, stripANSI(text))
	}
}

// Invented catalog pane: no "Waiting for N" line and
// no busy marker, but the background-agent panel lists a running sub-agent.
const subAgentPanePane = `The catalog checks are delegated.

────────────────────────────────────────────
❯ 
────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 8 agents · ↓ to manage

  ⏺ main
  ◯ general-purpose  Checking catalog import rows                    3m 12s · ↓ 8.2k tokens
  ◯ general-purpose  Checking catalog export order                   2m 8s · ↓ 6.1k tokens`

func TestClassifyPane_RunningSubAgentPanelIsNotIdle(t *testing.T) {
	if s, d := classifyPane(subAgentPanePane); s != store.AgentWaiting || d != "2 sub-agents" {
		t.Errorf("got %s/%q", s, d)
	}
	// A finished turn with an empty panel is still idle.
	if s, _ := classifyPane("✻ Cooked for 2m\n❯\n  ⏺ main"); s != store.AgentIdle {
		t.Errorf("empty panel is idle, got %s", s)
	}
	// A running turn stays working even with sub-agents listed.
	if s, _ := classifyPane("✻ Brewing… (esc to interrupt)\n  ◯ general-purpose  Running x   3m 1s"); s != store.AgentWorking {
		t.Errorf("busy turn is working, got %s", s)
	}
}
