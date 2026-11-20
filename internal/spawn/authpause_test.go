package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/authpause"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
)

func TestClaudeAuthFailureNeverFallsToAnotherRuntime(t *testing.T) {
	home := usageHome(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"OPENAI_API_KEY":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldHooks, oldTools := codexHooksReady, codexToolsReady
	codexHooksReady = func() bool { return true }
	t.Cleanup(func() { codexHooksReady, codexToolsReady = oldHooks, oldTools })
	if _, err := authpause.Mark(home, "default", "OAuth session expired", time.Now()); err != nil {
		t.Fatal(err)
	}
	order := []string{chat.RuntimeClaude, chat.RuntimeCodex}
	s := &Spawner{beads: &beads.MockClient{}}

	// A step labelled runtime:claude waits for the sign-in, even with a healthy Codex.
	codexToolsReady = func() bool { return true }
	_, err := s.resolveProvider(context.Background(), SpawnRequest{AgentAccounts: order, RuntimeOnly: chat.RuntimeClaude, TaskBeadID: "s-1"}, "operator-pinned")
	if !errors.Is(err, authpause.ErrPaused) {
		t.Fatalf("want the sign-in pause, got %v", err)
	}

	// An unpinned step may use Codex only while Codex can run a tool call.
	codexToolsReady = func() bool { return false }
	if got, err := s.resolveProvider(context.Background(), SpawnRequest{AgentAccounts: order, TaskBeadID: "s-2"}, "operator-unpinned-broken"); err == nil {
		t.Fatalf("a Codex whose tool preflight fails was chosen: %+v", got)
	}
	codexToolsReady = func() bool { return true }
	got, err := s.resolveProvider(context.Background(), SpawnRequest{AgentAccounts: order, TaskBeadID: "s-3"}, "operator-unpinned-ok")
	if err != nil || got.Runtime != chat.RuntimeCodex {
		t.Fatalf("a Codex with working tools should stay a fallback for unpinned work: %+v %v", got, err)
	}

	// Once the pause is lifted the Claude step starts on Claude.
	if _, _, err := authpause.Clear(home, "default"); err != nil {
		t.Fatal(err)
	}
	got, err = s.resolveProvider(context.Background(), SpawnRequest{AgentAccounts: order, RuntimeOnly: chat.RuntimeClaude, TaskBeadID: "s-4"}, "operator-after")
	if err != nil || got.Runtime != chat.RuntimeClaude {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestClaudePinnedStepNeverGoesToCodexWhenClaudeIsOut(t *testing.T) {
	home := usageHome(t)
	oldHooks, oldTools := codexHooksReady, codexToolsReady
	codexHooksReady, codexToolsReady = func() bool { return true }, func() bool { return true }
	t.Cleanup(func() { codexHooksReady, codexToolsReady = oldHooks, oldTools })
	markOut(t, home, chat.RuntimeClaude, "default")
	s := &Spawner{beads: &beads.MockClient{}}
	got, err := s.resolveProvider(context.Background(), SpawnRequest{AgentAccounts: []string{chat.RuntimeClaude, chat.RuntimeCodex}, RuntimeOnly: chat.RuntimeClaude, TaskBeadID: "s-1"}, "operator-out")
	if !errors.Is(err, chat.ErrNoUsage) || got.Runtime == chat.RuntimeCodex {
		t.Fatalf("got %+v %v", got, err)
	}
}
