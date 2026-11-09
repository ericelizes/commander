package chat

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

// A Manager a share once drew onto Codex starts on Claude when the share is now
// 0, with the handoff; a person's pin is kept, and so is a draw under a share a
// person configured.
func TestDropDrawnCodexAtShareZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv(agentrt.CodexManagerShareEnvVar, "0")
	t.Setenv("COMMANDER_HOME", home)
	if err := WriteAutoRuntime(home, "manager-drawn", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/chat/manager-drawn.jsonl", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !dropDrawnCodex(home, "manager-drawn") {
		t.Fatal("drawn Codex at share 0 not dropped")
	}
	spec := WithRuntime(Spec{Role: "manager-drawn", Model: "opus"}, home)
	if spec.Runtime != RuntimeClaude || spec.Model != "opus" || spec.Handoff == "" {
		t.Errorf("drawn at share 0: %+v", spec)
	}
	if got := ReadRuntime(home, "manager-drawn"); got != RuntimeClaude || RuntimePinned(home, "manager-drawn") {
		t.Errorf("recorded %s, pinned %v", got, RuntimePinned(home, "manager-drawn"))
	}

	if err := WriteRuntime(home, "manager-pinned", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if dropDrawnCodex(home, "manager-pinned") || WithRuntime(Spec{Role: "manager-pinned"}, home).Runtime != RuntimeCodex {
		t.Error("a person's pin was dropped")
	}

	t.Setenv(agentrt.CodexManagerShareEnvVar, "0.5")
	if err := WriteAutoRuntime(home, "manager-kept", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if dropDrawnCodex(home, "manager-kept") || WithRuntime(Spec{Role: "manager-kept"}, home).Runtime != RuntimeCodex {
		t.Error("a draw under a configured share was dropped")
	}
}

func out(m map[string]Usage, key string) {
	m[key] = Usage{Status: UsageOut, OutUntil: time.Now().Add(time.Hour).Unix()}
}

// A never-started Manager draws its runtime from the Manager share: Codex only
// with hooks and usage left; share 0, a started one or an out Codex is Claude.
func TestPlanManagerStartDraw(t *testing.T) {
	home := t.TempDir()
	now, yes, no := time.Now(), func() bool { return true }, func() bool { return false }
	codexOut := map[string]Usage{}
	out(codexOut, UsageKey(RuntimeCodex, ""))
	for _, tc := range []struct {
		name  string
		share string
		fresh bool
		usage map[string]Usage
		hooks func() bool
		want  string
	}{
		{"share 0", "0", true, nil, yes, RuntimeClaude},
		{"share 1", "1", true, nil, yes, RuntimeCodex},
		{"no hooks", "1", true, nil, no, RuntimeClaude},
		{"codex out", "1", true, codexOut, yes, RuntimeClaude},
		{"started before", "1", false, nil, yes, RuntimeClaude},
	} {
		t.Setenv(agentrt.CodexManagerShareEnvVar, tc.share)
		p, err := planManagerStart(home, "manager-ws-1", tc.fresh, tc.usage, now, tc.hooks)
		if err != nil || p.runtime != tc.want {
			t.Errorf("%s: %+v %v", tc.name, p, err)
		}
	}
	for _, f := range []string{RuntimeFile(home, "manager-ws-1"), AccountFile(home, "manager-ws-1")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("plan wrote %s", f)
		}
	}
}

