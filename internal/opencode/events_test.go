package opencode

import (
	"strings"
	"testing"
)

// verifiedStepFinish is a synthetic nd-JSON example from share/contracts.md
// (the shape was verified against the CLI). Any drift here means the contract changed and
// the wrapper must be updated to match.
const verifiedStepFinish = `{"type":"step_finish","timestamp":1767225600000,"sessionID":"ses_sample_1","part":{"id":"prt_sample_2","type":"step-finish","reason":"stop","snapshot":"0000000000000000000000000000000000000000","messageID":"msg_sample_3","sessionID":"ses_sample_1","cost":0,"tokens":{"total":125,"input":100,"output":3,"reasoning":12,"cache":{"read":0,"write":0}}}}`

func TestParseEvent_VerifiedStepFinish(t *testing.T) {
	ev, err := ParseEvent([]byte(verifiedStepFinish))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if ev.Type != "step_finish" {
		t.Errorf("Type = %q, want step_finish", ev.Type)
	}
	if ev.Timestamp != 1767225600000 {
		t.Errorf("Timestamp = %d, want 1767225600000", ev.Timestamp)
	}
	if ev.SessionID != "ses_sample_1" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}

	if ev.Part.ID != "prt_sample_2" {
		t.Errorf("Part.ID = %q", ev.Part.ID)
	}
	if ev.Part.Type != "step-finish" {
		t.Errorf("Part.Type = %q", ev.Part.Type)
	}
	if ev.Part.Reason != "stop" {
		t.Errorf("Part.Reason = %q", ev.Part.Reason)
	}
	if ev.Part.MessageID != "msg_sample_3" {
		t.Errorf("Part.MessageID = %q", ev.Part.MessageID)
	}
	if ev.Part.SessionID != "ses_sample_1" {
		t.Errorf("Part.SessionID = %q", ev.Part.SessionID)
	}
	if ev.Part.Cost != 0 {
		t.Errorf("Part.Cost = %v, want 0 (cost: 0 is valid, not missing)", ev.Part.Cost)
	}

	if ev.Part.Tokens.Total != 125 {
		t.Errorf("Tokens.Total = %d, want 125", ev.Part.Tokens.Total)
	}
	if ev.Part.Tokens.Input != 100 {
		t.Errorf("Tokens.Input = %d, want 100", ev.Part.Tokens.Input)
	}
	if ev.Part.Tokens.Output != 3 {
		t.Errorf("Tokens.Output = %d, want 3", ev.Part.Tokens.Output)
	}
	if ev.Part.Tokens.Reasoning != 12 {
		t.Errorf("Tokens.Reasoning = %d, want 12", ev.Part.Tokens.Reasoning)
	}
	if ev.Part.Tokens.Cache.Read != 0 || ev.Part.Tokens.Cache.Write != 0 {
		t.Errorf("Tokens.Cache = %+v, want zero", ev.Part.Tokens.Cache)
	}
}

func TestParseEvent_Malformed(t *testing.T) {
	cases := []string{
		`{not json`,
		``,
		`["array","not","object"]`, // not a JSON object — unmarshal into struct fails
	}
	for _, c := range cases {
		if _, err := ParseEvent([]byte(c)); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParseEvent_MissingTokens(t *testing.T) {
	// step_finish without tokens object — zero-value EventTokens is fine.
	line := `{"type":"step_finish","timestamp":1,"sessionID":"ses_sample_4","part":{"cost":0.5}}`
	ev, err := ParseEvent([]byte(line))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Part.Cost != 0.5 {
		t.Errorf("Cost = %v, want 0.5", ev.Part.Cost)
	}
	if ev.Part.Tokens != (EventTokens{}) {
		t.Errorf("Tokens should be zero-value, got %+v", ev.Part.Tokens)
	}
}

func TestParseEvent_UnknownType(t *testing.T) {
	// Unknown event type should parse cleanly — Type is populated as-is so
	// callers can no-op rather than crash on a future opencode upgrade.
	line := `{"type":"some_future_event","timestamp":42,"sessionID":"ses_sample_4","part":{}}`
	ev, err := ParseEvent([]byte(line))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Type != "some_future_event" {
		t.Errorf("Type = %q, want some_future_event", ev.Type)
	}
}

func TestParseEvent_TextEvent(t *testing.T) {
	line := `{"type":"text","timestamp":1,"sessionID":"ses_sample_4","part":{"text":"hello world"}}`
	ev, err := ParseEvent([]byte(line))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Type != "text" {
		t.Errorf("Type = %q", ev.Type)
	}
	if !strings.Contains(ev.Part.Text, "hello world") {
		t.Errorf("Part.Text = %q", ev.Part.Text)
	}
}
