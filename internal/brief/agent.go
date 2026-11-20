package brief

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/harddeny"
	"github.com/example-user/commander/internal/overview"
)

// The Brief's writer is an agent: it starts from the gathered evidence, digs in
// with read-only commands, then writes the document. It has only the Bash tool,
// and only these commands (claude's --allowedTools, then GuardCommand in a
// PreToolUse hook, which also counts calls against MaxToolCalls).
var readCommands = []string{
	"cmdr workstream-statuses",
	"cmdr internal-brief-read",
	"gh pr view",
	"gh pr checks",
	"gh pr list",
	"gh run view",
	"gh run list",
	"gh api --method GET",
}

// MaxToolCalls bounds one run's investigation; past it every command is refused
// with a note to write the Brief now. MinToolCalls is the least it must check
// before its answer is taken: the briefing alone is a summary, not proof.
const (
	MaxToolCalls = 40
	MinToolCalls = 10
)

// RunDirEnv names the run's scratch dir, where the guard counts tool calls.
const RunDirEnv = "COMMANDER_BRIEF_RUN"

// AllowedTools is the run's --allowedTools: each read command as a Bash
// prefix, and StructuredOutput, the tool its final answer comes back through.
func AllowedTools() string {
	tools := []string{"StructuredOutput"}
	for _, c := range readCommands {
		tools = append(tools, "Bash("+c+":*)")
	}
	return strings.Join(tools, ",")
}

// GuardCommand reports whether the writer may run command: one plain command
// (no pipes, chains, redirects or substitutions) starting with a read command,
// that no hard limit refuses. gh api reads only: no method change, fields or input.
func GuardCommand(command string) (bool, string) {
	c := strings.TrimSpace(command)
	if c == "" {
		return false, "empty command"
	}
	if why := shellMeta(c); why != "" {
		return false, why
	}
	ok := false
	for _, p := range readCommands {
		if c == p || strings.HasPrefix(c, p+" ") {
			ok = true
		}
	}
	if !ok {
		return false, "only these read-only commands may run: " + strings.Join(readCommands, "; ")
	}
	if strings.HasPrefix(c, "gh api") {
		f := strings.Fields(c)
		methods := 0
		for _, w := range f {
			switch {
			case w == "-X" || strings.HasPrefix(w, "-X") || strings.HasPrefix(w, "--method"):
				methods++
			case w == "-f" || w == "-F" || strings.HasPrefix(w, "--field") || strings.HasPrefix(w, "--raw-field") || strings.HasPrefix(w, "--input"):
				return false, "gh api may only read: no fields or input"
			}
		}
		if methods != 1 {
			return false, "gh api may only read: one --method GET"
		}
	}
	if v := harddeny.Check(c, harddeny.Context{}); v.Denied() {
		return false, "hard limit: " + v.Why
	}
	return true, ""
}

// shellMeta names the shell feature that would let a command do more than its
// prefix says: chains, pipes, redirects, substitution, expansion, newlines.
// Quoted text may hold < and > (a SQL comparison); $ and backticks never.
func shellMeta(c string) string {
	quote := rune(0)
	for _, r := range c {
		switch {
		case r == '\n' || r == '\r':
			return "one command per call, on one line"
		case r == '$' || r == '`':
			return "no expansion or substitution ($, backticks)"
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case strings.ContainsRune(";&|<>(){}\\", r):
			return "one plain command: no chains, pipes, redirects or subshells"
		}
	}
	if quote != 0 {
		return "unbalanced quotes"
	}
	return ""
}

// Guard is the PreToolUse decision for a hook input: "" lets it run, else the
// reason it is refused. dir is the run's scratch dir (calls are counted there).
func Guard(command, dir string) string {
	if dir != "" {
		f, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, strings.ReplaceAll(command, "\n", " "))
			f.Close()
		}
		if ToolCalls(dir) > MaxToolCalls {
			return fmt.Sprintf("the investigation budget (%d commands) is spent: write the Brief now from what you have", MaxToolCalls)
		}
	}
	if ok, why := GuardCommand(command); !ok {
		return "The Brief's writer only reads: " + why + "."
	}
	return ""
}

// GuardAnswer is the PreToolUse decision for the final answer (StructuredOutput):
// "" takes it, else why it is refused (too few checks so far).
func GuardAnswer(dir string) string {
	if n := ToolCalls(dir); n < MinToolCalls {
		return fmt.Sprintf("Not yet: investigate first. You have run %d of at least %d checks: read the live board, verify every shipped, deployed or merged claim, and confirm who each Needs me and Waiting on item waits on. Then return the Brief.", n, MinToolCalls)
	}
	return ""
}

// ToolCalls is how many commands a run tried.
func ToolCalls(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		return 0
	}
	return bytes.Count(b, []byte("\n"))
}

