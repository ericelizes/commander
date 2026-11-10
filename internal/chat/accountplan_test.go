package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommanderFallsBackAcrossProviders(t *testing.T) {
	h := accountHarness(t, "work")
	h.e.opts.CodexHooksReady = func() bool { return true }
	h.role("commander")
	ctx := context.Background()
	if _, err := h.e.SetAccount(ctx, "commander", "work", SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	until, now := time.Now().Add(time.Hour).Unix(), time.Now()
	h.e.Usage().MarkOut(RuntimeClaude, "default", "commander", until, now)
	h.e.Usage().MarkOut(RuntimeClaude, "work", "commander", until, now)
	st := h.start("commander")
	if st.Runtime != RuntimeCodex || !st.AutoSwitch {
		t.Fatalf("no fallback to Codex: %+v", st)
	}
	// The fake's initial usage report must land before the limit; otherwise
	// that earlier report can overwrite the simulated exhaustion.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if u, ok := h.e.Usage().Get(RuntimeCodex); ok && u.Role == "commander" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Codex did not report initial usage")
		}
	}
	// Exhaust Codex, restore one Claude account, and take the real restart path.
	h.e.Usage().MarkOut(RuntimeCodex, "", "commander", until, now)
	h.e.Usage().MarkOut(RuntimeClaude, "work", "commander", now.Add(-time.Minute).Unix(), now)
	c, _ := h.e.chat("commander")
	sub := h.subscribe("commander")
	h.e.moveManager(c)
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindUser && strings.Contains(e.Text, "re-read your handover") })
	st, _ = h.e.Status("commander")
	if st.Runtime != RuntimeClaude || st.Account != "work" {
		t.Fatalf("no fallback to Claude: %+v", st)
	}
}

func TestCommanderPlanSkipsDisconnectedAndExhaustedAccounts(t *testing.T) {
	h := accountHarness(t)
	now := time.Now()
	os.MkdirAll(filepath.Join(h.home, "claude-accounts", "stale"), 0700)
	os.WriteFile(filepath.Join(h.home, "claude-accounts", "stale", ".credentials.json"), []byte(`{"mcpOAuth":{"example":{}}}`), 0600)
	usage := map[string]Usage{UsageKey(RuntimeClaude, "default"): {Status: UsageOut, OutUntil: now.Add(time.Hour).Unix()}}
	if err := WriteRuntime(h.home, "commander", RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	plan, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true })
	if err != nil || plan.runtime != RuntimeCodex || plan.pin != RuntimeClaude {
		t.Fatalf("Claude preference blocked fallback: %+v %v", plan, err)
	}
	usage[RuntimeCodex] = Usage{Status: UsageOut, OutUntil: now.Add(time.Hour).Unix()}
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true }); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("all exhausted: %v", err)
	}
	delete(usage, RuntimeCodex)
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return false }); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("missing hooks: %v", err)
	}
	os.Remove(filepath.Join(CodexHome(), "auth.json"))
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true }); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("disconnected Codex: %v", err)
	}
}

func TestCodexIsSelectableAsAnAccount(t *testing.T) {
	h := accountHarness(t)
	h.e.opts.CodexHooksReady = func() bool { return true }
	h.role("commander")
	if _, err := h.e.SetAccount(context.Background(), "commander", "codex", SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := h.start("commander"); st.Runtime != RuntimeCodex {
		t.Fatalf("selected account: %+v", st)
	}
}

func TestCommanderAutoContinuesOnCodexAfterLimit(t *testing.T) {
	h := accountHarness(t, "work")
	h.e.opts.CodexHooksReady = func() bool { return true }
	h.role("commander")
	ctx := context.Background()
	if _, err := h.e.SetAccount(ctx, "commander", "work", SwitchOptions{}); err != nil {
		t.Fatal(err)
	}
	h.start("commander")
	now := time.Now()
	h.e.Usage().MarkOut(RuntimeClaude, "default", "commander", now.Add(time.Hour).Unix(), now)
	sub := h.subscribe("commander")
	if err := h.e.Send(ctx, "commander", "usagelimit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, func(e Event) bool { return e.Kind == KindUser && strings.HasPrefix(e.Text, "[switch]") })
	if st, _ := h.e.Status("commander"); st.Runtime != RuntimeCodex || !st.Running {
		t.Fatalf("Commander did not automatically continue on Codex: %+v", st)
	}
}