// An unpinned Codex Manager whose Codex is out runs on Claude; a pinned one
// stays. With every Claude account out an unpinned Manager goes to Codex; a
// pinned one, or no Codex, is refused with ErrNoUsage.
func TestPlanManagerStartRuntimeFallbacks(t *testing.T) {
	home := t.TempDir()
	now, yes, no := time.Now(), func() bool { return true }, func() bool { return false }
	codexOut := map[string]Usage{}
	out(codexOut, UsageKey(RuntimeCodex, ""))
	_ = WriteAutoRuntime(home, "manager-auto", RuntimeCodex)
	_ = WriteRuntime(home, "manager-pin", RuntimeCodex)
	if p, _ := planManagerStart(home, "manager-auto", false, codexOut, now, yes); p.runtime != RuntimeClaude {
		t.Errorf("unpinned out Codex: %+v", p)
	}
	// d82: a Codex pin with Codex out (or without hooks) runs on Claude, pin kept.
	if p, err := planManagerStart(home, "manager-pin", false, codexOut, now, yes); err != nil || p.runtime != RuntimeClaude || p.pin != RuntimeCodex {
		t.Errorf("pinned Codex, Codex out: %+v %v", p, err)
	}
	if p, err := planManagerStart(home, "manager-pin", false, map[string]Usage{}, now, no); err != nil || p.runtime != RuntimeClaude || p.pin != RuntimeCodex {
		t.Errorf("pinned Codex, no hooks: %+v %v", p, err)
	}
	if p, _ := planManagerStart(home, "manager-pin", false, map[string]Usage{}, now, yes); p.runtime != RuntimeCodex {
		t.Errorf("pinned Codex with usage: %+v", p)
	}
	allOut := map[string]Usage{}
	out(allOut, UsageKey(RuntimeCodex, ""))
	out(allOut, UsageKey(RuntimeClaude, "default"))
	if _, err := planManagerStart(home, "manager-pin", false, allOut, now, yes); !errors.Is(err, ErrNoUsage) || !strings.Contains(err.Error(), "pinned to Codex") {
		t.Errorf("pinned Codex, everything out: %v", err)
	}

	claudeOut := map[string]Usage{}
	out(claudeOut, UsageKey(RuntimeClaude, "default"))
	if p, err := planManagerStart(home, "manager-a", false, claudeOut, now, yes); err != nil || p.runtime != RuntimeCodex {
		t.Errorf("all Claude out, Codex ready: %+v %v", p, err)
	}
	if _, err := planManagerStart(home, "manager-a", false, claudeOut, now, no); !errors.Is(err, ErrNoUsage) || !strings.Contains(err.Error(), "hooks") {
		t.Errorf("no hooks: %v", err)
	}
	out(claudeOut, UsageKey(RuntimeCodex, ""))
	if _, err := planManagerStart(home, "manager-a", false, claudeOut, now, yes); !errors.Is(err, ErrNoUsage) {
		t.Errorf("everything out: %v", err)
	}
	_ = WriteRuntime(home, "manager-claude", RuntimeClaude)
	delete(claudeOut, UsageKey(RuntimeCodex, ""))
	if _, err := planManagerStart(home, "manager-claude", false, claudeOut, now, yes); !errors.Is(err, ErrNoUsage) || !strings.Contains(err.Error(), "pinned") {
		t.Errorf("pinned Claude: %v", err)
	}
}

// An account a person chose keeps a Manager nobody pinned unpinned (d82): with
// every Claude account out and Codex ready it still falls back to Codex. A
// pinned Manager's account choice pins it to Claude and drops a Codex fallback.
func TestAccountChoiceKeepsTheManagerUnpinned(t *testing.T) {
	home := t.TempDir()
	now, yes := time.Now(), func() bool { return true }
	if err := WriteAccountRuntime(home, "manager-fresh"); err != nil {
		t.Fatal(err)
	}
	if RuntimePinned(home, "manager-fresh") || ReadRuntime(home, "manager-fresh") != RuntimeClaude {
		t.Fatalf("an account choice pinned the runtime: pinned=%v", RuntimePinned(home, "manager-fresh"))
	}
	claudeOut := map[string]Usage{}
	out(claudeOut, UsageKey(RuntimeClaude, "default"))
	if p, err := planManagerStart(home, "manager-fresh", false, claudeOut, now, yes); err != nil || p.runtime != RuntimeCodex {
		t.Fatalf("all Claude out, Codex ready: %+v %v", p, err)
	}

	if err := WriteRuntime(home, "manager-pin", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RuntimeFallbackFile(home, "manager-pin"), []byte("codex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccountRuntime(home, "manager-pin"); err != nil {
		t.Fatal(err)
	}
	if ManagerPin(home, "manager-pin") != RuntimeClaude || ReadRuntimePin(home, "manager-pin") != RuntimeCodex {
		t.Fatalf("pinned Manager: pin %q, pin file %q", ManagerPin(home, "manager-pin"), ReadRuntimePin(home, "manager-pin"))
	}

	if err := WriteAccountRuntime(home, "operator-x"); err != nil {
		t.Fatal(err)
	}
	if fileExists(RuntimeAutoFile(home, "operator-x")) {
		t.Fatal("an Operator got a Manager's auto marker")
	}
}

// A Manager a share drew onto Codex, with the share now 0, goes to Claude only
// when Claude has usage; with every Claude account out it stays on Codex, so an
// outage never flips it to Claude and back (losing its Codex thread).
func TestPlanManagerStartDrawnCodexStaysWhileClaudeIsOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv(agentrt.CodexManagerShareEnvVar, "0")
	now, yes := time.Now(), func() bool { return true }
	if err := WriteAutoRuntime(home, "manager-drawn", RuntimeCodex); err != nil {
		t.Fatal(err)
	}
	if p, err := planManagerStart(home, "manager-drawn", false, map[string]Usage{}, now, yes); err != nil || p.runtime != RuntimeClaude || p.leave == "out of usage" {
		t.Errorf("share 0, Claude has usage: %+v %v", p, err)
	}
	claudeOut := map[string]Usage{}
	out(claudeOut, UsageKey(RuntimeClaude, "default"))
	if p, err := planManagerStart(home, "manager-drawn", false, claudeOut, now, yes); err != nil || p.runtime != RuntimeCodex {
		t.Errorf("share 0, Claude out: %+v %v", p, err)
	}
}
