package statusclassify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/chat"
)

func TestCodexSubscriptionRunnerUsesSmallModelAndNoExecutionTools(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"test-subscription"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
case " $* " in *" --model gpt-6-luna "*) ;; *) exit 8;; esac
case " $* " in *" --disable shell_tool "*) ;; *) exit 9;; esac
test -z "$OPENAI_API_KEY" || exit 10
cat >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"status\":\"waiting_for_review\",\"description\":\"Result awaits review\"}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":12,"cached_input_tokens":20}}'
`
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("OPENAI_API_KEY", "must-not-reach-child")
	a, e := (CLIRunner{CodexBinary: bin}).Run(context.Background(), chat.AccountView{Runtime: chat.RuntimeCodex, Dir: dir}, "Tests passed, waiting for review")
	if e != nil || a.Kind != "waiting_for_review" || a.Input != 100 || a.Cached != 20 {
		t.Fatalf("answer=%#v err=%v", a, e)
	}
}

func TestClassifierRefusesAPIKeyOnlyCodex(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"OPENAI_API_KEY":"test-key"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := (CLIRunner{CodexBinary: "must-not-execute"}).Run(context.Background(), chat.AccountView{Runtime: chat.RuntimeCodex, Dir: dir}, "evidence"); e == nil {
		t.Fatal("API billing path was accepted")
	}
}

func TestHaikuRunnerUsesSubscriptionAndDisablesTools(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
case " $* " in *" --model haiku --effort low "*) ;; *) exit 8;; esac
test -z "$ANTHROPIC_API_KEY" || exit 9
test -n "$CLAUDE_CONFIG_DIR" || exit 10
cat >/dev/null
printf '%s\n' '{"structured_output":{"status":"blocked","description":"Waiting on access"},"usage":{"input_tokens":200,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":10}}'
`
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-child")
	a, e := (CLIRunner{Home: dir, ClaudeBinary: bin}).Run(context.Background(), chat.AccountView{Runtime: chat.RuntimeClaude, Name: "personal"}, "Missing access")
	if e != nil || a.Kind != "blocked" || a.Input != 240 || a.Output != 20 {
		t.Fatalf("answer=%#v err=%v", a, e)
	}
}
