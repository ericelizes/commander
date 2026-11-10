package agentrt

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/opencode"
)

// ClaudeBinary is the Claude Code CLI looked up on PATH.
const ClaudeBinary = "claude"

// claudeAllowedTools pre-approves the commander CLIs every tier shells out to,
// matching opencode's default of running Bash without a prompt for them.
const claudeAllowedTools = "Bash(cmdr:*),Bash(br:*),Bash(bd:*),Bash(git:*)"

// claudeDisallowedTools denies the raw agent-mail MCP servers, as the opencode
// agents do: mail must go through `cmdr internal-mail` for role resolution.
const claudeDisallowedTools = "mcp__mcp-agent-mail,mcp__mcp_agent_mail"

// ClaudeAllowedTools and ClaudeDisallowedTools are the tool lists above, for
// other launchers of the same agents (the native chat engine).
const (
	ClaudeAllowedTools    = claudeAllowedTools
	ClaudeDisallowedTools = claudeDisallowedTools
)

// managerDisallowedTools are the edit tools a Manager never gets: owning a
// workstream is not doing its work (the Bash side is the PreToolUse guard).
const managerDisallowedTools = "Edit,Write,MultiEdit,NotebookEdit"

// IsManagerAgent reports whether agent is a workstream Manager (manager-*), not
// the Commander or an Operator.
func IsManagerAgent(agent string) bool { return strings.HasPrefix(agent, "manager-") }

// IsOperatorAgent reports whether agent is an Operator (operator-*).
func IsOperatorAgent(agent string) bool { return strings.HasPrefix(agent, "operator-") }

// operatorGuardHooks is the PreToolUse hook on Bash that keeps an Operator's full
// builds and suites inside the machine-wide build slot (`cmdr internal-operator-guard`).
func operatorGuardHooks(agent string) []map[string]any {
	return []map[string]any{{"matcher": "Bash", "hooks": []map[string]any{{
		"type":    "command",
		"command": "cmdr internal-operator-guard --role " + agent + " 2>/dev/null || true",
	}}}}
}

// ClaudeHostOperatorSettings is the inline --settings JSON for a headless host
// Operator: the build-slot guard, and mail. `--mail` delivers mail and nothing
// else, after every tool (a Manager's direction reaches a running Operator at its
// next tool call) and when it stops; with nothing signalled that costs two stats.
func ClaudeHostOperatorSettings(agent string) string {
	mail := func(event string) []map[string]any {
		return []map[string]any{{"hooks": []map[string]any{{
			"type":    "command",
			"command": "cmdr internal-mail-hook --role " + agent + " --event " + event + " --mail 2>/dev/null || true",
		}}}}
	}
	return marshalSettings(map[string]any{
		"PreToolUse":       append(operatorGuardHooks(agent), operatorStateHook(agent, "PreToolUse")...),
		"UserPromptSubmit": operatorStateHook(agent, "UserPromptSubmit"),
		"PostToolUse":      append(postToolUseHooks(agent, signedOutMatcher), mail("PostToolUse")...),
		"Stop":             mail("Stop"),
	})
}

func marshalSettings(hooks map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep ">" readable instead of >
	_ = enc.Encode(map[string]any{"hooks": hooks})
	return strings.TrimSpace(buf.String())
}

// ClaudeDisallowedToolsFor is the --disallowedTools list for agent: the agent
// mail MCP entries for everyone, plus the edit tools for Managers.
func ClaudeDisallowedToolsFor(agent string) string {
	if IsManagerAgent(agent) {
		return claudeDisallowedTools + "," + managerDisallowedTools
	}
	return claudeDisallowedTools
}

// ClaudePromptDirName is where commander keeps Claude role prompts, under
// the commander home. They are deliberately not Claude Code agents in
// ~/.claude/agents, which would surface every commander, manager and
// operator as a subagent in the user's unrelated Claude sessions.
const ClaudePromptDirName = "claude-agents"

// ClaudePromptDir returns (and creates) the role prompt directory.
func ClaudePromptDir() (string, error) {
	dir := filepath.Join(commanderHome(), ClaudePromptDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir prompt dir %s: %w", dir, err)
	}
	return dir, nil
}

