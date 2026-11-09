package evals

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A one-shot Codex Operator whose turn runs a native child agent: the real
// cmdr `__codex-bridge` talks to a scripted app-server that interleaves the
// child's notifications (its own threadId) with the parent's. The bridge must
// report the parent's own final turn, never end on the child's completion.
func TestCodexChildCompletionScenario(t *testing.T) {
	files, _ := filepath.Glob("scenarios/codex-child/*.json")
	if len(files) == 0 {
		t.Fatal("no codex-child scenarios")
	}
	dir := t.TempDir()
	cmdr := filepath.Join(dir, "cmdr")
	codex := filepath.Join(dir, "codex")
	// Link only this throwaway binary to synthetic system/managed paths.
	// Production has no environment override that could bypass managed policy.
	configFlags := "-ldflags=-X=github.com/example-user/commander/internal/chat.codexSystemConfigPath=" + filepath.Join(dir, "system.toml") + " -X=github.com/example-user/commander/internal/chat.codexManagedConfigPath=" + filepath.Join(dir, "managed_config.toml")
	mustBuild(t, append(os.Environ(), "GOFLAGS="+os.Getenv("GOFLAGS")+" "+strconv.Quote(configFlags)), cmdr, "./cmd/cmdr")
	mustBuild(t, os.Environ(), codex, "./tests/evals/fakecodex")
	t.Run("unsafe-browser-alias", func(t *testing.T) { runCodexUnsafeAliasScenario(t, cmdr) })
	for _, f := range files {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) { runCodexChildScenario(t, cmdr, codex, f) })
	}
}

func runCodexChildScenario(t *testing.T, cmdr, codex, file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		WantResult      string   `json:"want_result"`
		WantInputTokens int64    `json:"want_input_tokens"`
		NeverContains   []string `json:"never_contains"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdr, "__codex-bridge", "--prompt", "review the change", "--cwd", dir, "--skip-permissions")
	cmd.Env = append(os.Environ(), "HOME="+dir, "CODEX_HOME="+filepath.Join(dir, "codex-home"),
		"COMMANDER_HOME="+filepath.Join(dir, "commander"), "COMMANDER_CODEX_BIN="+codex, "FAKE_CODEX_SCENARIO="+abs, "COMMANDER_CODEX_BROWSER_SERVER=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bridge: %v\nstderr: %s\nstdout: %s", err, &stderr, &stdout)
	}

	var results []map[string]any
	sc2 := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	for sc2.Scan() {
		var line map[string]any
		if json.Unmarshal(sc2.Bytes(), &line) == nil && line["type"] == "result" {
			results = append(results, line)
		}
	}
	if len(results) != 1 {
		t.Fatalf("want exactly one result line (the parent's), got %d:\n%s", len(results), &stdout)
	}
	if got := results[0]["result"]; got != sc.WantResult || results[0]["is_error"] != false {
		t.Fatalf("result = %v (is_error %v), want %q:\n%s", got, results[0]["is_error"], sc.WantResult, &stdout)
	}
	usage, _ := results[0]["usage"].(map[string]any)
	if got, _ := usage["input_tokens"].(float64); int64(got) != sc.WantInputTokens {
		t.Fatalf("usage = %v, want input_tokens %d (the child's tokens counted?)", usage, sc.WantInputTokens)
	}
	for _, s := range sc.NeverContains {
		if strings.Contains(stdout.String(), s) {
			t.Fatalf("parent stream carries the child's %q:\n%s", s, &stdout)
		}
	}
}

// Exercise the actual bridge launch boundary with a marker-writing fake process.
// If unsafe overrides ever reach app-server, the marker makes the failure visible.
func runCodexUnsafeAliasScenario(t *testing.T, cmdr string) {
	raw, err := os.ReadFile("scenarios/codex-mcp/unsafe-alias.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct{ Aliases, Selected []string }
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	stub := filepath.Join(dir, "codex-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf started > \"$ALIAS_START_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "codex-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.toml")
	for _, alias := range sc.Aliases {
		content := "[mcp_servers." + strconv.Quote(alias) + "]\ncommand='chrome-devtools-mcp'\n"
		if err := os.WriteFile(config, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		for _, selected := range append(append([]string{}, sc.Selected...), alias) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmd := exec.CommandContext(ctx, cmdr, "__codex-bridge", "--prompt", "synthetic test", "--cwd", dir)
			cmd.Env = append(os.Environ(), "HOME="+dir, "CODEX_HOME="+home,
				"COMMANDER_HOME="+filepath.Join(dir, "commander"), "COMMANDER_CODEX_BIN="+stub,
				"ALIAS_START_MARKER="+marker, "COMMANDER_CODEX_BROWSER_SERVER="+selected)
			out, err := cmd.CombinedOutput()
			cancel()
			if err == nil || !strings.Contains(string(out), strconv.Quote(alias)) || !strings.Contains(string(out), "rename") {
				t.Fatalf("alias=%q selected=%q: %v: %s", alias, selected, err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("fake app-server started: %v", err)
			}
			got, err := os.ReadFile(config)
			if err != nil || string(got) != content {
				t.Fatal("config mutated", err)
			}
		}
	}
}
