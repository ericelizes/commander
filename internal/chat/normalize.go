package chat

import (
	"encoding/json"
	"strings"
	"time"
)

// maxContent caps tool result text and structured output kept in an event, so
// one huge Read result cannot bloat the log or a WebSocket frame.
const maxContent = 32 << 10

// ControlRequest is a request the CLI sent us on stdout (control_request). The
// engine must answer every one with a control_response.
type ControlRequest struct {
	RequestID string
	Subtype   string // can_use_tool, or something we do not support
	ToolName  string
	Input     json.RawMessage
	// Suggestions are the CLI's permission_suggestions (verbatim).
	Suggestions json.RawMessage
}

// Normalizer turns stream-json lines from one process into Events. It keeps the
// little state the protocol needs (open blocks, tool names, the cost baseline).
// Not safe for concurrent use.
type Normalizer struct {
	now func() time.Time

	initSeen  bool
	curMsg    map[string]string // parent_tool_use_id -> current api message id
	open      map[string]int    // message id -> open content block index
	nextBlock map[string]int    // message id -> next block index (no partial messages)
	blockKind map[string]string // message id/index -> block type
	toolNames map[string]string // tool_use id -> name
	started   map[string]bool   // tool_use id -> tool_start emitted

	// lastTotal is the previous cumulative total_cost_usd; hasBase reports
	// whether it is known (from a previous result, meta, or the transcript).
	lastTotal float64
	hasBase   bool
}

// NewNormalizer returns a Normalizer whose cost baseline is base (the session's
// cumulative cost so far) when known.
func NewNormalizer(base float64, known bool, now func() time.Time) *Normalizer {
	if now == nil {
		now = time.Now
	}
	return &Normalizer{
		now: now, curMsg: map[string]string{}, open: map[string]int{}, nextBlock: map[string]int{},
		blockKind: map[string]string{}, toolNames: map[string]string{}, started: map[string]bool{},
		lastTotal: base, hasBase: known,
	}
}

// LastTotal is the latest cumulative session cost seen.
func (n *Normalizer) LastTotal() float64 { return n.lastTotal }

type rawLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	UUID            string          `json:"uuid"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	APIMessageID    string          `json:"api_message_id"`
	Event           json.RawMessage `json:"event"`
	Message         json.RawMessage `json:"message"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	ToolResultMeta  []struct {
		ID                 string          `json:"id"`
		PermissionDecision json.RawMessage `json:"permission_decision"`
	} `json:"tool_result_meta"`
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	// Error is set on an assistant message the CLI made from an API error
	// (rate_limit, billing_error, authentication_failed, ...).
	Error string `json:"error"`
}

// Line normalizes one stdout line. It returns the events to emit and, when the
// CLI asked us something, the control request to track and answer.
func (n *Normalizer) Line(line []byte) ([]Event, *ControlRequest) {
	var r rawLine
	if err := json.Unmarshal(line, &r); err != nil {
		text := strings.TrimSpace(string(line))
		if text == "" {
			return nil, nil
		}
		return []Event{n.ev(KindNotice, func(e *Event) { e.Subtype = "stdout"; e.Text = text })}, nil
	}
	base := func(k Kind) Event {
		e := n.ev(k, nil)
		e.SessionID = r.SessionID
		e.UUID = r.UUID
		e.ParentToolUseID = r.ParentToolUseID
		return e
	}
	switch r.Type {
	case "commander_execution_start":
		var text string
		if json.Unmarshal(r.Message, &text) != nil {
			return nil, nil
		}
		event := base(KindUser)
		event.Source = SourceEngine
		event.Origin = "execution"
		event.Text = text
		return []Event{event}, nil
	case "system":
		return n.system(r, line, base), nil
	case "stream_event":
		return n.stream(r, base), nil
	case "assistant":
		return n.assistant(r, base), nil
	case "user":
		return n.user(r, base), nil
	case "result":
		return []Event{n.result(line, base)}, nil
	case "rate_limit_event":
		e := base(KindRateLimit)
		e.RateLimit = r.RateLimitInfo
		return []Event{e}, nil
	case "control_request":
		return n.controlRequest(r, base)
	case "control_response", "control_cancel_request", "keep_alive":
		return nil, nil // handled by the engine (responses) or noise
	}
	e := base(KindNotice)
	e.Subtype = "unknown:" + r.Type
	e.Raw = append(json.RawMessage(nil), line...)
	return []Event{e}, nil
}

