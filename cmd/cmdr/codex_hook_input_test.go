package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexPreToolUse is a PreToolUse payload as codex-cli 0.163 sends it
// (synthetic ids and paths; the wire shape matches the CLI).
func codexPreToolUse(tool string, input any) string {
	raw, _ := json.Marshal(map[string]any{
		"session_id": "session-sample-1", "turn_id": "turn-sample-1", "transcript_path": "/tmp/codex/rollout.jsonl",
		"cwd": "/repo", "hook_event_name": "PreToolUse", "model": "gpt-6.1-sol", "permission_mode": "bypassPermissions",
		"tool_name": tool, "tool_input": input, "tool_use_id": "exec-sample-1",
	})
	return string(raw)
}

const codexPatch = "*** Begin Patch\n*** Add File: b.txt\n+hello\n*** End Patch"

func denied(t *testing.T, out []byte) string {
	t.Helper()
	var got struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.H.Decision != "deny" {
		return ""
	}
	return got.H.Reason
}

func TestShellCommandNormalizesCodexInput(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		"codex Bash string":     {codexPreToolUse("Bash", map[string]any{"command": "go test ./..."}), "go test ./..."},
		"argv":                  {codexPreToolUse("shell", map[string]any{"command": []string{"go", "test", "./..."}}), "go test ./..."},
		"argv sh -lc":           {codexPreToolUse("shell", map[string]any{"command": []string{"/bin/zsh", "-lc", "find / -name x"}}), "find / -name x"},
		"argv with spaces":      {codexPreToolUse("exec_command", map[string]any{"command": []string{"git", "commit", "-m", "a b"}}), "git commit -m 'a b'"},
		"apply_patch not shell": {codexPreToolUse("apply_patch", map[string]any{"command": codexPatch}), ""},
		"claude Bash":           {`{"tool_name":"Bash","tool_input":{"command":"ls"}}`, "ls"},
		"non-string argv":       {codexPreToolUse("shell", map[string]any{"command": []any{"ls", 3}}), ""},
	} {
		if got := readHookInput(strings.NewReader(tc.in)).shellCommand(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	in := readHookInput(strings.NewReader(codexPreToolUse("apply_patch", map[string]any{"command": codexPatch})))
	if !in.isEditTool() || in.toolDetail() != "Edit: b.txt" {
		t.Errorf("apply_patch: edit=%v detail=%q", in.isEditTool(), in.toolDetail())
	}
}

func TestManagerGuardDeniesCodexFileEdit(t *testing.T) {
	useInbox(t)
	home := os.Getenv("COMMANDER_HOME")
	var out bytes.Buffer
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(codexPreToolUse("apply_patch", map[string]any{"command": codexPatch})), &out)
	if r := denied(t, out.Bytes()); !strings.HasPrefix(r, "Managers do not do the work.") {
		t.Fatalf("apply_patch by a Manager not denied: %q", out.String())
	}
	raw, _ := os.ReadFile(filepath.Join(home, "logs", "manager-refusals.jsonl"))
	if !strings.Contains(string(raw), "Edit: b.txt") {
		t.Errorf("refusal log: %q", raw)
	}
	out.Reset()
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(codexPreToolUse("Bash", map[string]any{"command": "git commit -m x"})), &out)
	if denied(t, out.Bytes()) == "" {
		t.Errorf("codex shell commit by a Manager not denied: %q", out.String())
	}
	out.Reset()
	runInternalManagerGuard([]string{"--role", "manager-x"}, strings.NewReader(codexPreToolUse("Bash", map[string]any{"command": "br list"})), &out)
	if out.Len() != 0 {
		t.Errorf("read-only command denied: %q", out.String())
	}
}

func TestOperatorGuardOnCodexPayloads(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	for name, tc := range map[string]struct {
		tool  string
		input any
		deny  string // substring of the reason; "" = allowed
	}{
		"bare suite":         {"Bash", map[string]any{"command": "go test ./..."}, "internal-slot run"},
		"bare suite argv":    {"shell", map[string]any{"command": []string{"bash", "-lc", "go test ./..."}}, "internal-slot run"},
		"root scan":          {"Bash", map[string]any{"command": "find / -name '*.go'"}, "walks the whole machine"},
		"targeted test":      {"Bash", map[string]any{"command": "go test -p 2 ./internal/slot -run TestX"}, ""},
		"wrapped suite":      {"Bash", map[string]any{"command": "cmdr internal-slot run -- go test ./..."}, ""},
		"apply_patch":        {"apply_patch", map[string]any{"command": codexPatch}, ""},
		"patch text not run": {"apply_patch", map[string]any{"command": "*** Begin Patch\n*** Add File: x\n+go test ./...\n*** End Patch"}, ""},
	} {
		var out bytes.Buffer
		runInternalOperatorGuard([]string{"--role", "operator-op-x"}, strings.NewReader(codexPreToolUse(tc.tool, tc.input)), &out)
		r := denied(t, out.Bytes())
		if tc.deny == "" && r != "" || tc.deny != "" && !strings.Contains(r, tc.deny) {
			t.Errorf("%s: reason %q, want %q", name, r, tc.deny)
		}
	}
}
