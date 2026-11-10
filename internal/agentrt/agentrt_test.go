package agentrt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFor_ResolutionOrder(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv("COMMANDER_HOME", t.TempDir())
	if got := For(TierLead); got != Opencode {
		t.Errorf("no config: got %q, want opencode", got)
	}

	writeConfig(t, "[runtime]\ndefault = \"claude\"\noperator = \"opencode\"\n")
	if got := For(TierCommander); got != Claude {
		t.Errorf("default=claude: commander got %q", got)
	}
	if got := For(TierOperator); got != Opencode {
		t.Errorf("per-tier override: operator got %q", got)
	}

	t.Setenv(EnvVar, "opencode")
	if got := For(TierCommander); got != Opencode {
		t.Errorf("env override: got %q", got)
	}
}

func TestSessionFileName(t *testing.T) {
	if got := SessionFileName("manager", Opencode); got != "manager.session-id" {
		t.Errorf("opencode: %q", got)
	}
	if got := SessionFileName("manager", Claude); got != "manager.claude.session-id" {
		t.Errorf("claude: %q", got)
	}
}

func TestClaudeModel(t *testing.T) {
	for in, want := range map[string]string{
		"anthropic/claude-opus-4-7":   "opus",
		"anthropic/claude-sonnet-4-5": "sonnet",
		"claude-haiku-4-5":            "haiku",
		"sonnet":                      "sonnet",
		"anthropic/claude-custom":     "claude-custom",
		"opencode/big-pickle":         "",
		"":                            "",
	} {
		if got := ClaudeModel(in); got != want {
			t.Errorf("ClaudeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeInteractiveCmd_RegistersMailHooksForItsRole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COMMANDER_HOME", t.TempDir())
	got := ClaudeInteractiveCmd("", "manager-ws-1", "sonnet", "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f")

	_, rest, ok := strings.Cut(got, " --settings '")
	if !ok {
		t.Fatalf("no --settings in %q", got)
	}
	raw, _, _ := strings.Cut(rest, "' --")
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatalf("settings not JSON: %q: %v", raw, err)
	}
	for _, event := range []string{"UserPromptSubmit", "Stop", "SessionStart"} {
		groups := settings.Hooks[event]
		// "|| true": an old cmdr exits 2 on the unknown subcommand, which would block every prompt.
		want := "cmdr internal-mail-hook --role manager-ws-1 --event " + event + " 2>/dev/null || true"
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || groups[0].Hooks[0].Command != want {
			t.Errorf("%s hook = %+v, want command %q", event, groups, want)
		}
	}
	// The pre-existing flags are untouched.
	for _, flag := range []string{"-n manager-ws-1", "--append-system-prompt-file", "--model sonnet", "--dangerously-skip-permissions", "--disallowedTools 'mcp__mcp-agent-mail,mcp__mcp_agent_mail,Edit,Write,MultiEdit,NotebookEdit'", "--session-id "} {
		if !strings.Contains(got, flag) {
			t.Errorf("missing %q in %q", flag, got)
		}
	}
}

func TestClaudeInteractiveCmd_SessionIDUntilTranscriptExists(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	dir := filepath.Join(user, ".claude")
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	const id = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"

	got := ClaudeInteractiveCmd("", "commander", "opencode/big-pickle", id)
	wantPrefix := "env -u CLAUDE_CONFIG_DIR DISABLE_AUTOUPDATER=1 claude -n commander --append-system-prompt-file '" + filepath.Join(home, "claude-agents", "commander.md") + "' --dangerously-skip-permissions "
	if !strings.HasPrefix(got, wantPrefix) || strings.Contains(got, "--model") || !strings.HasSuffix(got, " --session-id "+id) {
		t.Errorf("fresh session cmd = %q", got)
	}

	projDir := filepath.Join(dir, "projects", "-x")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, id+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeInteractiveCmd("", "commander", "", id); !strings.HasSuffix(got, " --resume "+id) {
		t.Errorf("existing transcript cmd = %q", got)
	}
}

func TestClaudeHeadlessArgs(t *testing.T) {
	host := ClaudeHeadlessArgs("operator-x", "/prompts/operator-x.md", "anthropic/claude-sonnet-4-7", "id-1", "go", false)
	for _, want := range []string{"-p", "stream-json", "--verbose", "acceptEdits", "--session-id", "/prompts/operator-x.md", "--disallowedTools"} {
		if !slices.Contains(host, want) {
			t.Errorf("host args missing %q: %v", want, host)
		}
	}
	// --allowedTools/--disallowedTools are variadic: a trailing prompt is swallowed as a tool name.
	if host[2] != "go" {
		t.Errorf("prompt must directly follow -p: %v", host)
	}
	sandbox := ClaudeHeadlessArgs("operator-x", "/p.md", "", "id-1", "go", true)
	if !slices.Contains(sandbox, "--dangerously-skip-permissions") || slices.Contains(sandbox, "acceptEdits") {
		t.Errorf("sandbox args: %v", sandbox)
	}
}

func TestClaudeEventMapper(t *testing.T) {
	m := NewClaudeEventMapper()
	assistant := `{"type":"assistant","session_id":"s","message":{"id":"msg_1","usage":{"input_tokens":10,"output_tokens":5}}}`

	ev, ok := m.Map([]byte(assistant))
	if !ok || ev.Type != "step_finish" || ev.Part.Tokens.Input != 10 || ev.Part.Tokens.Output != 5 {
		t.Fatalf("first assistant event = %+v ok=%v", ev, ok)
	}
	if ev, _ := m.Map([]byte(assistant)); ev.Type == "step_finish" {
		t.Errorf("repeated message id must not be billed twice")
	}

	ev, _ = m.Map([]byte(`{"type":"result","session_id":"s","is_error":false,"total_cost_usd":0.25}`))
	if ev.Type != "step_finish" || ev.Part.Cost != 0.25 || ev.Part.Tokens.Input != 0 {
		t.Errorf("result event = %+v", ev)
	}
	if ev, _ := m.Map([]byte(`{"type":"result","session_id":"s","is_error":true}`)); ev.Type != "error" {
		t.Errorf("error result type = %q", ev.Type)
	}
	if _, ok := m.Map([]byte(`{"type":"step_finish","sessionID":"ses_1","part":{"cost":1}}`)); ok {
		t.Errorf("opencode events must fall through to opencode.ParseEvent")
	}
}

func TestEventSessionID(t *testing.T) {
	if got := EventSessionID([]byte(`{"sessionID":"ses_1"}`)); got != "ses_1" {
		t.Errorf("opencode: %q", got)
	}
	if got := EventSessionID([]byte(`{"type":"system","session_id":"abc"}`)); got != "abc" {
		t.Errorf("claude: %q", got)
	}
	if got := EventSessionID([]byte(`not json`)); got != "" {
		t.Errorf("garbage: %q", got)
	}
}

func TestNewClaudeSessionID_IsUUIDv4(t *testing.T) {
	id := NewClaudeSessionID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 || len(id) != 36 || id[14] != '4' {
		t.Errorf("not a v4 uuid: %q", id)
	}
	if id == NewClaudeSessionID() {
		t.Errorf("ids must be random")
	}
}

func TestClaudeEventMapper_CostIsADeltaOfTheCumulativeTotal(t *testing.T) {
	m := NewClaudeEventMapper()
	var got []float64
	for _, line := range []string{
		`{"type":"result","session_id":"s","total_cost_usd":0.005}`,
		`{"type":"result","session_id":"s","total_cost_usd":0.0066}`,
		`{"type":"result","session_id":"s","total_cost_usd":0.0066}`,
		`{"type":"result","session_id":"s","total_cost_usd":0.0071}`,
	} {
		ev, ok := m.Map([]byte(line))
		if !ok {
			t.Fatal("not mapped")
		}
		got = append(got, ev.Part.Cost)
	}
	var sum float64
	for _, c := range got {
		sum += c
	}
	if got[2] != 0 || sum < 0.00709 || sum > 0.00711 {
		t.Errorf("deltas %v sum to %v, want 0.0071 total", got, sum)
	}
}

func TestManagerLaunchDisallowsEditToolsAndGuardsBash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COMMANDER_HOME", t.TempDir())
	const id = "0b6f1c1e-2a4d-4c3b-9e8f-1a2b3c4d5e6f"
	edits := "mcp__mcp-agent-mail,mcp__mcp_agent_mail,Edit,Write,MultiEdit,NotebookEdit"

	mgr := ClaudeInteractiveCmd("", "manager-ws-1", "sonnet", id)
	if !strings.Contains(mgr, "--disallowedTools '"+edits+"'") {
		t.Errorf("manager cmd lacks edit tools in --disallowedTools: %s", mgr)
	}
	if !strings.Contains(mgr, `"PreToolUse":[{"hooks":[{"command":"cmdr internal-manager-guard --role manager-ws-1 2>/dev/null || true","type":"command"}],"matcher":"Bash"}]`) {
		t.Errorf("manager cmd lacks the Bash guard hook: %s", mgr)
	}
	for _, agent := range []string{"commander", "operator-feat-x"} {
		got := ClaudeInteractiveCmd("", agent, "sonnet", id)
		if strings.Contains(got, ",Edit,") {
			t.Errorf("%s must keep edit tools: %s", agent, got)
		}
		if strings.Contains(got, "PreToolUse") != (agent != "commander") { // operators get the build-slot guard
			t.Errorf("%s guard hook mismatch: %s", agent, got)
		}
	}
	if got := strings.Join(ClaudeHeadlessArgs("manager-ws-1", "p.md", "", id, "go", false), " "); !strings.Contains(got, edits) {
		t.Errorf("headless manager args: %s", got)
	}
	if got := strings.Join(ClaudeHeadlessArgs("operator-x", "p.md", "", id, "go", false), " "); strings.Contains(got, ",Edit,") {
		t.Errorf("headless operator args: %s", got)
	}
}