func (n *Normalizer) ev(k Kind, fill func(*Event)) Event {
	e := Event{Kind: k, Time: n.now(), Source: SourceLive}
	if fill != nil {
		fill(&e)
	}
	return e
}

func (n *Normalizer) system(r rawLine, line []byte, base func(Kind) Event) []Event {
	switch r.Subtype {
	case "init":
		if n.initSeen {
			return nil // repeated before every turn
		}
		n.initSeen = true
		var in struct {
			Model          string          `json:"model"`
			Cwd            string          `json:"cwd"`
			Version        string          `json:"claude_code_version"`
			PermissionMode string          `json:"permissionMode"`
			Capabilities   []string        `json:"capabilities"`
			NTools         int             `json:"n_tools"`
			Tools          []string        `json:"tools"`
			NSlash         int             `json:"n_slash"`
			MCP            json.RawMessage `json:"mcp_servers"`
		}
		_ = json.Unmarshal(line, &in)
		tools := in.NTools
		if tools == 0 {
			tools = len(in.Tools)
		}
		e := base(KindSession)
		e.Session = &SessionInfo{Model: in.Model, Cwd: in.Cwd, CLIVersion: in.Version, PermissionMode: in.PermissionMode,
			Capabilities: in.Capabilities, Tools: tools, SlashCommands: in.NSlash, MCPServers: in.MCP}
		return []Event{e}
	case "status":
		var s struct {
			Status *string `json:"status"`
		}
		_ = json.Unmarshal(line, &s)
		e := base(KindStatus)
		e.Status = "idle"
		if s.Status != nil && *s.Status != "" {
			e.Status = *s.Status
		}
		return []Event{e}
	case "thinking_tokens":
		var s struct {
			Tokens int `json:"estimated_tokens"`
		}
		_ = json.Unmarshal(line, &s)
		e := base(KindStatus)
		e.Status, e.Tokens = "thinking", s.Tokens
		return []Event{e}
	case "compact_boundary":
		var c struct {
			Meta struct {
				Trigger    string `json:"trigger"`
				PreTokens  int    `json:"pre_tokens"`
				PostTokens int    `json:"post_tokens"`
			} `json:"compact_metadata"`
		}
		_ = json.Unmarshal(line, &c)
		e := base(KindCompaction)
		e.Compaction = &Compaction{Trigger: c.Meta.Trigger, PreTokens: c.Meta.PreTokens, PostTokens: c.Meta.PostTokens}
		return []Event{e}
	case "hook_started", "commands_changed":
		return nil
	case "hook_response":
		var h struct {
			Name     string `json:"hook_name"`
			Event    string `json:"hook_event"`
			Outcome  string `json:"outcome"`
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
		}
		_ = json.Unmarshal(line, &h)
		out, _ := truncate(h.Output, 4000)
		e := base(KindHook)
		e.Hook = &Hook{Name: h.Name, Event: h.Event, Outcome: h.Outcome, ExitCode: h.ExitCode, Output: out}
		return []Event{e}
	}
	if strings.HasPrefix(r.Subtype, "task_") {
		var t struct {
			TaskID      string `json:"task_id"`
			ToolUseID   string `json:"tool_use_id"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Summary     string `json:"summary"`
		}
		_ = json.Unmarshal(line, &t)
		e := base(KindTask)
		e.Subtype = r.Subtype
		e.Task = &Task{TaskID: t.TaskID, ToolUseID: t.ToolUseID, Description: t.Description, Status: t.Status,
			Summary: t.Summary, Raw: append(json.RawMessage(nil), line...)}
		return []Event{e}
	}
	var s struct {
		Text    string `json:"text"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(line, &s)
	e := base(KindNotice)
	e.Subtype = r.Subtype
	e.Text = firstNonEmpty(s.Text, s.Title, s.Content)
	if e.Text == "" {
		e.Raw = append(json.RawMessage(nil), line...)
	}
	return []Event{e}
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

func blockKey(msg string, idx int) string {
	b, _ := json.Marshal([]any{msg, idx})
	return string(b)
}

func (n *Normalizer) stream(r rawLine, base func(Kind) Event) []Event {
	var se streamEvent
	if json.Unmarshal(r.Event, &se) != nil {
		return nil
	}
	msg := r.APIMessageID
	if se.Type == "message_start" {
		if se.Message.ID != "" {
			msg = se.Message.ID
		}
		n.curMsg[r.ParentToolUseID] = msg
		return nil
	}
	if msg == "" {
		msg = n.curMsg[r.ParentToolUseID]
	}
	blk := func(k Kind) Event {
		e := base(k)
		e.MessageID, e.Block = msg, intp(se.Index)
		return e
	}
	switch se.Type {
	case "content_block_start":
		n.open[msg] = se.Index
		n.blockKind[blockKey(msg, se.Index)] = se.ContentBlock.Type
		if se.Index >= n.nextBlock[msg] {
			n.nextBlock[msg] = se.Index + 1
		}
		if se.ContentBlock.Type == "tool_use" || se.ContentBlock.Type == "server_tool_use" {
			n.toolNames[se.ContentBlock.ID] = se.ContentBlock.Name
			n.started[se.ContentBlock.ID] = true
			e := blk(KindToolStart)
			e.Tool = &Tool{ID: se.ContentBlock.ID, Name: se.ContentBlock.Name}
			return []Event{e}
		}
	case "content_block_delta":
		switch se.Delta.Type {
		case "text_delta":
			e := blk(KindTextDelta)
			e.Text = se.Delta.Text
			return []Event{e}
		case "thinking_delta":
			e := blk(KindThinkingDelta)
			e.Text = se.Delta.Thinking
			return []Event{e}
		case "input_json_delta":
			e := blk(KindToolInputDelta)
			e.Tool = &Tool{PartialJSON: se.Delta.PartialJSON}
			return []Event{e}
		}
	case "content_block_stop":
		delete(n.open, msg)
	}
	return nil
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// assistant handles a message snapshot: Claude emits one per content block,
// right before that block's content_block_stop, so the open block is its index.
func (n *Normalizer) assistant(r rawLine, base func(Kind) Event) []Event {
	var m message
	if json.Unmarshal(r.Message, &m) != nil {
		return nil
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Event
	for i, b := range blocks {
		idx, ok := n.open[m.ID]
		if !ok || len(blocks) > 1 {
			idx = n.nextBlock[m.ID]
			if len(blocks) > 1 {
				idx = i
			}
			n.nextBlock[m.ID] = idx + 1
		}
		out = append(out, n.blockEvents(base, m.ID, idx, b)...)
	}
	for i := range out {
		out[i].Error = r.Error
	}
	return out
}

func (n *Normalizer) blockEvents(base func(Kind) Event, msgID string, idx int, b contentBlock) []Event {
	mk := func(k Kind) Event {
		e := base(k)
		e.MessageID, e.Block = msgID, intp(idx)
		return e
	}
	switch b.Type {
	case "text":
		e := mk(KindText)
		e.Text = b.Text
		return []Event{e}
	case "thinking", "redacted_thinking":
		e := mk(KindThinking)
		e.Text = b.Thinking
		return []Event{e}
	case "tool_use", "server_tool_use":
		var out []Event
		n.toolNames[b.ID] = b.Name
		if !n.started[b.ID] {
			n.started[b.ID] = true
			s := mk(KindToolStart)
			s.Tool = &Tool{ID: b.ID, Name: b.Name}
			out = append(out, s)
		}
		e := mk(KindToolInput)
		e.Tool = &Tool{ID: b.ID, Name: b.Name, Input: b.Input}
		return append(out, e)
	}
	return nil
}

// user handles user-role lines: tool results, and (for sub-agents or replays)
// prompt text.
func (n *Normalizer) user(r rawLine, base func(Kind) Event) []Event {
	var m message
	if json.Unmarshal(r.Message, &m) != nil {
		return nil
	}
	perm := map[string]json.RawMessage{}
	for _, meta := range r.ToolResultMeta {
		perm[meta.ID] = meta.PermissionDecision
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		e := base(KindUser)
		e.Text, e.Origin = text, originFor(r.ParentToolUseID, "replay")
		return []Event{e}
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Event
	results := 0
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results++
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			var structured json.RawMessage
			if results == 1 {
				structured = r.ToolUseResult
			}
			e := base(KindToolResult)
			e.Tool = toolResult(b, n.toolNames[b.ToolUseID], structured, perm[b.ToolUseID])
			out = append(out, e)
		case "text":
			e := base(KindUser)
			e.Text, e.Origin = b.Text, originFor(r.ParentToolUseID, "replay")
			out = append(out, e)
		}
	}
	return out
}

func originFor(parent, def string) string {
	if parent != "" {
		return "subagent"
	}
	return def
}

func toolResult(b contentBlock, name string, structured, perm json.RawMessage) *Tool {
	content, cut := truncate(flattenContent(b.Content), maxContent)
	t := &Tool{ID: b.ToolUseID, Name: name, Content: content, IsError: b.IsError, Permission: nullToNil(perm), Truncated: cut}
	if s := nullToNil(structured); s != nil {
		if len(s) > maxContent {
			t.Truncated = true
		} else {
			t.Structured = s
		}
	}
	return t
}

// flattenContent turns a tool_result content (string or block list) into text.
func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n")
		}
		if p.Type == "text" {
			b.WriteString(p.Text)
		} else {
			b.WriteString("[" + p.Type + "]")
		}
	}
	return b.String()
}

func (n *Normalizer) result(line []byte, base func(Kind) Event) Event {
	var r struct {
		Subtype           string          `json:"subtype"`
		IsError           bool            `json:"is_error"`
		TerminalReason    string          `json:"terminal_reason"`
		StopReason        string          `json:"stop_reason"`
		Result            string          `json:"result"`
		NumTurns          int             `json:"num_turns"`
		DurationMS        int64           `json:"duration_ms"`
		DurationAPIMS     int64           `json:"duration_api_ms"`
		TotalCostUSD      float64         `json:"total_cost_usd"`
		Usage             json.RawMessage `json:"usage"`
		ModelUsage        json.RawMessage `json:"modelUsage"`
		PermissionDenials json.RawMessage `json:"permission_denials"`
	}
	_ = json.Unmarshal(line, &r)
	delta := r.TotalCostUSD - n.lastTotal
	if !n.hasBase {
		// Unknown baseline (a session we never saw a cost for): count it all once.
		delta = r.TotalCostUSD
	}
	if delta < 0 {
		delta = 0
	}
	if r.TotalCostUSD > n.lastTotal || !n.hasBase {
		n.lastTotal = r.TotalCostUSD
	}
	n.hasBase = true
	text, _ := truncate(r.Result, maxContent)
	e := base(KindResult)
	e.Result = &Result{Subtype: r.Subtype, IsError: r.IsError, TerminalReason: r.TerminalReason, StopReason: r.StopReason,
		Text: text, NumTurns: r.NumTurns, DurationMS: r.DurationMS, DurationAPIMS: r.DurationAPIMS,
		CostDeltaUSD: delta, TotalCostUSD: r.TotalCostUSD, Usage: nullToNil(r.Usage), ModelUsage: nullToNil(r.ModelUsage),
		PermissionDenials: nullToNil(r.PermissionDenials)}
	return e
}

func (n *Normalizer) controlRequest(r rawLine, base func(Kind) Event) ([]Event, *ControlRequest) {
	var req struct {
		Subtype     string          `json:"subtype"`
		ToolName    string          `json:"tool_name"`
		Input       json.RawMessage `json:"input"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
		Description string          `json:"description"`
		BlockedPath string          `json:"blocked_path"`
		ToolUseID   string          `json:"tool_use_id"`
	}
	_ = json.Unmarshal(r.Request, &req)
	cr := &ControlRequest{RequestID: r.RequestID, Subtype: req.Subtype, ToolName: req.ToolName, Input: req.Input, Suggestions: req.Suggestions}
	if req.Subtype != "can_use_tool" {
		return nil, cr
	}
	k := KindPermission
	switch req.ToolName {
	case "AskUserQuestion":
		k = KindQuestion
	case "ExitPlanMode":
		k = KindPlan
	}
	e := base(k)
	e.Request = &Request{ID: r.RequestID, ToolName: req.ToolName, ToolUseID: req.ToolUseID, Input: nullToNil(req.Input),
		Suggestions: nullToNil(req.Suggestions), Description: req.Description, BlockedPath: req.BlockedPath}
	return []Event{e}, cr
}

func nullToNil(r json.RawMessage) json.RawMessage {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return r
}

func truncate(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // do not split a UTF-8 rune
		cut--
	}
	return s[:cut] + "…", true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
