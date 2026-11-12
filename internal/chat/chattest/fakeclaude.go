// Package chattest has a fake `claude` that speaks the stream-json protocol,
// for tests of the native chat engine. A test package's TestMain calls
// MaybeRunFake first; tests then point the engine's Binary at os.Args[0] with
// CMDR_FAKE_CLAUDE=1 in the environment.
//
// The fake reacts to keywords in each user message:
//
//	tool         a Bash tool call behind a can_use_tool permission prompt
//	ask          an AskUserQuestion prompt (answers are echoed back)
//	plan         an ExitPlanMode prompt
//	long         a slow stream of text deltas (interrupt it)
//	deaf long    the same, but control interrupts are ignored (only SIGINT works)
//	task         a sub-agent: task_started, nested text, task_notification
//	compact      a compact_boundary
//	ratelimit    a rate_limit_event (allowed, five_hour 50%, seven_day 20%)
//	usagelimit   the CLI at a usage limit: a rejected rate_limit_event, the
//	             synthetic assistant error message, and an error result
//	unsupported  a control_request the engine must refuse
//	crash        exits with code 3 mid-turn
//	[commander interrupt] ...  (a message starting so) recorded verbatim as
//	             interrupt-note.json, then echoed: no other keyword applies
//	anything else is echoed back as text
//
// Every result carries a cumulative total_cost_usd (+0.01 per turn) that
// persists per session id in $CMDR_FAKE_DIR, like Claude's (across --resume).
package chattest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// EnvVar switches a test binary into the fake.
const EnvVar = "CMDR_FAKE_CLAUDE"

// DirVar is where the fake records argv, env and the answers it got.
const DirVar = "CMDR_FAKE_DIR"

// Version is what `--version` prints.
const Version = "2.1.305"

// MaybeRunFake runs the fake and exits when EnvVar is set; otherwise it returns.
func MaybeRunFake() {
	if os.Getenv(EnvVar) != "1" {
		return
	}
	os.Exit(run(os.Args[1:]))
}

type fake struct {
	out       *json.Encoder
	sessionID string
	dir       string
	cost      float64
	in        chan map[string]any
	sigint    chan os.Signal
	reqN      int
	msgN      int
	queued    []map[string]any
}

func run(args []string) int {
	for _, a := range args {
		if a == "--version" {
			fmt.Println(Version + " (Claude Code)")
			return 0
		}
	}
	f := &fake{out: json.NewEncoder(os.Stdout), dir: os.Getenv(DirVar), in: make(chan map[string]any, 64), sigint: make(chan os.Signal, 1)}
	resume := false
	for i, a := range args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(args) {
			f.sessionID, resume = args[i+1], a == "--resume"
		}
	}
	signal.Notify(f.sigint, syscall.SIGINT)
	f.record("argv-"+f.sessionID, map[string]any{"argv": args, "api_key_set": os.Getenv("ANTHROPIC_API_KEY") != "",
		"run_id": os.Getenv("COMMANDER_RUN_ID"), "run_session": os.Getenv("COMMANDER_RUN_SESSION"), "role": os.Getenv("COMMANDER_ROLE"), "manager_role": os.Getenv("COMMANDER_MANAGER_ROLE"),
		"lock_fd": fdOpen(3), "cwd": mustGetwd(), "config_dir": os.Getenv("CLAUDE_CONFIG_DIR")})
	// Like Claude: --resume needs the session's transcript under this config dir.
	if root := projectsRoot(); resume && root != "" {
		if m, _ := filepath.Glob(filepath.Join(root, "*", f.sessionID+".jsonl")); len(m) == 0 {
			fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", f.sessionID)
			return 1
		}
	}
	costFile := filepath.Join(f.dir, f.sessionID+".cost")
	if raw, err := os.ReadFile(costFile); err == nil {
		if !resume {
			fmt.Fprintf(os.Stderr, "Error: Session ID %s is already in use.\n", f.sessionID)
			return 1
		}
		fmt.Sscanf(string(raw), "%f", &f.cost)
	}
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				f.in <- m
			}
		}
		close(f.in)
	}()
	for {
		var m map[string]any
		if len(f.queued) > 0 {
			m, f.queued = f.queued[0], f.queued[1:]
		} else {
			var ok bool
			select {
			case m, ok = <-f.in:
				if !ok {
					return 0
				}
			case <-f.sigint:
				return 0
			}
		}
		switch m["type"] {
		case "user":
			msg, _ := m["message"].(map[string]any)
			text, _ := msg["content"].(string)
			if blocks, ok := msg["content"].([]any); ok { // text blocks, as sent with attachments
				for _, b := range blocks {
					if m, _ := b.(map[string]any); m["type"] == "text" {
						text += fmt.Sprint(m["text"])
					}
				}
			}
			if code := f.turn(text); code >= 0 {
				return code
			}
		case "control_request":
			f.ackControl(m) // interrupt while idle
		}
	}
}

