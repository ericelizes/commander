package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentrt"
)

// useClaudeRuntime switches every tier to Claude and isolates Claude's config
// dir so tests never touch the real ~/.claude.
func useClaudeRuntime(t *testing.T) string {
	t.Helper()
	t.Setenv(agentrt.EnvVar, string(agentrt.Claude))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return filepath.Join(home, ".claude")
}

// claudeTools is the permission/hook flag block of a Claude chat launch for
// role: the hooks run `cmdr internal-mail-hook` so mail and the board are
// injected as system context instead of being typed into the pane.
func claudeTools(role string) string {
	hook := func(event string) string {
		return `[{"hooks":[{"command":"cmdr internal-mail-hook --role ` + role + ` --event ` + event + ` 2>/dev/null || true","type":"command"}]}]`
	}
	disallowed, pre := "mcp__mcp-agent-mail,mcp__mcp_agent_mail", ""
	if strings.HasPrefix(role, "manager-") { // Managers: no edit tools, Bash guard hook
		disallowed += ",Edit,Write,MultiEdit,NotebookEdit"
		pre = `"PreToolUse":[{"hooks":[{"command":"cmdr internal-manager-guard --role ` + role + ` 2>/dev/null || true","type":"command"}],"matcher":"Bash"}],`
	}
	return "--dangerously-skip-permissions --disallowedTools '" + disallowed + "'" +
		` --settings '{"hooks":{"PostToolUse":[{"hooks":[{"command":"cmdr internal-mail-hook --role ` + role + ` --event PostToolUse 2>/dev/null || true","type":"command"}],"matcher":"Bash"},` +
		`{"hooks":[{"command":"cmdr internal-mail-hook --role ` + role + ` --event PostToolUse 2>/dev/null || true","type":"command"}],"matcher":"mcp__.*__authenticate"}],` + pre + `"SessionStart":` + hook("SessionStart") + `,"Stop":` + hook("Stop") + `,"UserPromptSubmit":` + hook("UserPromptSubmit") + `}}'`
}

func paneRunner(chatPane string) *MockRunner {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "has-session":
			return nil, []byte("can't find session: " + CommanderSessionName), errExit1
		case len(args) >= 1 && (args[0] == "new-session" || args[0] == "new-window"):
			return []byte(chatPane + "\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			return []byte("%9\n"), nil, nil
		}
		return nil, nil, nil
	})
	return mr
}

func TestLaunchCommander_ClaudeRuntimeMintsSessionAndAgent(t *testing.T) {
	claudeDir := useClaudeRuntime(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	t.Setenv("COMMANDER_WORKSPACE_ROOT", workspaceRoot)

	mr := paneRunner("%1")
	d := NewDirectorWith(mr)
	if err := d.LaunchCommander(context.Background(), home, false); err != nil {
		t.Fatalf("LaunchCommander: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "commander.claude.session-id"))
	if err != nil {
		t.Fatalf("claude session id not persisted: %v", err)
	}
	id := strings.TrimSpace(string(raw))
	if _, err := os.Stat(filepath.Join(home, CommanderSessionFileName)); !os.IsNotExist(err) {
		t.Errorf("opencode session file should not be written under the claude runtime")
	}
	promptPath := filepath.Join(home, "claude-agents", "commander.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Errorf("claude commander prompt not installed: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(claudeDir, "agents")); len(entries) != 0 {
		t.Errorf("nothing may be written to ~/.claude/agents; found %v", entries)
	}
	mustContainCall(t, mr.CallStrings(), "tmux new-session -d -P -F #{pane_id} -s commander -n commander -c "+workspaceRoot+
		" env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude -n commander --append-system-prompt-file '"+promptPath+"' --model opus "+claudeTools("commander")+" --session-id "+id)
}

func TestCommanderFleetWindowHealthy_RuntimeSwitchForcesRebuild(t *testing.T) {
	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "list-windows":
			return []byte("commander\n"), nil, nil
		case len(args) >= 1 && args[0] == "list-panes":
			return []byte("cmdr __column cmdr-objectives\nopencode --session ses_1 --agent commander\ncmdr __column cmdr-fleet\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)

	healthy, err := d.commanderFleetWindowHealthy(context.Background())
	if err != nil || !healthy {
		t.Fatalf("opencode window under opencode runtime: healthy=%v err=%v", healthy, err)
	}
	useClaudeRuntime(t)
	healthy, err = d.commanderFleetWindowHealthy(context.Background())
	if err != nil || healthy {
		t.Fatalf("opencode window under claude runtime should be unhealthy: healthy=%v err=%v", healthy, err)
	}
}

func TestLaunchLead_ClaudeRuntimeResumesExistingTranscript(t *testing.T) {
	claudeDir := useClaudeRuntime(t)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	tmp := t.TempDir()
	commanderDir := filepath.Join(tmp, ".commander")
	if err := os.MkdirAll(commanderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"
	if err := os.WriteFile(filepath.Join(commanderDir, "manager.claude.session-id"), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transcriptDir := filepath.Join(claudeDir, "projects", "-some-project")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		switch {
		case len(args) >= 1 && args[0] == "new-window":
			return []byte("%11\n"), nil, nil
		case len(args) >= 1 && args[0] == "split-window":
			return []byte("%12\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)
	if err := d.LaunchLead(context.Background(), "Frontend", tmp, false); err != nil {
		t.Fatalf("LaunchLead: %v", err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n manager (Frontend) -c "+tmp+
		" env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude -n manager-frontend --append-system-prompt-file '"+filepath.Join(home, "claude-agents", "manager-frontend.md")+"' --model sonnet "+claudeTools("manager-frontend")+" --resume "+id)
}

func TestLaunchOperator_OpensOperatorUnderTheRuntimeItWasSpawnedWith(t *testing.T) {
	useClaudeRuntime(t)
	t.Setenv("COMMANDER_HOME", t.TempDir())
	tmp := t.TempDir()
	opsDir := filepath.Join(tmp, ".commander", "operators")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Spawned under opencode before the switch: only the opencode file exists.
	if err := os.WriteFile(filepath.Join(opsDir, "fro-001.session-id"), []byte("ses_op999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mr := &MockRunner{}
	mr.SetRunFunc(func(args []string) ([]byte, []byte, error) {
		if len(args) >= 1 && (args[0] == "new-window" || args[0] == "split-window") {
			return []byte("%21\n"), nil, nil
		}
		return nil, nil, nil
	})
	d := NewDirectorWith(mr)
	if err := d.LaunchOperator(context.Background(), "fro-001", "frontend", tmp, false); err != nil {
		t.Fatalf("LaunchOperator: %v", err)
	}
	mustContainCall(t, mr.CallStrings(), "tmux new-window -P -F #{pane_id} -t commander: -n op-fro-001 -c "+tmp+" opencode --session ses_op999")
}