// ClaudePromptPath is the role prompt file for agent.
func ClaudePromptPath(agent string) string {
	return filepath.Join(commanderHome(), ClaudePromptDirName, agent+".md")
}

// NewClaudeSessionID returns a random UUIDv4. Claude Code accepts a
// caller-chosen id via --session-id, so unlike opencode there is no
// capture-then-resume dance: commander mints the id and persists it.
func NewClaudeSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// claudeSessionExists reports whether projectsDir has a transcript for id.
// Claude only writes the transcript after the first message, so a freshly
// minted id must be opened with --session-id, and --resume only after that.
func claudeSessionExists(projectsDir, id string) bool {
	matches, _ := filepath.Glob(filepath.Join(projectsDir, "*", id+".jsonl"))
	return len(matches) > 0
}

// ClaudeMailHookSettings is the inline --settings JSON that makes Claude Code
// run `cmdr internal-mail-hook` before each prompt and when a turn stops, so
// unread mail is injected as system context instead of being typed into the
// chat pane. The commands contain no single quotes, so the JSON can be
// shell-quoted whole.
//
// Managers also get a PreToolUse hook on Bash: `cmdr internal-manager-guard`
// refuses the commands that do the work (commits, test runs, worktree access).
//
// Claude Code treats a hook exiting 2 as "block the prompt", and an older
// installed cmdr that does not know the subcommand exits 2. The `|| true` makes
// the hook inert in that case (and when cmdr is missing from PATH) instead of
// blocking every prompt until cmdr is reinstalled.
//
// Claude agents also get a PostToolUse hook on Bash that interrupts them when a
// cmdr call in the command printed an error, so a failed state change is never
// reported as done.
func ClaudeMailHookSettings(agent string) string {
	hooks := agentHooks(agent, true, "Bash")
	hooks["PostToolUse"] = postToolUseHooks(agent, "Bash", signedOutMatcher)
	return marshalSettings(hooks)
}

// signedOutMatcher is the tool a session offers in place of an MCP server that
// is signed out: calling it means the agent needed that server.
const signedOutMatcher = "mcp__.*__authenticate"

func postToolUseHooks(agent string, matchers ...string) []map[string]any {
	var out []map[string]any
	for _, m := range matchers {
		out = append(out, map[string]any{"matcher": m, "hooks": []map[string]any{{
			"type":    "command",
			"command": "cmdr internal-mail-hook --role " + agent + " --event PostToolUse 2>/dev/null || true",
		}}})
	}
	return out
}

// codexManagerMatcher is the PreToolUse matcher of a Manager on Codex: Codex
// has no --disallowedTools, so the guard also refuses its file edits
// (apply_patch, which Codex hooks also match as Edit and Write).
const codexManagerMatcher = "Bash|apply_patch|Edit|Write"

// agentHooks is the hooks table of agent: the mail hook (when mail is set) and
// the role's PreToolUse guard. managerMatcher is the tools the Manager guard sees.
func agentHooks(agent string, mail bool, managerMatcher string) map[string]any {
	hooks := map[string]any{}
	if mail {
		hook := func(event string) []map[string]any {
			return []map[string]any{{"hooks": []map[string]any{{
				"type":    "command",
				"command": "cmdr internal-mail-hook --role " + agent + " --event " + event + " 2>/dev/null || true",
			}}}}
		}
		hooks["UserPromptSubmit"], hooks["Stop"] = hook("UserPromptSubmit"), hook("Stop")
		// SessionStart (startup, resume, compaction) makes the next turn carry the whole board.
		hooks["SessionStart"] = hook("SessionStart")
	}
	if IsManagerAgent(agent) {
		hooks["PreToolUse"] = []map[string]any{{"matcher": managerMatcher, "hooks": []map[string]any{{
			"type":    "command",
			"command": "cmdr internal-manager-guard --role " + agent + " 2>/dev/null || true",
		}}}}
	}
	if IsOperatorAgent(agent) {
		hooks["PreToolUse"] = append(operatorGuardHooks(agent), operatorStateHook(agent, "PreToolUse")...)
		hooks["UserPromptSubmit"] = operatorStateHook(agent, "UserPromptSubmit")
		if !mail {
			hooks["Stop"] = operatorStateHook(agent, "Stop")
		}
	}
	return hooks
}

