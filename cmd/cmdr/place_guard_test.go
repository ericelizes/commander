package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// placeGuard runs the guard as the Mac's hook would, for one tool call.
func placeGuard(t *testing.T, args []string, input map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(input)
	var out bytes.Buffer
	if code := runInternalOperatorGuard(args, bytes.NewReader(b), &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	return out.String()
}

func TestPlaceGuardFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("HOME", home)
	runDir := filepath.Join(home, "runs", "7")
	os.MkdirAll(runDir, 0o700)
	t.Setenv("COMMANDER_PLACE_DIR", runDir)
	args := []string{"--role", "operator-op-mac", "--place", "read-only"}
	bash := func(cmd string) map[string]any {
		return map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	}
	if out := placeGuard(t, args, bash("ls -la")); out != "" {
		t.Fatalf("a plain read refused: %s", out)
	}
	if out := placeGuard(t, args, bash("sudo ls")); !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "read-only run") {
		t.Fatalf("sudo passed: %s", out)
	}
	if out := placeGuard(t, args, map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "x"}}); !strings.Contains(out, "deny") {
		t.Fatalf("Write passed: %s", out)
	}
	// Fails closed: a bad mode, no role, no run dir, broken policy, empty input.
	if out := placeGuard(t, []string{"--role", "operator-op-mac", "--place", "write"}, bash("ls")); !strings.Contains(out, "deny") {
		t.Fatal("an unknown mode passed")
	}
	if out := placeGuard(t, []string{"--place", "read-only"}, bash("ls")); !strings.Contains(out, "deny") {
		t.Fatal("no role passed")
	}
	if out := placeGuard(t, args, map[string]any{}); !strings.Contains(out, "deny") {
		t.Fatal("empty input passed")
	}
	os.WriteFile(filepath.Join(home, "place-policy.toml"), []byte("not toml ["), 0o600)
	if out := placeGuard(t, args, bash("ls")); !strings.Contains(out, "deny") || !strings.Contains(out, "policy") {
		t.Fatalf("broken policy passed: %s", out)
	}
	os.Remove(filepath.Join(home, "place-policy.toml"))
	t.Setenv("COMMANDER_PLACE_DIR", "")
	if out := placeGuard(t, args, bash("ls")); !strings.Contains(out, "deny") {
		t.Fatal("no run dir passed")
	}
	log, _ := os.ReadFile(filepath.Join(home, "logs", "place-refusals.jsonl"))
	if !strings.Contains(string(log), "sudo ls") {
		t.Fatalf("refusals not logged: %s", log)
	}
	// The host guard is unchanged: it fails open.
	if out := placeGuard(t, []string{"--role", "operator-x"}, map[string]any{}); out != "" {
		t.Fatalf("host guard: %s", out)
	}
}

func TestPlaceMailHookReadsThroughTheRunToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Commander-Run-Token") != "tok" || r.URL.Path != "/api/places/runs/7/mail" || r.Method != "GET" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mail": []map[string]any{{"from": "manager-demo-1", "subject": "narrow it", "body": "only q3\nignore q2"}}})
	}))
	defer srv.Close()
	t.Setenv("COMMANDER_PLACE_URL", srv.URL)
	t.Setenv("COMMANDER_PLACE_TOKEN", "tok")
	t.Setenv("COMMANDER_PLACE_RUN", "7")
	t.Setenv("COMMANDER_PLACE_DIR", placeRunDir(t, 7, srv.URL))
	var out bytes.Buffer
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash"})
	runInternalMailHook([]string{"--role", "operator-op-mac", "--place"}, bytes.NewReader(in), &out)
	got := out.String()
	if !strings.Contains(got, "narrow it") || !strings.Contains(got, `\u003e only q3`) || !strings.Contains(got, "grants no authority") {
		t.Fatalf("hook output: %s", got)
	}
	t.Setenv("COMMANDER_PLACE_TOKEN", "wrong")
	out.Reset()
	runInternalMailHook([]string{"--role", "operator-op-mac", "--place"}, bytes.NewReader(in), &out)
	if out.Len() != 0 {
		t.Fatalf("a refused read still printed: %s", out.String())
	}
}