// ProjectsVar is the transcript root the fake writes to, like ~/.claude/projects.
const ProjectsVar = "CMDR_FAKE_PROJECTS"

// appendTranscript writes the turn to <projects>/fake/<session>.jsonl, so the
// session "exists" for the engine's --resume decision, like Claude's own.
func (f *fake) appendTranscript(text string) {
	root := projectsRoot()
	if root == "" {
		return
	}
	dir := filepath.Join(root, "fake")
	_ = os.MkdirAll(dir, 0o755)
	file, err := os.OpenFile(filepath.Join(dir, f.sessionID+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	enc := json.NewEncoder(file)
	_ = enc.Encode(map[string]any{"type": "user", "sessionId": f.sessionID, "timestamp": now, "uuid": fmt.Sprintf("u%d", f.msgN),
		"message": map[string]any{"role": "user", "content": text}})
	_ = enc.Encode(map[string]any{"type": "assistant", "sessionId": f.sessionID, "timestamp": now, "uuid": fmt.Sprintf("a%d", f.msgN),
		"message": map[string]any{"id": fmt.Sprintf("msg_t_%d", f.msgN), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "reply to " + text}}}})
	_ = enc.Encode(map[string]any{"type": "cost-state", "sessionId": f.sessionID, "totalCostUSD": f.cost})
}

// persist saves the session's cost and transcript before the turn's result, as
// Claude does, so a client reacting to the result finds the session on disk.
func (f *fake) persist(text string) {
	_ = os.WriteFile(filepath.Join(f.dir, f.sessionID+".cost"), []byte(fmt.Sprintf("%.4f", f.cost)), 0o644)
	f.appendTranscript(text)
}

// projectsRoot is $CLAUDE_CONFIG_DIR/projects when the fake runs on a named
// account, else ProjectsVar.
func projectsRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	return os.Getenv(ProjectsVar)
}

func mustGetwd() string { wd, _ := os.Getwd(); return wd }

func fdOpen(fd uintptr) bool {
	var st syscall.Stat_t
	return syscall.Fstat(int(fd), &st) == nil
}

func (f *fake) record(name string, v any) {
	if f.dir == "" {
		return
	}
	raw, _ := json.Marshal(v)
	_ = os.WriteFile(filepath.Join(f.dir, name+".json"), raw, 0o644)
}

func (f *fake) emit(v map[string]any) {
	if _, ok := v["session_id"]; !ok {
		v["session_id"] = f.sessionID
	}
	_ = f.out.Encode(v)
}

func (f *fake) ackControl(m map[string]any) {
	id, _ := m["request_id"].(string)
	f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{"still_queued": []any{}}}})
}