func TestOperatorsGetTheBuildSlotGuard(t *testing.T) {
	for _, s := range []string{ClaudeMailHookSettings("operator-op-x"), ClaudeHostOperatorSettings("operator-op-x")} {
		if !strings.Contains(s, `"matcher":"Bash"`) || !strings.Contains(s, "cmdr internal-operator-guard --role operator-op-x") {
			t.Errorf("operator settings lack the guard: %s", s)
		}
	}
	if s := ClaudeMailHookSettings("commander"); strings.Contains(s, "operator-guard") {
		t.Errorf("commander must not get the operator guard: %s", s)
	}
	if s := ClaudeMailHookSettings("manager-x"); strings.Contains(s, "operator-guard") {
		t.Errorf("manager must not get the operator guard: %s", s)
	}
}

// CodexHookArgs are -c overrides whose values are valid TOML holding the same
// hooks Claude gets (Codex has no --disallowedTools, so a Manager's guard also
// matches apply_patch/Edit/Write).
func TestCodexHookArgsMirrorClaudeHooks(t *testing.T) {
	type hook struct {
		Matcher string `toml:"matcher"`
		Hooks   []struct {
			Type    string `toml:"type"`
			Command string `toml:"command"`
		} `toml:"hooks"`
	}
	parse := func(args []string) map[string][]hook {
		out := map[string][]hook{}
		for i := 0; i < len(args); i += 2 {
			if args[i] != "-c" {
				t.Fatalf("not a -c pair: %v", args)
			}
			key, val, _ := strings.Cut(args[i+1], "=")
			var doc struct{ V []hook }
			if _, err := toml.Decode("V = "+val, &doc); err != nil {
				t.Fatalf("%s is not TOML: %v", args[i+1], err)
			}
			out[strings.TrimPrefix(key, "hooks.")] = doc.V
		}
		return out
	}
	mgr := parse(CodexHookArgs("manager-ws", true))
	if got := mgr["PreToolUse"]; len(got) != 1 || got[0].Matcher != "Bash|apply_patch|Edit|Write" ||
		got[0].Hooks[0].Command != "cmdr internal-manager-guard --role manager-ws 2>/dev/null || true" {
		t.Errorf("manager PreToolUse: %+v", got)
	}
	if got := mgr["Stop"]; len(got) != 1 || got[0].Hooks[0].Command != "cmdr internal-mail-hook --role manager-ws --event Stop 2>/dev/null || true" {
		t.Errorf("manager Stop: %+v", got)
	}
	if _, ok := mgr["UserPromptSubmit"]; !ok {
		t.Error("manager has no UserPromptSubmit mail hook")
	}
	op := parse(CodexHookArgs("operator-op-x", false))
	if len(op) != 3 || op["Stop"] == nil || op["UserPromptSubmit"] == nil || op["PreToolUse"][0].Matcher != "Bash" || !strings.Contains(op["PreToolUse"][0].Hooks[0].Command, "internal-operator-guard --role operator-op-x") {
		t.Errorf("one-shot operator: %+v", op)
	}
	if cmdr := parse(CodexHookArgs("commander", true)); len(cmdr) != 3 || cmdr["PreToolUse"] != nil || cmdr["SessionStart"] == nil {
		t.Errorf("commander gets mail hooks only: %+v", cmdr)
	}
	// Claude's settings are unchanged: the Manager guard there matches Bash only.
	if s := ClaudeMailHookSettings("manager-ws"); !strings.Contains(s, `"matcher":"Bash"`) {
		t.Errorf("claude manager settings: %s", s)
	}
}

