package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

func reportEvent(agent, kind, reason string, at int64) store.CostEvent {
	return store.CostEvent{AgentName: agent, EventType: kind, Reason: sql.NullString{String: reason, Valid: reason != ""}, OccurredAt: at}
}

func TestBuildRuntimeReport(t *testing.T) {
	runs := []store.AgentRun{
		{ID: 1, AgentName: "operator-a", TaskBeadID: "b-a", SpawnTime: 1000},
		{ID: 2, AgentName: "operator-a", TaskBeadID: "b-a", SpawnTime: 5000}, // respawn of a
		{ID: 3, AgentName: "operator-b", TaskBeadID: "b-b", SpawnTime: 1000},
		{ID: 4, AgentName: "operator-c", TaskBeadID: "b-c", SpawnTime: 1000, TokensUsed: 70},
		{ID: 5, AgentName: "operator-old", TaskBeadID: "b-o", SpawnTime: 900},
	}
	events := []store.CostEvent{
		reportEvent("operator-a", store.EventOperatorRuntime, "codex", 1000),
		reportEvent("operator-a", store.EventOperatorDied, "killed in a restart", 2000),
		reportEvent("operator-a", store.EventOperatorRuntime, "codex", 5000),
		reportEvent("operator-a", store.EventOperatorExited, "done", 5000+1800),
		reportEvent("operator-a", store.EventOperatorShipped, "op/a", 5000+1900),
		reportEvent("operator-b", store.EventOperatorRuntime, "codex", 1000),
		reportEvent("operator-b", store.EventOperatorExited, "done", 1000+3600),
		reportEvent("operator-c", store.EventOperatorRuntime, "claude", 1000),
		reportEvent("operator-c", store.EventOperatorExited, "done", 1000+600),
	}
	rows := buildRuntimeReport(runs, events, func(r store.AgentRun) int64 { return r.TokensUsed + 5 })
	want := []runtimeReportRow{
		{Runtime: "claude", Started: 1, Finished: 1, MedianFinish: 10 * time.Minute, Tokens: 75},
		{Runtime: "codex", Started: 3, Finished: 2, Died: 1, Respawned: 1, Shipped: 1, MedianFinish: 45 * time.Minute, Tokens: 15},
		{Runtime: runtimeUnrecorded, Started: 1, Tokens: 5},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	var out bytes.Buffer
	writeRuntimeReport(&out, 7, rows)
	for _, s := range []string{"Operators, last 7 days", "started  finished", "codex", "45m "} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("table lacks %q:\n%s", s, out.String())
		}
	}
}

// The command reads runs and events from the store, and the tokens of a run
// the watchdog never counted from its output file (Codex bridge stream-json).
func TestRuntimeReportCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	proj := t.TempDir()
	pid, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: proj, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "b-1", AgentName: "operator-x", OpencodeSessionID: "s", SpawnTime: now - 600}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []store.CostEvent{reportEvent("operator-x", store.EventOperatorRuntime, "codex", now-600), reportEvent("operator-x", store.EventOperatorExited, "done", now-60)} {
		e.ProjectID = pid
		if _, err := st.InsertCostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	ops := filepath.Join(proj, ".commander", "operators")
	_ = os.MkdirAll(ops, 0o755)
	stream := `{"type":"system","subtype":"init","session_id":"s","model":"codex:gpt-5.5"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]},"session_id":"s"}
{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0,"usage":{"input_tokens":3726,"cache_read_input_tokens":42880,"output_tokens":148},"session_id":"s"}
`
	if err := os.WriteFile(filepath.Join(ops, "x.fifo"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runRuntime([]string{"report", "--days", "1"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "codex    1        1         0     0          9m             0        3874") {
		t.Errorf("report:\n%s", out.String())
	}
}

func TestShippedAgentFromWorktreeList(t *testing.T) {
	porcelain := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /repo/.worktrees/op-fix-x-commander-il9.1\nHEAD def\nbranch refs/heads/op/fix-x\n\n"
	if got := shippedAgent(porcelain, "op/fix-x"); got != "operator-op-fix-x-commander-il9.1" {
		t.Errorf("got %q", got)
	}
	if got := shippedAgent(porcelain, "main"); got != "" {
		t.Errorf("main checkout is not an Operator: %q", got)
	}
}

// A legacy real fifo under the .fifo name is skipped, never opened (it would block).
func TestStreamTokensSkipsAFifo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.fifo")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan int64, 1)
	go func() { done <- streamTokens(path) }()
	select {
	case n := <-done:
		if n != 0 {
			t.Errorf("tokens %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamTokens blocked on a fifo")
	}
}

func TestShippedAgentUnderTheNoindexWorktreeFolder(t *testing.T) {
	porcelain := "worktree /r\nbranch refs/heads/main\n\nworktree /r/.worktrees.noindex/op-fix-y-commander-il9.2\nbranch refs/heads/op/fix-y\n"
	if got := shippedAgent(porcelain, "op/fix-y"); got != "operator-op-fix-y-commander-il9.2" {
		t.Fatalf("shippedAgent = %q", got)
	}
}

func TestRuntimeReportFoldsHooksFallbackIntoClaude(t *testing.T) {
	runs := []store.AgentRun{{ID: 1, AgentName: "operator-fallback", TaskBeadID: "sample-1", SpawnTime: 100}}
	events := []store.CostEvent{reportEvent("operator-fallback", store.EventOperatorRuntime, spawn.ReasonClaudeHooksUnavailable, 100)}
	rows := buildRuntimeReport(runs, events, func(store.AgentRun) int64 { return 0 })
	if len(rows) != 1 || rows[0].Runtime != "claude" || rows[0].Started != 1 || rows[0].Forced != 1 {
		t.Fatalf("rows: %+v", rows)
	}
}
