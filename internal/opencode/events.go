// Package opencode wraps the opencode CLI as an in-process Go client.
//
// The contract verified in share/contracts.md is the single source of truth
// for every flag, event shape, and lifecycle invariant referenced here.
//
// Two facts dominate the design:
//
//  1. Sessions are captured-then-resumed, never pre-minted. The first
//     invocation runs without --session and the wrapper captures the
//     server-issued sessionID from the first nd-JSON event that carries one.
//     Subsequent invocations pass --session <captured>.
//  2. opencode run --format json emits one JSON object per line on stdout.
//     The Run method streams these events on a buffered channel so multiple
//     callers (watchdog ceiling math, operator chat tail, persisted log)
//     can each consume the same stream by tee-ing.
package opencode

import (
	"encoding/json"
	"fmt"
)

// Event is a single nd-JSON object emitted by `opencode run --format json`.
// Schema verified live 2026-05-08; see share/contracts.md.
//
// Known Type values: step_start, text, tool_use, tool_result, step_finish, error.
// Unknown types still parse successfully — Type is populated as-is so callers
// can no-op on them without crashing on a future opencode upgrade.
type Event struct {
	Type      string    `json:"type"`
	Timestamp int64     `json:"timestamp"`
	SessionID string    `json:"sessionID"`
	Part      EventPart `json:"part"`
}

// EventPart is the per-event payload. Most fields are only set for some
// event types (e.g. Cost/Tokens are only meaningful on step_finish, Text
// only on text events). Unset fields are zero-value.
type EventPart struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Reason    string `json:"reason,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Text      string `json:"text,omitempty"`
	// Cost is direct USD for the step. cost: 0 IS valid (cached/free model
	// paths emit zero — never treat as missing).
	Cost   float64     `json:"cost,omitempty"`
	Tokens EventTokens `json:"tokens,omitempty"`
}

// EventTokens is the token-count breakdown on step_finish events. The watchdog
// (U10) uses Input + Output for billing math; the precomputed Total may differ
// because it counts cache and reasoning tokens too.
type EventTokens struct {
	Total     int        `json:"total"`
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     EventCache `json:"cache"`
}

// EventCache is the cache-token breakdown nested in EventTokens.
type EventCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

// ParseEvent parses one nd-JSON line into an Event. Returns an error if the
// line is malformed JSON; unknown event types parse successfully (Type is
// populated as-is so callers can ignore them).
func ParseEvent(line []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, fmt.Errorf("opencode: parse event: %w", err)
	}
	return e, nil
}