// The share env override is a person's: in an agent's process (a chat role,
// or a host Operator with only COMMANDER_MANAGER_ROLE) only config counts.
func TestShareEnvOverrideIsIgnoredForAgents(t *testing.T) {
	t.Setenv("COMMANDER_HOME", t.TempDir())
	t.Setenv(CodexShareEnvVar, "1")
	t.Setenv(CodexManagerShareEnvVar, "1")
	t.Setenv("COMMANDER_ROLE", "")
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	if AgentRole() != "" || CodexOperatorShare() != 1 || CodexManagerShare() != 1 {
		t.Fatalf("a person's override: role %q, shares %v %v", AgentRole(), CodexOperatorShare(), CodexManagerShare())
	}
	t.Setenv("COMMANDER_MANAGER_ROLE", "manager-ws")
	if AgentRole() != "an Operator of manager-ws" || CodexOperatorShare() != 0 || CodexManagerShare() != 0 {
		t.Fatalf("host Operator: role %q, shares %v %v", AgentRole(), CodexOperatorShare(), CodexManagerShare())
	}
	t.Setenv("COMMANDER_ROLE", "manager-ws")
	if AgentRole() != "manager-ws" || CodexOperatorShare() != 0 {
		t.Fatalf("Manager: role %q, share %v", AgentRole(), CodexOperatorShare())
	}
}

