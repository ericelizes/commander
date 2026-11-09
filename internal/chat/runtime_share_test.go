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
	if p, _ := planManagerStart(home, "manager-pin", false, codexOut, now, yes); p.runtime != RuntimeCodex {
		t.Errorf("pinned Codex moved: %+v", p)
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
