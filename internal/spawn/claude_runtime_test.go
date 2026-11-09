package spawn

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
)

func TestSpawnKnowledge_ClaudeRuntimeSendsHeadlessClaude(t *testing.T) {
	withTempHome(t)
	t.Setenv(agentrt.EnvVar, string(agentrt.Claude))
	claudeDir := filepath.Join(os.Getenv("HOME"), ".claude")
	t.Setenv("COMMANDER_HOME", t.TempDir())
	dir := makeGitRepo(t)

	sp, _, _, _, tx := newKnowledgeSpawner(t, "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f")
	if _, err := sp.Spawn(context.Background(), goodKnowledgeRequest(t, dir)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var sendKeys string
	for _, c := range tx.Calls {
		if strings.HasPrefix(c, "SendKeys(") {
			sendKeys = c
		}
	}
	if !strings.Contains(sendKeys, "keys=env -u CLAUDE_CONFIG_DIR ") {
		t.Errorf("default knowledge run keeps an inherited profile: %q", sendKeys)
	}
	for _, want := range []string{"'--output-format' 'stream-json'", "'-n' 'operator-4fc' '--append-system-prompt-file'", "'--model' 'sonnet'", "'--session-id' '"} {
		if !strings.Contains(sendKeys, want) {
			t.Errorf("SendKeys missing %q; got %q", want, sendKeys)
		}
	}
	if strings.Contains(sendKeys, "opencode") {
		t.Errorf("claude runtime must not invoke opencode: %q", sendKeys)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commander", "operators", "4fc.claude.session-id")); err != nil {
		t.Errorf("claude session file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("COMMANDER_HOME"), "claude-agents", "operator-4fc.md")); err != nil {
		t.Errorf("claude operator prompt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "agents")); !os.IsNotExist(err) {
		t.Errorf("nothing may be written to ~/.claude/agents")
	}
}

func TestUseClaudeInContainer(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "")
	req := SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "bd-1", Branch: "feat/x", Model: "anthropic/claude-sonnet-4-7"}
	opts := buildSpawnOptions(req, "/p/.worktrees/feat-x", "cmdr_p", "/commander", "operator-feat-x")

	useClaudeInContainer(&opts, req, "operator-feat-x")

	for _, v := range opts.Volumes {
		if strings.Contains(v.Target, "opencode") {
			t.Errorf("opencode mount should be dropped: %+v", v)
		}
	}
	if last := opts.Volumes[len(opts.Volumes)-1]; last.Target != "/root/.commander/claude-agents" || !last.ReadOnly {
		t.Errorf("claude agents mount = %+v", last)
	}
	if opts.Env["IS_SANDBOX"] != "1" || opts.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
		t.Errorf("env = %v", opts.Env)
	}
	if _, set := opts.Env["ANTHROPIC_API_KEY"]; set {
		t.Errorf("unset host credentials must not be forwarded")
	}
	if opts.Cmd[0] != "claude" || !slices.Contains(opts.Cmd, "--dangerously-skip-permissions") ||
		!slices.Contains(opts.Cmd, "/root/.commander/claude-agents/operator-feat-x.md") {
		t.Errorf("cmd = %v", opts.Cmd)
	}
}

// An Operator must use the plan login, never silently bill the API.
func TestClaudeContainerAuth_OnlyThePlanLoginIsForwarded(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	t.Setenv("COMMANDER_ALLOW_API_KEY", "")
	req := SpawnRequest{ProjectName: "p", ProjectPath: "/p", TaskBeadID: "bd-1", Branch: "feat/x", Model: "anthropic/claude-sonnet-4-7"}
	opts := buildSpawnOptions(req, "/p/.worktrees/feat-x", "cmdr_p", "/commander", "operator-feat-x")
	useClaudeInContainer(&opts, req, "operator-feat-x")
	if opts.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
		t.Fatalf("the login token must be forwarded: %v", opts.Env)
	}
	if _, leaked := opts.Env["ANTHROPIC_API_KEY"]; leaked {
		t.Fatal("ANTHROPIC_API_KEY must not be forwarded: it would bill separately from the plan")
	}
}

func TestClaudeContainerAuth_RefusesWithoutALoginAndOnlyOptsInToAnAPIKeyExplicitly(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-x")
	t.Setenv("COMMANDER_ALLOW_API_KEY", "")
	err := checkClaudeContainerAuth()
	if err == nil || !strings.Contains(err.Error(), "claude setup-token") {
		t.Fatalf("expected an actionable error, got %v", err)
	}
	t.Setenv("COMMANDER_ALLOW_API_KEY", "1")
	if err := checkClaudeContainerAuth(); err != nil {
		t.Fatalf("explicit opt-in allows the API key: %v", err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("COMMANDER_ALLOW_API_KEY", "")
	if err := checkClaudeContainerAuth(); err != nil {
		t.Fatalf("the login token is enough: %v", err)
	}
}
