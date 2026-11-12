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
	plan, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true }, commanderOrder)
	if !errors.Is(err, ErrNoUsage) {
		t.Fatalf("explicit Claude pin escaped: %+v %v", plan, err)
	}
	if err := WriteAutoRuntime(h.home, "commander", RuntimeClaude); err != nil {
		t.Fatal(err)
	}
	plan, err = planChatStart(h.home, "commander", false, usage, now, func() bool { return true }, commanderOrder)
	if err != nil || plan.runtime != RuntimeCodex {
		t.Fatalf("unpinned fallback failed: %+v %v", plan, err)
	}
	usage[RuntimeCodex] = Usage{Status: UsageOut, OutUntil: now.Add(time.Hour).Unix()}
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true }, commanderOrder); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("all exhausted: %v", err)
	}
	delete(usage, RuntimeCodex)
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return false }, commanderOrder); !errors.Is(err, ErrNoUsage) {
		t.Fatalf("missing hooks: %v", err)
	}
	os.Remove(filepath.Join(CodexHome(), "auth.json"))
	if _, err := planChatStart(h.home, "commander", false, usage, now, func() bool { return true }, commanderOrder); !errors.Is(err, ErrNoUsage) {
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

var commanderOrder = []string{RuntimeClaude, RuntimeCodex}

func TestCommanderReturnsToClaudeWhenCodexWasOnlyAFallback(t *testing.T) {
	h := accountHarness(t)
	now := time.Now()
	if err := WriteAutoRuntime(h.home, "commander", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	plan, err := planChatStart(h.home, "commander", false, map[string]Usage{}, now, func() bool { return true }, commanderOrder)
	if err != nil || plan.runtime != RuntimeClaude || plan.account != "default" {
		t.Fatalf("Commander stayed on its Codex fallback: %+v %v", plan, err)
	}
}

func TestCommanderFollowsSavedAccountOrder(t *testing.T) {
	h := accountHarness(t, "work")
	now := time.Now()
	plan, err := planChatStart(h.home, "commander", false, map[string]Usage{}, now, func() bool { return true }, []string{RuntimeCodex, RuntimeClaude})
	if err != nil || plan.runtime != RuntimeCodex {
		t.Fatalf("saved order Codex first: %+v %v", plan, err)
	}
	plan, err = planChatStart(h.home, "commander", false, map[string]Usage{}, now, func() bool { return true }, []string{"work", RuntimeCodex})
	if err != nil || plan.runtime != RuntimeClaude || plan.account != "work" {
		t.Fatalf("saved order naming one account: %+v %v", plan, err)
	}
	plan, err = planChatStart(h.home, "commander", false, map[string]Usage{UsageKey(RuntimeClaude, "work"): {Status: UsageOut, OutUntil: now.Add(time.Hour).Unix()}}, now, func() bool { return true }, []string{"work"})
	if err != nil || plan.account == "work" {
		t.Fatalf("saved preference blocked another account: %+v %v", plan, err)
	}

}

func TestAccountOrderReadFailureCannotLaunchOnDefaults(t *testing.T) {
	h := accountHarness(t)
	h.role("commander")
	h.e.opts.AccountOrder = func(context.Context, string) ([]string, error) {
		return nil, errors.New("private account store unavailable")
	}
	if _, err := h.e.Start(context.Background(), "commander", StartOptions{}); err == nil || !strings.Contains(err.Error(), "account store unavailable") {
		t.Fatalf("failed account read fell back to defaults: %v", err)
	}
	if h.e.Running("commander") {
		t.Fatal("started an account without its saved preferences")
	}
}

// Integration guard: automatic Commander preferences permit provider fallback;
// explicit Commander and Manager pins refuse it. All retain capacity recovery.
func TestUnifiedPlanCapacityRecoveryPreservesRolePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		pinned     bool
	}{
		{"commander-preference", "commander", false},
		{"commander-pin", "commander", true},
		{"manager-pin", "manager-example", true},
	} {
		role := tc.role
		t.Run(tc.name, func(t *testing.T) {
			h := accountHarness(t)
			h.e.opts.CodexHooksReady = func() bool { return true }
			write := WriteAutoRuntime
			if tc.pinned {
				write = WriteRuntime
			}
			if err := write(h.home, role, RuntimeClaude); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			h.e.Usage().MarkOut(RuntimeClaude, "default", role, now.Add(time.Hour).Unix(), now)
			h.e.Usage().MarkOut(RuntimeCodex, "", role, now.Add(time.Hour).Unix(), now)
			if _, _, err := h.e.steerManager(role, false); !errors.Is(err, ErrNoUsage) {
				t.Fatalf("exhausted plan: %v", err)
			}
			if h.e.CapacityFailures()[role] == "" {
				t.Fatal("capacity refusal missing")
			}
			h.e.Usage().Record(RuntimeCodex, "", role, []byte(`{"status":"allowed"}`), now)
			_, _, err := h.e.steerManager(role, false)
			if !tc.pinned {
				if err != nil || ReadRuntime(h.home, role) != RuntimeCodex || len(h.e.CapacityFailures()) != 0 {
					t.Fatalf("unified fallback failed: runtime=%s capacity=%v err=%v", ReadRuntime(h.home, role), h.e.CapacityFailures(), err)
				}
			} else if !errors.Is(err, ErrNoUsage) || h.e.CapacityFailures()[role] == "" {
				t.Fatalf("explicit pin lost: capacity=%v err=%v", h.e.CapacityFailures(), err)
			}
			h.e.Usage().Record(RuntimeClaude, "default", role, []byte(`{"status":"allowed"}`), now)
			if _, _, err := h.e.steerManager(role, false); err != nil || ReadRuntime(h.home, role) != RuntimeClaude || len(h.e.CapacityFailures()) != 0 {
				t.Fatalf("Claude recovery failed: runtime=%s capacity=%v err=%v", ReadRuntime(h.home, role), h.e.CapacityFailures(), err)
			}
		})
	}
}
