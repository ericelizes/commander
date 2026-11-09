package chat

import (
	"os"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
)

// Share 0 never draws Codex, whatever the hooks say.
func TestAssignNewManagerRuntimeAtShareZeroIsClaude(t *testing.T) {
	home := t.TempDir()
	for _, ws := range []string{"ws-1", "ws-2", "ws-3", "ws-4", "ws-5", "ws-6", "ws-7", "ws-8"} {
		role := "manager-" + ws
		if err := AssignNewManagerRuntime(home, role, ws, 0, func() bool { return true }); err != nil {
			t.Fatal(err)
		}
		if got := ReadRuntime(home, role); got != RuntimeClaude || RuntimePinned(home, role) {
			t.Errorf("%s: runtime %s, pinned %v", role, got, RuntimePinned(home, role))
		}
	}
}

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