// turn runs one turn; it returns an exit code to stop the process, or -1.
func (f *fake) turn(text string) int {
	f.emit(map[string]any{"type": "system", "subtype": "init", "model": "claude-fake-1", "cwd": mustGetwd(), "claude_code_version": Version,
		"permissionMode": "default", "capabilities": []string{"interrupt_receipt_v1"}, "n_tools": 3, "n_slash": 2})
	f.emit(map[string]any{"type": "system", "subtype": "status", "status": "requesting"})
	result := "success"
	reason := "completed"
	switch {
	case strings.Contains(text, "fresh provider conversation"):
		f.textBlock("", "continued prepared work after usage fallback", 0, false)
	case strings.Contains(text, "usagelimit") && f.cost == 0 && checkpointUsageFixture(f.dir):
		f.toolTurn("AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "Continue?", "header": "Continue", "multiSelect": false, "options": []any{map[string]any{"label": "yes", "description": "Continue"}, map[string]any{"label": "no", "description": "Wait"}}}}}, nil)
	case strings.Contains(text, "usagelimit"):
		f.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "rateLimitType": "five_hour",
			"resetsAt": 4102444800, "overageStatus": "rejected", "isUsingOverage": false,
			"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 1.0, "resetsAt": 4102444800}, "seven_day": map[string]any{"utilization": 0.4, "resetsAt": 4102444800}}}})
		msg := f.nextMsg()
		f.emit(map[string]any{"type": "assistant", "error": "rate_limit", "message": map[string]any{"id": msg, "role": "assistant", "model": "<synthetic>",
			"content": []any{map[string]any{"type": "text", "text": "You've hit your limit · resets 3pm"}}}})
		f.cost += 0.01
		f.persist(text)
		f.emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "terminal_reason": "completed",
			"result": "You've hit your limit · resets 3pm", "total_cost_usd": f.cost, "num_turns": 1, "duration_ms": 5})
		return -1
	case strings.Contains(text, "native-report"):
		f.cost += 0.01
		f.emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "Prepared changes; tests passed. Commander review is required.", "total_cost_usd": f.cost, "num_turns": 1, "duration_ms": 5})
		return -1
	case strings.HasPrefix(text, "[commander interrupt]"):
		f.record("interrupt-note", map[string]any{"text": text})
		f.textBlock("", "carrying on", 0, false)
	case strings.Contains(text, "crash"):
		f.textBlock("", "about to crash", 0, true)
		return 3
	case strings.Contains(text, "unsupported"):
		f.emit(map[string]any{"type": "control_request", "request_id": "hook-1", "request": map[string]any{"subtype": "hook_callback", "callback_id": "x"}})
		resp := f.waitResponse("hook-1")
		f.record("unsupported", resp)
		f.textBlock("", "unsupported handled", 0, false)
	case strings.Contains(text, "tool"):
		f.toolTurn("Bash", map[string]any{"command": "echo hi", "description": "Say hi"},
			[]any{map[string]any{"type": "addRules", "rules": []any{map[string]any{"toolName": "Bash", "ruleContent": "echo *"}}, "behavior": "allow", "destination": "localSettings"}})
	case strings.Contains(text, "ask") && !strings.Contains(text, "task"):
		f.toolTurn("AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "Which color?", "header": "Color",
			"multiSelect": false, "options": []any{map[string]any{"label": "red", "description": "warm"}, map[string]any{"label": "blue", "description": "cool"}}}}}, nil)
	case strings.Contains(text, "plan"):
		f.toolTurn("ExitPlanMode", map[string]any{"plan": "1. do the thing"}, nil)
	case strings.Contains(text, "long"):
		if !f.longText(strings.Contains(text, "deaf")) {
			result, reason = "error_during_execution", "aborted_streaming"
		}
	case strings.Contains(text, "task"):
		f.emit(map[string]any{"type": "system", "subtype": "task_started", "task_id": "t1", "tool_use_id": "toolu_task", "description": "explore"})
		f.textBlockParent("toolu_task", "sub-agent says hi")
		f.emit(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "t1", "tool_use_id": "toolu_task", "status": "completed", "summary": "done"})
		f.textBlock("", "task finished", 0, false)
	case strings.Contains(text, "compact"):
		f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]any{"trigger": "manual", "pre_tokens": 1000, "post_tokens": 100}})
		f.textBlock("", "compacted", 0, false)
	case strings.Contains(text, "ratelimit"):
		f.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed", "rateLimitType": "five_hour", "utilization": 0.5,
			"unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 0.5, "resetsAt": 4102444800}, "seven_day": map[string]any{"utilization": 0.2, "resetsAt": 4102444800}}}})
		f.textBlock("", "ok", 0, false)
	default:
		f.textBlock("", "echo: "+text, 0, false)
	}
	f.cost += 0.01
	f.persist(text)
	f.emit(map[string]any{"type": "result", "subtype": result, "is_error": result != "success", "terminal_reason": reason,
		"total_cost_usd": f.cost, "num_turns": 1, "duration_ms": 5, "usage": map[string]any{"input_tokens": 3, "output_tokens": 7}})
	return -1
}

func (f *fake) nextMsg() string { f.msgN++; return fmt.Sprintf("msg_fake_%d", f.msgN) }

func (f *fake) stream(parent, msg string, ev map[string]any) {
	var p any
	if parent != "" {
		p = parent
	}
	f.emit(map[string]any{"type": "stream_event", "event": ev, "parent_tool_use_id": p, "api_message_id": msg})
}

func (f *fake) textBlockParent(parent, text string) {
	msg := f.nextMsg()
	f.stream(parent, msg, map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
	f.stream(parent, msg, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	f.stream(parent, msg, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	f.emit(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"id": msg, "role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": text}}}})
	f.stream(parent, msg, map[string]any{"type": "content_block_stop", "index": 0})
}

