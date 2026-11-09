package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/example-user/commander/internal/claudeacct"
)

// ClaudeProjectsDir is where the default account's Claude keeps session
// transcripts. Its child drops an inherited CLAUDE_CONFIG_DIR (childEnv), so this
// is always ~/.claude/projects, never that value.
func ClaudeProjectsDir() string {
	return filepath.Join(claudeacct.DefaultDir(), "projects")
}

// FindTranscript returns the transcript path of sessionID under projectsDir, or
// "" when Claude has none (the session never received a message).
func FindTranscript(projectsDir, sessionID string) string {
	return claudeacct.FindTranscript(projectsDir, sessionID)
}

// TranscriptInfo is what hydration learns besides the events.
type TranscriptInfo struct {
	// TotalCostUSD is the last cost-state total, the cost baseline for a resumed
	// process (Claude's result total_cost_usd continues from it).
	TotalCostUSD float64
	CostKnown    bool
	Cwd          string
}

type transcriptEntry struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	UUID            string          `json:"uuid"`
	Timestamp       time.Time       `json:"timestamp"`
	IsSidechain     bool            `json:"isSidechain"`
	IsMeta          bool            `json:"isMeta"`
	IsCompact       bool            `json:"isCompactSummary"`
	SessionID       string          `json:"sessionId"`
	Cwd             string          `json:"cwd"`
	Message         json.RawMessage `json:"message"`
	Content         json.RawMessage `json:"content"`
	ToolUseResult   json.RawMessage `json:"toolUseResult"`
	PermissionDec   json.RawMessage `json:"permissionDecision"`
	APIBlockIndex   *int            `json:"apiBlockIndex"`
	TotalCostUSD    *float64        `json:"totalCostUSD"`
	CompactMetadata struct {
		Trigger    string `json:"trigger"`
		PreTokens  int    `json:"preTokens"`
		PostTokens int    `json:"postTokens"`
	} `json:"compactMetadata"`
}

var commandNameRe = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
var commandArgsRe = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
var localStdoutRe = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)

// ReadTranscript parses a Claude Code session transcript into Events (Source
// "transcript", Seq unset) in file order. It is best effort: unknown entry
// types (attachments, queue operations, api blobs) are skipped, sub-agent
// (sidechain) and meta entries too. Forked histories are not untangled: every
// branch's entries appear in file order.
func ReadTranscript(path, role string) ([]Event, TranscriptInfo, error) {
	var info TranscriptInfo
	f, err := os.Open(path)
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	var out []Event
	toolNames := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256<<10), 64<<20)
	for sc.Scan() {
		var t transcriptEntry
		if json.Unmarshal(sc.Bytes(), &t) != nil {
			continue
		}
		if t.Type == "cost-state" && t.TotalCostUSD != nil {
			info.TotalCostUSD, info.CostKnown = *t.TotalCostUSD, true
			continue
		}
		if t.IsSidechain || t.IsMeta {
			continue
		}
		if t.Cwd != "" {
			info.Cwd = t.Cwd
		}
		mk := func(k Kind) Event {
			return Event{Kind: k, Role: role, SessionID: t.SessionID, Time: t.Timestamp, Source: SourceTranscript, UUID: t.UUID}
		}
		switch t.Type {
		case "user":
			out = append(out, transcriptUser(t, mk, toolNames)...)
		case "assistant":
			var m message
			if json.Unmarshal(t.Message, &m) != nil {
				continue
			}
			var blocks []contentBlock
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			for i, b := range blocks {
				idx := i
				if t.APIBlockIndex != nil && len(blocks) == 1 {
					idx = *t.APIBlockIndex
				}
				for _, e := range transcriptBlock(mk, m.ID, idx, b) {
					if e.Kind == KindToolInput {
						toolNames[e.Tool.ID] = e.Tool.Name
					}
					out = append(out, e)
				}
			}
		case "system":
			switch t.Subtype {
			case "compact_boundary":
				e := mk(KindCompaction)
				e.Compaction = &Compaction{Trigger: t.CompactMetadata.Trigger, PreTokens: t.CompactMetadata.PreTokens, PostTokens: t.CompactMetadata.PostTokens}
				out = append(out, e)
			case "local_command":
				var s string
				_ = json.Unmarshal(t.Content, &s)
				e := mk(KindNotice)
				e.Subtype, e.Text = "local_command", localOutput(s)
				out = append(out, e)
			}
		}
	}
	return out, info, sc.Err()
}

func transcriptBlock(mk func(Kind) Event, msgID string, idx int, b contentBlock) []Event {
	at := func(k Kind) Event {
		e := mk(k)
		e.MessageID, e.Block = msgID, intp(idx)
		return e
	}
	switch b.Type {
	case "text":
		e := at(KindText)
		e.Text = b.Text
		return []Event{e}
	case "thinking", "redacted_thinking":
		e := at(KindThinking)
		e.Text = b.Thinking
		return []Event{e}
	case "tool_use", "server_tool_use":
		s := at(KindToolStart)
		s.Tool = &Tool{ID: b.ID, Name: b.Name}
		in := at(KindToolInput)
		in.Tool = &Tool{ID: b.ID, Name: b.Name, Input: b.Input}
		return []Event{s, in}
	}
	return nil
}

func transcriptUser(t transcriptEntry, mk func(Kind) Event, toolNames map[string]string) []Event {
	var m message
	if json.Unmarshal(t.Message, &m) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []Event{transcriptUserText(t, mk, text)}
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Event
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			e := mk(KindToolResult)
			e.Tool = toolResult(b, toolNames[b.ToolUseID], t.ToolUseResult, t.PermissionDec)
			out = append(out, e)
		case "text":
			out = append(out, transcriptUserText(t, mk, b.Text))
		}
	}
	return out
}

func transcriptUserText(t transcriptEntry, mk func(Kind) Event, text string) Event {
	switch {
	case t.IsCompact || strings.HasPrefix(text, "This session is being continued from a previous conversation"):
		e := mk(KindNotice)
		e.Subtype, e.Text = "compact_summary", text
		return e
	case strings.HasPrefix(text, "[Request interrupted by user"):
		e := mk(KindNotice)
		e.Subtype, e.Text = "interrupted", text
		return e
	case strings.Contains(text, "<local-command-stdout>"):
		e := mk(KindNotice)
		e.Subtype, e.Text = "local_command", localOutput(text)
		return e
	case strings.Contains(text, "<command-name>"):
		cmd := ""
		if m := commandNameRe.FindStringSubmatch(text); m != nil {
			cmd = strings.TrimSpace(m[1])
		}
		if m := commandArgsRe.FindStringSubmatch(text); m != nil && strings.TrimSpace(m[1]) != "" {
			cmd += " " + strings.TrimSpace(m[1])
		}
		e := mk(KindUser)
		e.Text, e.Origin, e.Subtype = cmd, "transcript", "slash_command"
		return e
	}
	e := mk(KindUser)
	e.Text, e.Origin = text, "transcript"
	return e
}

func localOutput(s string) string {
	if m := localStdoutRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(s)
}
