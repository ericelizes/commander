// Package chat is cmdrd's native chat engine: it owns one headless Claude Code
// process per session (`claude -p` in bidirectional stream-json mode),
// normalizes its output into a small stable Event model, persists an
// append-only event log per role for replay, and exposes the commands the app
// needs (send, interrupt, approve, answer, stop, handover to/from the tmux TUI).
//
// The Event model is the contract the Electron chat UI renders. It is
// documented in docs/plans/2026-10-20-native-chat-engine.md; change it only
// additively.
package chat

import (
	"encoding/json"
	"time"
)

// Kind is the type of an Event.
type Kind string

const (
	// KindSession: the process came up (first init of a process). Session holds
	// the model, cwd, CLI version and capabilities.
	KindSession Kind = "session"
	// KindUser: a user turn. Origin is "user" (sent through the engine), "mail"
	// (DeliverMail), "transcript" (hydrated history) or "subagent" (a Task
	// prompt, with ParentToolUseID).
	KindUser Kind = "user_message"
	// KindTextDelta / KindText: assistant text, streamed per content block
	// (MessageID + Block) and then final. The final replaces the deltas.
	KindTextDelta Kind = "text_delta"
	KindText      Kind = "text"
	// KindThinkingDelta / KindThinking: extended thinking for a block. Claude
	// often redacts the text; Tokens carries the estimated thinking tokens.
	KindThinkingDelta Kind = "thinking_delta"
	KindThinking      Kind = "thinking"
	// KindToolStart: a tool_use block opened (Tool.ID, Tool.Name).
	// KindToolInputDelta: partial JSON of its arguments (Tool.PartialJSON).
	// KindToolInput: the final arguments (Tool.Input).
	// KindToolResult: the result (Tool.Content, Tool.IsError, Tool.Structured).
	KindToolStart      Kind = "tool_start"
	KindToolInputDelta Kind = "tool_input_delta"
	KindToolInput      Kind = "tool_input"
	KindToolResult     Kind = "tool_result"
	// KindPermission: Claude asks to run a tool. Answer with Approve.
	// KindQuestion: AskUserQuestion. Answer with Answer.
	// KindPlan: ExitPlanMode (a proposed plan). Answer with Approve.
	// KindResolved: one of the three was answered or cancelled (Request.Decision).
	KindPermission Kind = "permission_request"
	KindQuestion   Kind = "question"
	KindPlan       Kind = "plan"
	KindResolved   Kind = "request_resolved"
	// KindStatus: transient CLI status (Status: "requesting", "thinking",
	// "interrupting", ...). Live only, not persisted.
	KindStatus Kind = "status"
	// KindState: the derived agent state changed (State.State is one of
	// working/idle/waiting/stuck, as in the agent_state table).
	KindState Kind = "agent_state"
	// KindTask: a background task / sub-agent lifecycle event (Task).
	KindTask Kind = "task"
	// KindCompaction: the conversation was compacted (Compaction).
	KindCompaction Kind = "compaction"
	// KindRateLimit: subscription rate-limit info (RateLimit, raw CLI object).
	KindRateLimit Kind = "rate_limit"
	// KindHook: a hook ran (Hook), e.g. the mail hook injecting unread mail.
	KindHook Kind = "hook"
	// KindNotice: anything else worth showing (Subtype + Text, Raw for unknown
	// CLI events).
	KindNotice Kind = "notice"
	// KindResult: a turn ended (Result: usage, cost delta, terminal reason).
	KindResult Kind = "turn_result"
	// KindError: an engine or CLI error (Error).
	KindError Kind = "error"
	// KindExit: the process exited (Exit).
	KindExit Kind = "process_exit"
	// KindReplayDone: sent once per subscription after the replayed history;
	// live events follow. Seq is the last replayed seq. Not persisted.
	KindReplayDone Kind = "replay_done"
)

// Sources of an Event.
const (
	SourceLive       = "live"       // from the running process
	SourceTranscript = "transcript" // hydrated from Claude's own session JSONL
	SourceEngine     = "engine"     // made by the engine (sends, state, exits)
)

