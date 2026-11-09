package tokenledger

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

func byRole(t *testing.T, st *store.Store) map[string]store.AgentUsage {
	t.Helper()
	rows, err := st.UsageByAgent(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.AgentUsage{}
	for _, r := range rows {
		out[r.Role] = r
	}
	return out
}

func TestScanCountsEveryAgentByAccountAndModel(t *testing.T) {
	ctx := context.Background()
	home, proj := t.TempDir(), t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pid, _ := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: proj, ActivatedAt: 1, Status: "active"})
	runID, _ := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "p-1", AgentName: "operator-op-a", OpencodeSessionID: "s", SpawnTime: time.Now().Unix()})

	now := time.Now().Format(time.RFC3339)
	// A Claude Manager: usage is each turn's own; model_usage is cumulative and
	// restarts from an old total when the process resumes (seq 5), so it must not count.
	write(t, filepath.Join(home, "chat", "manager-x.jsonl"),
		`{"seq":1,"session_id":"s1","kind":"session","time":"`+now+`","session":{"model":"sonnet"}}`,
		`{"seq":2,"session_id":"s1","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0.1,"usage":{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50},"model_usage":{"claude-sonnet-5-5":{"inputTokens":10,"outputTokens":100,"cacheReadInputTokens":1000,"cacheCreationInputTokens":50,"costUSD":0.1}}}}`,
		`{"seq":3,"session_id":"s1","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0.2,"usage":{"input_tokens":10,"output_tokens":200,"cache_read_input_tokens":2000},"model_usage":{"claude-sonnet-5-5":{"inputTokens":20,"outputTokens":300,"cacheReadInputTokens":3000,"cacheCreationInputTokens":50,"costUSD":0.3}}}}`,
		`{"seq":4,"session_id":"s1","kind":"session","time":"`+now+`","session":{"model":"claude-sonnet-5-5"}}`,
		`{"seq":5,"session_id":"s1","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0.01,"usage":{"input_tokens":5,"output_tokens":5},"model_usage":{"claude-sonnet-5-5":{"inputTokens":900000,"outputTokens":5,"costUSD":9}}}}`,
	)
	// A Codex Manager: each turn reports its own usage, $0.
	write(t, filepath.Join(home, "chat", "manager-y.jsonl"),
		`{"seq":1,"session_id":"s2","kind":"session","time":"`+now+`","session":{"model":"codex:gpt-6-astra"}}`,
		`{"seq":2,"session_id":"s2","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0,"usage":{"input_tokens":5000,"cache_read_input_tokens":100000,"output_tokens":700}}}`,
		`{"seq":3,"session_id":"s2","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0,"usage":{"input_tokens":1000,"cache_read_input_tokens":200000,"output_tokens":300}}}`,
	)
	ops := filepath.Join(proj, ".commander", "operators")
	write(t, filepath.Join(ops, "op-a.fifo"),
		`{"type":"system","subtype":"init","model":"claude-sonnet-5-5"}`,
		`{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":1},"modelUsage":{"claude-sonnet-5-5":{"inputTokens":14,"outputTokens":1450,"cacheReadInputTokens":328832,"cacheCreationInputTokens":37657}}}`,
	)
	write(t, filepath.Join(ops, "op-b.fifo"),
		`{"type":"system","subtype":"init","model":"codex:gpt-6.1-sol"}`,
		`{"type":"result","total_cost_usd":0,"usage":{"cache_read_input_tokens":1531520,"input_tokens":85983,"output_tokens":12624}}`,
	)
	// An old run's real FIFO: never opened (it would block).
	if err := syscall.Mkfifo(filepath.Join(ops, "op-old.fifo"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := Ledger{Store: st, Home: home, Projects: func(context.Context) ([]string, error) { return []string{proj}, nil },
		Account: func(role string) string { return map[string]string{"manager-x": "work"}[role] }}
	done := make(chan error, 1)
	go func() { done <- l.Scan(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan hung (did it open a FIFO?)")
	}

	got := byRole(t, st)
	if x := got["manager-x"]; x.Account != "work" || x.Runtime != "claude" || x.Model != "claude-sonnet-5-5" || x.WeekTokens != 3380 || x.WeekTurns != 3 || x.WeekUSD < 0.309 || x.WeekUSD > 0.311 {
		t.Errorf("claude manager = %+v", x)
	}
	if y := got["manager-y"]; y.Account != "codex" || y.Runtime != "codex" || y.Model != "gpt-6-astra" || y.WeekTokens != 307000 || y.WeekTurns != 2 {
		t.Errorf("codex manager = %+v", y)
	}
	if a := got["operator-op-a"]; a.Account != "default" || a.Model != "claude-sonnet-5-5" || a.WeekTokens != 367953 {
		t.Errorf("claude operator = %+v", a)
	}
	if b := got["operator-op-b"]; b.Account != "codex" || b.Model != "gpt-6.1-sol" || b.WeekTokens != 1630127 {
		t.Errorf("codex operator = %+v", b)
	}
	run, _ := st.GetAgentRun(ctx, runID)
	if run.TokensUsed != 367953 || run.UsdUsed < 0.499 {
		t.Errorf("run usage = %d tokens $%f", run.TokensUsed, run.UsdUsed)
	}

	// A second scan counts only what was added.
	write(t, filepath.Join(home, "chat", "manager-y.jsonl"),
		`{"seq":4,"session_id":"s2","kind":"turn_result","time":"`+now+`","result":{"cost_delta_usd":0,"usage":{"input_tokens":1,"output_tokens":1}}}`)
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got = byRole(t, st)
	if y := got["manager-y"]; y.WeekTokens != 307002 || y.WeekTurns != 3 {
		t.Errorf("after rescan, codex manager = %+v", y)
	}
	if x := got["manager-x"]; x.WeekTurns != 3 {
		t.Errorf("rescan recounted: %+v", x)
	}
}