// AgentPrompt is the writer's system prompt: how to investigate, then Prompt's writing rules.
const AgentPrompt = `You are the writer of a person's Brief. You get a briefing (JSON on stdin): the time windows, the workstreams with their goals and progress, and evidence records gathered for you: the board's open steps and their owners, beads (work items) finished or changed in the windows, agents' final reports, and Linear tickets. That briefing is where you start, not all there is.
INVESTIGATE FIRST, then write. Do not write from the briefing alone: it is a summary and can be stale or wrong. You MUST:
1. Run cmdr workstream-statuses to read the live board: who owns each open step now (an owner "You" is the person) and what each waits on.
2. Before you say anything was shipped, deployed, rolled out or merged, check it: the step's final report or close reason (sql on bead_comments / bead_tasks), and for a pull request, gh pr view (merged or not) and the deploy run (gh run list / gh run view). If you cannot confirm a deploy, say merged, built or reviewed instead.
3. For each candidate for Needs me or Waiting on, confirm who it waits on with cmdr workstream-statuses --step <bead-id> or gh pr view (whose review is requested).
4. Read the final reports of the biggest items of yesterday to tell what was built from what was only reviewed.
Use between 10 and 30 commands; your answer is refused before 10. Stop when the picture is clear.
Your only tool is Bash, and only these read-only commands run; anything else is refused. One plain command per call: no pipes, ;, &&, redirects, $ or subshells.
- cmdr workstream-statuses   (the board as text: every workstream, its open steps by column, owners, waits, bead ids); add --step <bead-id> for one step's full detail.
- cmdr internal-brief-read sql "SELECT ..."   (read-only Postgres; one SELECT or WITH query, at most 200 rows, JSON lines). Tables: projects(id, name); bead_tasks(project_id, id, parent_id, title, description, acceptance_criteria, notes, status, labels jsonb, close_reason, created_at, updated_at text RFC 3339 UTC, deleted_at); bead_comments(project_id, bead_id, author, text, created_at). Final reports are comments starting "Final report:". Use jsonb_exists(labels, 'kind:workstream') for labels.
- cmdr internal-brief-read linear <TICKET-ID>   (one Linear ticket: status, description, recent comments).
- gh pr view <n> -R <owner/repo> [--json ...], gh pr checks, gh pr list -R <owner/repo> --search ..., gh run list -R <owner/repo>, gh run view <id> -R <owner/repo>, gh api --method GET <path>.
` + promptRules

// AgentSchema is the agent's final answer: the document.
func AgentSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"markdown"}, "properties": map[string]any{
		"markdown": map[string]any{"type": "string", "minLength": 1, "maxLength": 20000},
	}}
}

// Agent is the Brief's writer: claude on model with only the read commands
// (allowlisted, then guarded by `cmdr internal-brief-guard` with the hard
// deny-list), no MCP servers, user settings or session; the briefing on stdin.
// ctx bounds its time. It reports the document and how many commands it ran.
func Agent(model, prompt string, accountEnv []string) Generate {
	return func(ctx context.Context, in Input) (string, error) {
		md, _, err := RunAgent(ctx, model, prompt, in, accountEnv)
		return md, err
	}
}

// RunAgent is Agent that also reports how many commands the run tried.
func RunAgent(ctx context.Context, model, prompt string, in Input, accountEnv []string) (string, int, error) {
	dir, err := os.MkdirTemp("", "commander-brief-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(dir)
	payload, _ := json.Marshal(in)
	schema, _ := json.Marshal(AgentSchema())
	settings, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []map[string]any{{"matcher": "Bash|StructuredOutput", "hooks": []map[string]any{{
		"type": "command", "command": "cmdr internal-brief-guard",
	}}}}}})
	// --allowedTools is variadic: it goes last, after --.
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--system-prompt", prompt,
		"--tools", "Bash", "--permission-mode", "dontAsk",
		"--setting-sources", "", "--settings", string(settings),
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence",
		"--output-format", "json", "--json-schema", string(schema),
		"--allowedTools", AllowedTools(), "--",
		"Read the briefing on stdin, investigate with your read-only commands, then return the Brief.")
	claudeacct.Graceful(cmd)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(overview.ReaderEnv(os.Environ(), accountEnv), RunDirEnv+"="+dir, "CLAUDE_CODE_MAX_OUTPUT_TOKENS=16000")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	calls := ToolCalls(dir)
	if runErr != nil {
		return "", calls, fmt.Errorf("brief writer: %w", runErr)
	}
	var env struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil {
		return "", calls, errors.New("brief writer gave no result")
	}
	if env.IsError {
		return "", calls, errors.New("brief writer failed")
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	body := env.StructuredOutput
	if len(body) == 0 || string(body) == "null" {
		body = json.RawMessage(strings.TrimSpace(env.Result)) // the answer as text, when the schema tool was not used
	}
	if json.Unmarshal(body, &out) != nil || strings.TrimSpace(out.Markdown) == "" {
		return "", calls, errors.New("brief writer returned no document")
	}
	if calls < MinToolCalls {
		return "", calls, fmt.Errorf("brief writer ran %d checks, fewer than %d: not taken", calls, MinToolCalls)
	}
	return out.Markdown, calls, nil
}