// CodexHookArgs are the `codex app-server` -c overrides that give a Codex
// session the hooks Claude gets: ClaudeMailHookSettings with mail, or (a
// one-shot Operator, which takes no mail) only the guard. Codex reads the same
// hook wire format as Claude (event names, tool_name Bash, the deny and block
// outputs). Session-flag hooks are untrusted until reviewed, so the bridge also
// starts its thread with bypass_hook_trust.
func CodexHookArgs(agent string, mail bool) []string {
	hooks := agentHooks(agent, mail, codexManagerMatcher)
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	var args []string
	for _, event := range events {
		args = append(args, "-c", "hooks."+event+"="+tomlValue(hooks[event]))
	}
	return args
}

// tomlValue writes v as a TOML inline value (strings, arrays of tables, tables).
// A JSON string is a valid TOML basic string.
func tomlValue(v any) string {
	switch v := v.(type) {
	case string:
		raw, _ := json.Marshal(v)
		return string(raw)
	case []map[string]any:
		parts := make([]string, len(v))
		for i, t := range v {
			parts[i] = tomlValue(t)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + tomlValue(v[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return fmt.Sprintf("%q", fmt.Sprint(v))
}

// ClaudeInteractiveCmd builds the tmux pane command for an interactive Claude
// Code chat running agent's role prompt on session id. Model is opencode-style
// ("anthropic/claude-sonnet-4-7") or a Claude alias; non-Anthropic models are
// dropped so Claude's default applies. A pane always runs on default, whatever
// the role's .account says: ~/.claude with any CLAUDE_CONFIG_DIR it inherited
// cleared, and the resume choice reads the same dir. A caller about to launch
// carries the transcript there first (claudeacct.CarryTranscript); this also
// builds the command for panes SoftRestart leaves running, so it must not.
func ClaudeInteractiveCmd(bin, agent, model, sessionID string) string {
	if bin == "" {
		bin = ClaudeBinary
	}
	// DISABLE_AUTOUPDATER keeps Claude's footer free of a red "Auto-update failed"
	// notice: the cockpit's Claude is updated by hand, not from inside a pane.
	parts := []string{"env", "-u", "CLAUDE_CONFIG_DIR", "DISABLE_AUTOUPDATER=1", bin, "-n", agent, "--append-system-prompt-file", shellQuote(ClaudePromptPath(agent))}
	if m := ClaudeModel(model); m != "" {
		parts = append(parts, "--model", m)
	}
	// the user's call: no permission prompts in the cockpit. The never-merge/
	// deploy/prod guardrails live in the role prompts.
	parts = append(parts,
		"--dangerously-skip-permissions",
		"--disallowedTools", shellQuote(ClaudeDisallowedToolsFor(agent)),
		"--settings", shellQuote(ClaudeMailHookSettings(agent)),
	)
	if claudeSessionExists(claudeacct.ProjectsDir("", claudeacct.Default), sessionID) {
		parts = append(parts, "--resume", sessionID)
	} else {
		parts = append(parts, "--session-id", sessionID)
	}
	return strings.Join(parts, " ")
}

// ClaudeHeadlessArgs returns argv for a one-shot `claude -p` operator run
// that streams nd-JSON on stdout. promptPath is the role prompt as seen by
// the process (it differs inside a container). skipPermissions is for the
// Docker sandbox only; host runs auto-accept edits and the commander CLIs.
func ClaudeHeadlessArgs(agent, promptPath, model, sessionID, prompt string, skipPermissions bool) []string {
	// The prompt goes right after -p: --disallowedTools / --allowedTools are
	// variadic and would swallow a trailing prompt as another tool name.
	args := []string{ClaudeBinary, "-p", prompt, "--output-format", "stream-json", "--verbose",
		"-n", agent, "--append-system-prompt-file", promptPath}
	if m := ClaudeModel(model); m != "" {
		args = append(args, "--model", m)
	}
	args = append(args, "--session-id", sessionID, "--disallowedTools", ClaudeDisallowedToolsFor(agent))
	if skipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode", "acceptEdits", "--allowedTools", claudeAllowedTools)
	}
	return args
}

// ClaudeModel maps an opencode-style model to a Claude Code --model value.
// Family names become aliases (which track the latest release) so stale
// pinned versions in config never fail; other Anthropic ids pass through.
func ClaudeModel(model string) string {
	m := strings.TrimSpace(model)
	if provider, rest, ok := strings.Cut(m, "/"); ok {
		if provider != "anthropic" {
			return ""
		}
		m = rest
	}
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, family) {
			return family
		}
	}
	return m
}

// ClaudeEventMapper converts Claude Code stream-json lines into the opencode
// Event shape the ceilings watchdog already understands:
//
//   - assistant → step_finish carrying that message's input+output tokens.
//     Claude repeats an assistant message once per content block with the
//     same usage, so tokens are counted once per message id.
//   - result → step_finish carrying the cost increase since the previous result
//     (total_cost_usd is cumulative per session), or error when is_error is set.
//     A run whose assistant messages carry no usage (the Codex bridge, which
//     reports $0 on a ChatGPT plan) is counted from the result's usage instead,
//     so the token ceiling still caps it.
//
// One mapper per run; it is not safe for concurrent use.
type ClaudeEventMapper struct {
	seen map[string]bool
	// lastTotal is the previous result's total_cost_usd: Claude reports cost
	// cumulatively per session (and a resumed session starts from its history),
	// so each step_finish carries only the increase.
	lastTotal float64
	// assistantUsage is set once an assistant message carried tokens: then the
	// result's usage repeats them and is not counted again.
	assistantUsage bool
}

// NewClaudeEventMapper returns a mapper with empty dedupe state.
func NewClaudeEventMapper() *ClaudeEventMapper {
	return &ClaudeEventMapper{seen: map[string]bool{}}
}

type claudeEvent struct {
	Type         string  `json:"type"`
	SessionID    string  `json:"session_id"`
	IsError      bool    `json:"is_error"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Message struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Map parses line. ok is false when line is not a Claude event (so callers
// fall back to opencode.ParseEvent) or carries nothing the watchdog uses.
func (m *ClaudeEventMapper) Map(line []byte) (ev opencode.Event, ok bool) {
	var ce claudeEvent
	if err := json.Unmarshal(line, &ce); err != nil || ce.SessionID == "" {
		return opencode.Event{}, false
	}
	ev.SessionID = ce.SessionID
	switch ce.Type {
	case "assistant":
		if ce.Message.ID == "" || m.seen[ce.Message.ID] {
			ev.Type = "claude_" + ce.Type
			return ev, true
		}
		m.seen[ce.Message.ID] = true
		ev.Type = "step_finish"
		ev.Part.Tokens.Input = ce.Message.Usage.InputTokens
		ev.Part.Tokens.Output = ce.Message.Usage.OutputTokens
		if ev.Part.Tokens.Input+ev.Part.Tokens.Output > 0 {
			m.assistantUsage = true
		}
	case "result":
		if ce.IsError {
			ev.Type = "error"
		} else {
			ev.Type = "step_finish"
			if delta := ce.TotalCostUSD - m.lastTotal; delta > 0 {
				ev.Part.Cost = delta
			}
			if ce.TotalCostUSD > m.lastTotal {
				m.lastTotal = ce.TotalCostUSD
			}
			if !m.assistantUsage {
				ev.Part.Tokens.Input, ev.Part.Tokens.Output = ce.Usage.InputTokens, ce.Usage.OutputTokens
			}
		}
	default:
		ev.Type = "claude_" + ce.Type
	}
	return ev, true
}

// EventSessionID returns the session id carried by an nd-JSON line from
// either CLI (opencode "sessionID", Claude "session_id"), or "".
func EventSessionID(line []byte) string {
	var ev struct {
		Opencode string `json:"sessionID"`
		Claude   string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return ""
	}
	if ev.Opencode != "" {
		return ev.Opencode
	}
	return ev.Claude
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func operatorStateHook(agent, event string) []map[string]any {
	return []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": "cmdr internal-state-hook --role " + agent + " --event " + event + " 2>/dev/null || true"}}}}
}