// Event is one normalized chat event. Seq increases strictly per role across
// restarts; there are gaps where live-only kinds (deltas, status) were not
// persisted. Only the fields relevant to Kind are set.
type Event struct {
	Seq       uint64    `json:"seq"`
	Role      string    `json:"role"`
	SessionID string    `json:"session_id,omitempty"`
	Kind      Kind      `json:"kind"`
	Time      time.Time `json:"time"`
	Source    string    `json:"source"`

	// ParentToolUseID is set on events produced inside a sub-agent (Task tool);
	// it is the tool_use id of the Task call that owns them.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	MessageID       string `json:"message_id,omitempty"` // assistant message id (blocks)
	Block           *int   `json:"block,omitempty"`      // content block index within MessageID
	UUID            string `json:"uuid,omitempty"`       // CLI/transcript uuid when there is one

	Text    string `json:"text,omitempty"`
	Origin  string `json:"origin,omitempty"`
	Subtype string `json:"subtype,omitempty"`
	Status  string `json:"status,omitempty"`
	Tokens  int    `json:"tokens,omitempty"`
	Error   string `json:"error,omitempty"`

	Attachments []EventAttachment `json:"attachments,omitempty"` // files sent with a user message

	Tool       *Tool           `json:"tool,omitempty"`
	Request    *Request        `json:"request,omitempty"`
	State      *State          `json:"state,omitempty"`
	Task       *Task           `json:"task,omitempty"`
	Compaction *Compaction     `json:"compaction,omitempty"`
	RateLimit  json.RawMessage `json:"rate_limit,omitempty"`
	Result     *Result         `json:"result,omitempty"`
	Session    *SessionInfo    `json:"session,omitempty"`
	Hook       *Hook           `json:"hook,omitempty"`
	Exit       *Exit           `json:"exit,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// EventAttachment is a file sent with a user message (the bytes live at Path).
type EventAttachment struct {
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Path string `json:"path"`
}

// Tool describes a tool call or its result.
type Tool struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	PartialJSON string          `json:"partial_json,omitempty"`
	Content     string          `json:"content,omitempty"`
	IsError     bool            `json:"is_error,omitempty"`
	// Structured is Claude's tool_use_result (e.g. Bash {stdout, stderr,
	// interrupted}); Permission is its permission_decision for this call.
	Structured json.RawMessage `json:"structured,omitempty"`
	Permission json.RawMessage `json:"permission,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
}

// Request is a pending permission / question / plan, or its resolution.
type Request struct {
	ID          string          `json:"id"`
	ToolName    string          `json:"tool_name,omitempty"`
	ToolUseID   string          `json:"tool_use_id,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Suggestions json.RawMessage `json:"suggestions,omitempty"`
	Description string          `json:"description,omitempty"`
	BlockedPath string          `json:"blocked_path,omitempty"`
	// Decision is set on KindResolved: allow, allow_session, deny, answered,
	// or cancelled (the turn ended or the CLI withdrew the request).
	Decision string `json:"decision,omitempty"`
	Message  string `json:"message,omitempty"`
}

// State is the derived agent state, as written to the agent_state table.
type State struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Task is a background task / sub-agent event (system task_* in the stream).
type Task struct {
	TaskID      string          `json:"task_id,omitempty"`
	ToolUseID   string          `json:"tool_use_id,omitempty"`
	Description string          `json:"description,omitempty"`
	Status      string          `json:"status,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Compaction is a compact boundary.
type Compaction struct {
	Trigger    string `json:"trigger,omitempty"`
	PreTokens  int    `json:"pre_tokens,omitempty"`
	PostTokens int    `json:"post_tokens,omitempty"`
}

// Result is the end of a turn. CostDeltaUSD is this turn's cost; Claude's
// total_cost_usd is cumulative per session (and carried across --resume).
type Result struct {
	Subtype           string          `json:"subtype"`
	IsError           bool            `json:"is_error,omitempty"`
	TerminalReason    string          `json:"terminal_reason,omitempty"`
	StopReason        string          `json:"stop_reason,omitempty"`
	Text              string          `json:"text,omitempty"`
	NumTurns          int             `json:"num_turns,omitempty"`
	DurationMS        int64           `json:"duration_ms,omitempty"`
	DurationAPIMS     int64           `json:"duration_api_ms,omitempty"`
	CostDeltaUSD      float64         `json:"cost_delta_usd"`
	TotalCostUSD      float64         `json:"total_cost_usd"`
	Usage             json.RawMessage `json:"usage,omitempty"`
	ModelUsage        json.RawMessage `json:"model_usage,omitempty"`
	PermissionDenials json.RawMessage `json:"permission_denials,omitempty"`
}

// SessionInfo is what the CLI reported at init.
type SessionInfo struct {
	Model          string          `json:"model,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	CLIVersion     string          `json:"cli_version,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	Capabilities   []string        `json:"capabilities,omitempty"`
	Tools          int             `json:"tools,omitempty"`
	SlashCommands  int             `json:"slash_commands,omitempty"`
	MCPServers     json.RawMessage `json:"mcp_servers,omitempty"`
	PID            int             `json:"pid,omitempty"`
	Resumed        bool            `json:"resumed,omitempty"`
}

// Hook is a hook run reported by the CLI (--include-hook-events).
type Hook struct {
	Name     string `json:"name,omitempty"`
	Event    string `json:"event,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}

// Exit is a process exit.
type Exit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	// Expected is true when the engine stopped the process (Stop, PopOut,
	// shutdown) rather than it dying on its own.
	Expected bool `json:"expected,omitempty"`
}

// persisted reports whether k is written to the on-disk event log. Deltas and
// transient status are live-only: their final events supersede them.
func persisted(k Kind) bool {
	switch k {
	case KindTextDelta, KindThinkingDelta, KindToolInputDelta, KindStatus, KindReplayDone:
		return false
	}
	return true
}

func intp(i int) *int { return &i }