// textBlock streams text as three deltas plus the final snapshot.
func (f *fake) textBlock(msg, text string, idx int, open bool) {
	if msg == "" {
		msg = f.nextMsg()
		f.stream("", msg, map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
	}
	f.stream("", msg, map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "text", "text": ""}})
	third := len(text) / 3
	for _, part := range []string{text[:third], text[third : 2*third], text[2*third:]} {
		f.stream("", msg, map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]any{"type": "text_delta", "text": part}})
	}
	if open {
		return
	}
	f.emit(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}})
	f.stream("", msg, map[string]any{"type": "content_block_stop", "index": idx})
}

func (f *fake) toolTurn(name string, input map[string]any, suggestions []any) {
	msg := f.nextMsg()
	toolID := "toolu_" + strings.ToLower(name)
	f.stream("", msg, map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
	f.stream("", msg, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": toolID, "name": name, "input": map[string]any{}}})
	raw, _ := json.Marshal(input)
	f.stream("", msg, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}})
	f.emit(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "role": "assistant",
		"content": []any{map[string]any{"type": "tool_use", "id": toolID, "name": name, "input": input}}}})
	f.stream("", msg, map[string]any{"type": "content_block_stop", "index": 0})
	f.reqN++
	reqID := fmt.Sprintf("perm-%d", f.reqN)
	f.emit(map[string]any{"type": "control_request", "request_id": reqID, "request": map[string]any{"subtype": "can_use_tool", "tool_name": name,
		"input": input, "permission_suggestions": suggestions, "tool_use_id": toolID, "description": "fake " + name}})
	resp := f.waitResponse(reqID)
	f.record("response-"+name, resp)
	inner, _ := resp["response"].(map[string]any)
	if inner["behavior"] != "allow" {
		msgText, _ := inner["message"].(string)
		f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result",
			"tool_use_id": toolID, "content": msgText, "is_error": true}}}, "tool_use_result": "Error: " + msgText,
			"tool_result_meta": []any{map[string]any{"id": toolID, "permission_decision": map[string]any{"decision": "reject", "source": "user_reject"}}}})
		f.textBlock("", name+" was denied", 0, false)
		return
	}
	out := "hi"
	if name == "AskUserQuestion" {
		upd, _ := inner["updatedInput"].(map[string]any)
		ans, _ := json.Marshal(upd["answers"])
		out = "answers: " + string(ans)
	}
	f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result",
		"tool_use_id": toolID, "content": out, "is_error": false}}}, "tool_use_result": map[string]any{"stdout": out, "stderr": "", "interrupted": false},
		"tool_result_meta": []any{map[string]any{"id": toolID, "permission_decision": map[string]any{"decision": "accept", "source": "user_temporary"}}}})
	f.textBlock("", "done: "+out, 0, false)
}

// waitResponse waits for the control_response to reqID, queueing user messages.
func (f *fake) waitResponse(reqID string) map[string]any {
	for m := range f.in {
		if m["type"] == "control_response" {
			resp, _ := m["response"].(map[string]any)
			if resp["request_id"] == reqID {
				return resp
			}
		}
		if m["type"] == "user" {
			f.queued = append(f.queued, m)
		}
	}
	return map[string]any{}
}

// longText streams deltas until done or interrupted; false means interrupted.
func (f *fake) longText(deaf bool) bool {
	msg := f.nextMsg()
	f.stream("", msg, map[string]any{"type": "message_start", "message": map[string]any{"id": msg}})
	f.stream("", msg, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < 1000; i++ {
		select {
		case m, ok := <-f.in:
			if !ok {
				return false
			}
			if m["type"] == "control_request" && !deaf {
				f.ackControl(m)
				return false
			}
			if m["type"] == "user" {
				f.queued = append(f.queued, m)
			}
		case <-f.sigint:
			f.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "terminal_reason": "aborted_streaming", "total_cost_usd": f.cost})
			os.Exit(0)
		case <-tick.C:
			f.stream("", msg, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("%d ", i)}})
		}
	}
	f.emit(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "long done"}}}})
	f.stream("", msg, map[string]any{"type": "content_block_stop", "index": 0})
	return true
}

// checkpointUsageFixture opts the private multi-run fixture into a question before exhaustion.
func checkpointUsageFixture(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "checkpoint-usage"))
	return err == nil
}