// A host Operator keeps its guard and takes mail after every tool and at Stop;
// a Codex one-shot, which takes no message after its prompt, stays guard-only.
func TestHostOperatorSettingsCarryTheMailHook(t *testing.T) {
	const agent = "operator-op-x"
	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(ClaudeHostOperatorSettings(agent)), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"PostToolUse", "Stop"} {
		found := false
		for _, e := range parsed.Hooks[event] {
			for _, h := range e.Hooks {
				// No matcher: every tool, or a long Edit-only stretch would miss it.
				if e.Matcher == "" && h.Command == "cmdr internal-mail-hook --role "+agent+" --event "+event+" --mail 2>/dev/null || true" {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s has no mail hook: %+v", event, parsed.Hooks[event])
		}
	}
	if pre := parsed.Hooks["PreToolUse"]; len(pre) != 2 || pre[0].Matcher != "Bash" || !strings.Contains(pre[0].Hooks[0].Command, "cmdr internal-operator-guard --role "+agent) {
		t.Errorf("guard changed: %+v", pre)
	}
	if !strings.Contains(fmt.Sprint(parsed.Hooks["PostToolUse"]), signedOutMatcher) {
		t.Errorf("signed-out hook lost: %+v", parsed.Hooks["PostToolUse"])
	}
	if args := strings.Join(CodexHookArgs(agent, false), " "); strings.Contains(args, "mail-hook") || !strings.Contains(args, "internal-operator-guard") {
		t.Errorf("codex one-shot hooks: %s", args)
	}
}
