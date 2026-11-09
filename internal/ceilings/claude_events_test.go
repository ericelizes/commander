package ceilings

import "testing"

func TestConsumer_AccumulatesClaudeStreamUsage(t *testing.T) {
	fs := newFakeStore()
	w, src := newTestWatchdog(t, fs, DefaultLimits())

	r := fakeRun(10)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","session_id":"s","message":{"id":"msg_1","usage":{"input_tokens":1000,"output_tokens":500}}}`,
		// Same message repeated for a second content block: billed once.
		`{"type":"assistant","session_id":"s","message":{"id":"msg_1","usage":{"input_tokens":1000,"output_tokens":500}}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0.30}`,
	}, true))

	w.Register(r, "myproj", "/abs/myproj")

	waitFor(t, func() bool {
		return fs.callsCount("UpdateUsage") >= 2
	}, "UpdateUsage called for the assistant and result events")

	tokens, usd, ok := w.RunUsage(r.ID)
	if !ok {
		t.Fatalf("expected run still registered")
	}
	if tokens != 1500 {
		t.Errorf("tokens: want 1500 got %d", tokens)
	}
	if usd < 0.299 || usd > 0.301 {
		t.Errorf("usd: want ~0.30 got %f", usd)
	}
}

// Codex (through the bridge) reports $0 and no per-message usage: its result
// lines' usage is what counts, and the token ceiling stops the run.
func TestConsumer_CodexRunCappedOnTokens(t *testing.T) {
	fs := newFakeStore()
	limits := DefaultLimits()
	limits.MaxTokensPerOperator = 10_000
	w, src := newTestWatchdog(t, fs, limits)

	r := fakeRun(11)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{"type":"system","subtype":"init","session_id":"s","model":"codex:gpt-5.5"}`,
		`{"type":"assistant","session_id":"s","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0,"usage":{"input_tokens":3000,"cache_read_input_tokens":27520,"output_tokens":81}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0,"usage":{"input_tokens":9000,"cache_read_input_tokens":40000,"output_tokens":500}}`,
	}, true))

	w.Register(r, "myproj", "/abs/myproj")

	waitFor(t, func() bool { return fs.callsContains("MarkClosed:killed-ceiling") }, "token ceiling kills the Codex run")
	if got := fs.costEvents; len(got) != 1 {
		t.Fatalf("cost events: %v", got)
	}
}

// Claude's result also carries usage, but its assistant messages were already
// counted: the result adds no tokens.
func TestConsumer_ClaudeResultUsageNotCountedTwice(t *testing.T) {
	fs := newFakeStore()
	w, src := newTestWatchdog(t, fs, DefaultLimits())

	r := fakeRun(12)
	src.installReader(r.ID, newFakeLineReader([]string{
		`{"type":"assistant","session_id":"s","message":{"id":"msg_1","usage":{"input_tokens":1000,"output_tokens":500}}}`,
		`{"type":"result","subtype":"success","session_id":"s","is_error":false,"total_cost_usd":0.30,"usage":{"input_tokens":1000,"output_tokens":500}}`,
	}, true))

	w.Register(r, "myproj", "/abs/myproj")
	waitFor(t, func() bool { return fs.callsCount("UpdateUsage") >= 2 }, "both events applied")
	if tokens, _, _ := w.RunUsage(r.ID); tokens != 1500 {
		t.Errorf("tokens: want 1500 got %d", tokens)
	}
}
