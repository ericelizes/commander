package chat

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/agentrt"
)

// The Codex runtime: a role set to codex runs `cmdrd __codex-bridge` instead of
// claude. The bridge speaks Claude's stream-json on stdin/stdout, which is all
// the engine and the chat host know, and drives `codex app-server` (JSON-RPC
// over stdio, the protocol Codex's own IDE clients use) underneath. Nothing
// above the host changes: chat, mail, hold, handover and approvals all work.

// CodexBridgeArg is argv[1] of a bridge process.
const CodexBridgeArg = "__codex-bridge"

// CodexBinary is the Codex CLI looked up on PATH ($COMMANDER_CODEX_BIN overrides).
const CodexBinary = "codex"

func codexBinary() string {
	if b := os.Getenv("COMMANDER_CODEX_BIN"); b != "" {
		return b
	}
	return CodexBinary
}

// CodexBridgeConfig is what one bridge needs; it arrives as flags.
type CodexBridgeConfig struct {
	Cwd             string // "" = the bridge's own cwd
	Model           string // "" = Codex's default
	PromptPath      string // the role prompt, sent as developer instructions
	ThreadFile      string // holds the Codex thread id between runs
	Handoff         string // a predecessor transcript the first turn should read ("" = none)
	SkipPermissions bool
	RequireResume   bool   // a checkpoint may never silently start a new thread
	SessionID       string // the role's session id, reported in init
	// Prompt makes a one-shot run (an Operator's `claude -p`): the bridge sends
	// it as the first message, ignores stdin and exits when that turn ends.
	Prompt string
	// HookRole gives the session the hooks Claude runs for that agent
	// (agentrt.CodexHookArgs); "" = none. GuardOnly drops the mail hooks, as
	// a one-shot host Operator on Claude has only the build-slot guard. Such a
	// run cannot read mail: `internal-mail --interrupt` ends it (spawn.StopHostRun).
	HookRole  string
	GuardOnly bool
	// BrowserServer explicitly selects one configured chrome-devtools MCP. Empty disables all.
	BrowserServer string
}

// CodexBridgeArgs is the bridge argv (without the binary) for spec.
func CodexBridgeArgs(spec Spec, home string) []string {
	hookRole := spec.Agent // the hook role of the Claude chat (Resolver sets Settings from it)
	if hookRole == "" {
		hookRole = spec.Role
	}
	return CodexBridgeConfig{Cwd: spec.Cwd, Model: CodexModel(spec.Model), PromptPath: spec.PromptPath, ThreadFile: spec.codexThreadFile(home),
		Handoff: spec.Handoff, RequireResume: spec.ExecutionResume, SkipPermissions: spec.SkipPermissions, SessionID: spec.SessionID, HookRole: hookRole}.Args()
}

// Args is the bridge argv (without the binary) that runs cfg.
func (cfg CodexBridgeConfig) Args() []string {
	args := []string{CodexBridgeArg, "--cwd", cfg.Cwd, "--thread-file", cfg.ThreadFile, "--session-id", cfg.SessionID}
	for _, f := range []struct{ flag, val string }{{"--model", cfg.Model}, {"--prompt-file", cfg.PromptPath}, {"--handoff", cfg.Handoff}, {"--prompt", cfg.Prompt}, {"--hooks-role", cfg.HookRole}, {"--browser-server", cfg.BrowserServer}} {
		if f.val != "" {
			args = append(args, f.flag, f.val)
		}
	}
	if cfg.SkipPermissions {
		args = append(args, "--skip-permissions")
	}
	if cfg.RequireResume {
		args = append(args, "--require-resume")
	}
	if cfg.GuardOnly {
		args = append(args, "--guard-only")
	}
	return args
}

// DefaultCodexModel is the model Commander runs its Codex agents on when none
// is named: Commander's choice, not the Codex app's own default (which was its
// most expensive model). COMMANDER_CODEX_MODEL overrides it.
const DefaultCodexModel = "gpt-6.1-sol"

// CodexModel is model as a Codex model id, "openai/" stripped. A Claude model
// name, another provider, or nothing gives the default Codex model.
func CodexModel(model string) string {
	def := DefaultCodexModel
	if v := strings.TrimSpace(os.Getenv("COMMANDER_CODEX_MODEL")); v != "" {
		def = v
	}
	m := strings.TrimSpace(model)
	if provider, rest, ok := strings.Cut(m, "/"); ok {
		if provider != "openai" {
			return def
		}
		m = rest
	}
	if m == "" {
		return def
	}
	for _, claude := range []string{"claude", "opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, claude) {
			return def
		}
	}
	return m
}

// codexAgentMailServers are the agent-mail MCP server names Commander agents
// must not use (they mail with `cmdr internal-mail`), as Claude's
// --disallowedTools has them.
var codexAgentMailServers = []string{"mcp_agent_mail", "mcp-agent-mail"}

// codexAppServerArgv adds launch-only MCP policy and role hooks. No user or
// managed configuration is rewritten.
func codexAppServerArgv(cfg CodexBridgeConfig, codexHome string) ([]string, error) {
	paths, err := codexConfigPaths(codexHome, cfg.Cwd)
	if err != nil {
		return nil, err
	}
	args, err := CodexMCPLaunchArgs(paths, cfg.BrowserServer)
	if err != nil {
		return nil, err
	}
	if cfg.HookRole != "" {
		args = append(args, agentrt.CodexHookArgs(cfg.HookRole, !cfg.GuardOnly)...)
	}
	return args, nil
}

// CodexHooksReady reports whether the Codex CLI runs hooks (`codex features
// list` shows hooks enabled): without them a Codex Operator has no build-slot
// or root-search guard, so Operators are only sent to Codex when this holds.
func CodexHooksReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, codexBinary(), "features", "list").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "hooks" {
			return f[len(f)-1] == "true"
		}
	}
	return false
}

// CodexHome is $CODEX_HOME, else ~/.codex.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".codex")
}

// CodexThreadFile holds a role's Codex thread id.
func CodexThreadFile(home, role string) string {
	return filepath.Join(home, "chat", role+".codex-thread")
}

// MaybeRunCodexBridge runs the bridge and exits when argv[1] is CodexBridgeArg.
func MaybeRunCodexBridge() {
	if len(os.Args) < 2 || os.Args[1] != CodexBridgeArg {
		return
	}
	var cfg CodexBridgeConfig
	fs := flag.NewFlagSet(CodexBridgeArg, flag.ExitOnError)
	fs.StringVar(&cfg.Cwd, "cwd", "", "")
	fs.StringVar(&cfg.Model, "model", "", "")
	fs.StringVar(&cfg.PromptPath, "prompt-file", "", "")
	fs.StringVar(&cfg.ThreadFile, "thread-file", "", "")
	fs.StringVar(&cfg.Handoff, "handoff", "", "")
	fs.StringVar(&cfg.SessionID, "session-id", "", "")
	fs.BoolVar(&cfg.SkipPermissions, "skip-permissions", false, "")
	fs.BoolVar(&cfg.RequireResume, "require-resume", false, "")
	fs.StringVar(&cfg.Prompt, "prompt", "", "")
	fs.StringVar(&cfg.HookRole, "hooks-role", "", "")
	fs.BoolVar(&cfg.GuardOnly, "guard-only", false, "")
	fs.StringVar(&cfg.BrowserServer, "browser-server", os.Getenv("COMMANDER_CODEX_BROWSER_SERVER"), "one configured browser MCP for an explicitly browser-required task")
	_ = fs.Parse(os.Args[2:])
	if cfg.Cwd == "" {
		cfg.Cwd, _ = os.Getwd()
	}
	args, err := codexAppServerArgv(cfg, CodexHome())
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex bridge: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command(codexBinary(), args...)
	cmd.Dir = cfg.Cwd
	cmd.Stderr = os.Stderr
	// Its own group: the tools Codex runs die with it (a killed Operator's pane
	// hangs up only the bridge), and a closed stdin alone does not end them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "codex bridge: start codex app-server: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	err = RunCodexBridge(ctx, cfg, os.Stdin, os.Stdout, out, in)
	cancel()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex bridge: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// codexBridge is one bridge session: Claude stream-json on one side, Codex
// app-server JSON-RPC on the other.
type codexBridge struct {
	cfg CodexBridgeConfig
	out *json.Encoder // to the engine (Claude stream-json lines)
	rpc io.Writer     // to codex app-server
	omu sync.Mutex
	wmu sync.Mutex

	mu       sync.Mutex
	nextID   int
	pending  map[int]chan rpcReply
	thread   string
	turn     string // the turn in flight ("" = idle)
	turnAt   time.Time
	lastText string
	approval map[string]json.RawMessage // control request id -> codex request id
	firstMsg bool                       // the next user message is the session's first
	usage    codexTokens                // the turn's tokens so far (thread/tokenUsage/updated)
	done     chan error                 // RunCodexBridge returns the first value
}

type rpcReply struct {
	Result json.RawMessage
	Err    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

// RunCodexBridge runs until the engine closes stdin or Codex exits.
func RunCodexBridge(ctx context.Context, cfg CodexBridgeConfig, stdin io.Reader, stdout io.Writer, codexOut io.Reader, codexIn io.Writer) error {
	b := &codexBridge{cfg: cfg, out: json.NewEncoder(stdout), rpc: codexIn, pending: map[int]chan rpcReply{}, approval: map[string]json.RawMessage{},
		done: make(chan error, 3)}
	go func() { b.done <- b.readCodex(codexOut) }()
	if err := b.start(ctx); err != nil {
		return err
	}
	if cfg.Prompt != "" {
		msg, _ := json.Marshal(map[string]any{"role": "user", "content": cfg.Prompt})
		go b.userMessage(msg)
	} else {
		go func() { b.done <- b.readEngine(stdin) }()
	}
	select {
	case err := <-b.done:
		return err
	case <-ctx.Done():
		b.endTurn()
		return fmt.Errorf("stopped: %w", ctx.Err())
	}
}

// codexStopGrace is how long a stopped bridge waits for Codex to end its turn
// (which also ends the tool it is running) before the group is killed.
const codexStopGrace = 3 * time.Second

// endTurn asks Codex to end the turn in flight and waits for it, within
// codexStopGrace.
func (b *codexBridge) endTurn() {
	b.mu.Lock()
	turn := b.turn
	b.mu.Unlock()
	if turn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexStopGrace)
	defer cancel()
	if _, err := b.call(ctx, "turn/interrupt", map[string]any{"threadId": b.thread, "turnId": turn}); err != nil {
		return
	}
	for ctx.Err() == nil {
		b.mu.Lock()
		turn = b.turn
		b.mu.Unlock()
		if turn == "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (b *codexBridge) emit(v any) {
	// Claude puts session_id on every line; the ceilings watchdog and
	// `cmdr runtime report` skip lines without one.
	if m, ok := v.(map[string]any); ok && m["session_id"] == nil && b.cfg.SessionID != "" {
		m["session_id"] = b.cfg.SessionID
	}
	b.omu.Lock()
	defer b.omu.Unlock()
	_ = b.out.Encode(v)
}

func (b *codexBridge) send(msg map[string]any) error {
	msg["jsonrpc"] = "2.0"
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	_, err = b.rpc.Write(append(raw, '\n'))
	return err
}

func (b *codexBridge) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	ch := make(chan rpcReply, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	if err := b.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, fmt.Errorf("%s: %s", method, r.Err.Message)
		}
		return r.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(2 * time.Minute):
		return nil, fmt.Errorf("%s: no reply", method)
	}
}

// start initializes Codex and starts or resumes the role's thread.
func (b *codexBridge) start(ctx context.Context) error {
	if _, err := b.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "commander", "title": "Commander", "version": "1"}}); err != nil {
		return err
	}
	_ = b.send(map[string]any{"method": "initialized"})
	params := map[string]any{"cwd": b.cfg.Cwd}
	if b.cfg.Model != "" {
		params["model"] = b.cfg.Model
	}
	if b.cfg.PromptPath != "" {
		if raw, err := os.ReadFile(b.cfg.PromptPath); err == nil {
			params["developerInstructions"] = string(raw)
		}
	}
	if b.cfg.HookRole != "" {
		// Hooks from -c are "session flags", which Codex skips until someone
		// trusts their hash in /hooks; Commander wrote them itself.
		params["config"] = map[string]any{"bypass_hook_trust": true}
	}
	if b.cfg.SkipPermissions {
		params["approvalPolicy"], params["sandbox"] = "never", "danger-full-access"
	} else {
		params["approvalPolicy"], params["sandbox"] = "on-request", "workspace-write"
	}
	prev := ""
	if raw, err := os.ReadFile(b.cfg.ThreadFile); err == nil {
		prev = strings.TrimSpace(string(raw))
	}
	var res json.RawMessage
	var err error
	if b.cfg.RequireResume && prev == "" {
		return errors.New("checkpoint thread is unavailable; refusing fresh Codex context")
	}
	if prev != "" {
		p := map[string]any{"threadId": prev}
		for k, v := range params {
			p[k] = v
		}
		res, err = b.call(ctx, "thread/resume", p)
		if err != nil && b.cfg.RequireResume {
			return fmt.Errorf("resume checkpoint thread: %w", err)
		}
	}
	if prev == "" || err != nil {
		res, err = b.call(ctx, "thread/start", params)
		b.firstMsg = true
	}
	if err != nil {
		return err
	}
	var r struct {
		Model  string `json:"model"`
		Thread struct {
			ID         string `json:"id"`
			CLIVersion string `json:"cliVersion"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(res, &r)
	if b.cfg.RequireResume && r.Thread.ID != prev {
		return errors.New("Codex resumed a different checkpoint thread")
	}
	b.mu.Lock()
	b.thread = r.Thread.ID
	b.mu.Unlock()
	go b.readRateLimits()
	if b.cfg.ThreadFile != "" && b.thread != "" {
		_ = os.MkdirAll(filepath.Dir(b.cfg.ThreadFile), 0o700)
		_ = os.WriteFile(b.cfg.ThreadFile, []byte(b.thread+"\n"), 0o600)
	}
	mode := "default"
	if b.cfg.SkipPermissions {
		mode = "bypassPermissions"
	}
	b.emit(map[string]any{"type": "system", "subtype": "init", "session_id": b.cfg.SessionID, "model": "codex:" + r.Model,
		"cwd": b.cfg.Cwd, "claude_code_version": "codex-" + r.Thread.CLIVersion, "permissionMode": mode,
		"capabilities": RequiredCapabilities, "tools": []string{}})
	return nil
}

// readEngine turns the engine's stream-json input into Codex calls.
func (b *codexBridge) readEngine(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var line struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Message   json.RawMessage `json:"message"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Behavior string `json:"behavior"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		switch line.Type {
		case "user":
			go b.userMessage(line.Message)
		case "control_request":
			if line.Request.Subtype == "interrupt" {
				go b.interrupt(line.RequestID)
			} else {
				b.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": line.RequestID, "error": "unsupported by the codex runtime: " + line.Request.Subtype}})
			}
		case "control_response":
			b.answerApproval(line.Response.RequestID, line.Response.Response.Behavior)
		}
	}
	return sc.Err()
}

// codexInput converts a Claude user message (text or content blocks) to Codex input.
func codexInput(msg json.RawMessage) []map[string]any {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []map[string]any{{"type": "text", "text": text}}
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var in []map[string]any
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			in = append(in, map[string]any{"type": "text", "text": bl.Text})
		case "image":
			// Codex takes images by path: write the attachment out once.
			raw, err := base64.StdEncoding.DecodeString(bl.Source.Data)
			if err != nil {
				continue
			}
			ext := ".png"
			if strings.Contains(bl.Source.MediaType, "jpeg") {
				ext = ".jpg"
			}
			f, err := os.CreateTemp("", "cmdr-codex-*"+ext)
			if err != nil {
				continue
			}
			_, _ = f.Write(raw)
			_ = f.Close()
			in = append(in, map[string]any{"type": "localImage", "path": f.Name()})
		}
	}
	return in
}

func (b *codexBridge) userMessage(msg json.RawMessage) {
	in := codexInput(msg)
	if len(in) == 0 {
		return
	}
	b.mu.Lock()
	turn, first := b.turn, b.firstMsg
	b.firstMsg = false
	b.mu.Unlock()
	if first && b.cfg.Handoff != "" {
		in = append([]map[string]any{{"type": "text", "text": HandoffNote(b.cfg.Handoff)}}, in...)
	}
	ctx := context.Background()
	if turn != "" {
		// A message during a turn joins it, as with Claude.
		if _, err := b.call(ctx, "turn/steer", map[string]any{"threadId": b.thread, "expectedTurnId": turn, "input": in}); err == nil {
			return
		}
	}
	if _, err := b.call(ctx, "turn/start", map[string]any{"threadId": b.thread, "input": in}); err != nil {
		b.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "codex: " + err.Error()})
		if b.cfg.Prompt != "" {
			b.done <- err
		}
	}
}

func (b *codexBridge) interrupt(reqID string) {
	b.mu.Lock()
	turn := b.turn
	b.mu.Unlock()
	if turn != "" {
		_, _ = b.call(context.Background(), "turn/interrupt", map[string]any{"threadId": b.thread, "turnId": turn})
	}
	b.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": reqID}})
}

// readCodex turns Codex notifications, replies and requests into stream-json.
func (b *codexBridge) readCodex(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "" && len(m.ID) > 0: // a reply to one of our calls
			var id int
			if json.Unmarshal(m.ID, &id) == nil {
				b.mu.Lock()
				ch := b.pending[id]
				delete(b.pending, id)
				b.mu.Unlock()
				if ch != nil {
					ch <- rpcReply{Result: m.Result, Err: m.Error}
				}
			}
		case m.Method != "" && len(m.ID) > 0: // Codex asks us something
			b.serverRequest(m.ID, m.Method, m.Params)
		default:
			b.notification(m.Method, m.Params)
		}
	}
	return fmt.Errorf("codex app-server exited")
}

type codexItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Summary          []string        `json:"summary"`
	Content          json.RawMessage `json:"content"`
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Changes          json.RawMessage `json:"changes"`
	Status           string          `json:"status"`
	Tool             string          `json:"tool"`
	Server           string          `json:"server"`
	Arguments        json.RawMessage `json:"arguments"`
	Query            string          `json:"query"`
	Error            json.RawMessage `json:"error"`
}

func (b *codexBridge) notification(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			DurationMS int64  `json:"durationMs"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item       codexItem `json:"item"`
		ItemID     string    `json:"itemId"`
		Delta      string    `json:"delta"`
		TokenUsage struct {
			Last codexTokens `json:"last"`
		} `json:"tokenUsage"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(params, &p)
	// Native child agents share the app-server notification stream. Their
	// completion must not finish the parent Operator's one-shot bridge.
	b.mu.Lock()
	thread, turn := b.thread, b.turn
	b.mu.Unlock()
	if p.ThreadID != "" && p.ThreadID != thread {
		return
	}
	// Turn-scoped child events can lack threadId, or carry the subscribing
	// parent's threadId. An explicit turn identity must also match. Keep
	// legacy unscoped item notifications, but never accept an unidentified
	// completion or let a second turn/started replace the active turn.
	if p.TurnID != "" && p.TurnID != turn {
		return
	}
	if method == "turn/started" && (p.Turn.ID == "" || (turn != "" && p.Turn.ID != turn)) {
		return
	}
	switch method {
	case "turn/started":
		b.mu.Lock()
		b.turn, b.turnAt, b.lastText, b.usage = p.Turn.ID, time.Now(), "", codexTokens{}
		b.mu.Unlock()
	case "thread/tokenUsage/updated":
		// One update per model call; "last" is that call, "total" the thread's
		// (which a resumed thread carries over), so the turn sums "last".
		b.mu.Lock()
		b.usage = b.usage.add(p.TokenUsage.Last)
		b.mu.Unlock()
	case "item/started":
		b.itemStarted(p.Item)
	case "item/agentMessage/delta":
		b.emit(streamLine(p.ItemID, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": p.Delta}}))
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		b.emit(streamLine(p.ItemID, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": p.Delta}}))
	case "item/completed":
		b.itemCompleted(p.Item)
	case "turn/completed":
		b.mu.Lock()
		if b.turn == "" || p.Turn.ID != b.turn {
			b.mu.Unlock()
			return
		}
		text, at, usage := b.lastText, b.turnAt, b.usage
		b.turn, b.usage = "", codexTokens{}
		b.mu.Unlock()
		go b.readRateLimits()
		res := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "num_turns": 1,
			"duration_ms": time.Since(at).Milliseconds(), "total_cost_usd": 0, "usage": usage.claudeUsage()}
		switch p.Turn.Status {
		case "interrupted":
			res["subtype"], res["terminal_reason"] = "error_during_execution", "aborted_streaming"
		case "failed":
			res["subtype"], res["is_error"] = "error_during_execution", true
			if p.Turn.Error != nil {
				res["result"] = p.Turn.Error.Message
			}
		}
		b.emit(res)
		if b.cfg.Prompt != "" {
			var err error
			if res["is_error"] == true {
				err = fmt.Errorf("turn %s", p.Turn.Status)
			}
			b.done <- err
		}
	case "account/rateLimits/updated":
		if info := codexRateLimitInfo(params); info != nil {
			b.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": info})
		}
	case "error":
		if p.Message != "" {
			b.emit(map[string]any{"type": "system", "subtype": "codex_error", "message": p.Message})
		}
	}
}

// codexTokens is Codex's TokenUsageBreakdown.
type codexTokens struct {
	Input     int64 `json:"inputTokens"`
	Cached    int64 `json:"cachedInputTokens"`
	Output    int64 `json:"outputTokens"`
	Reasoning int64 `json:"reasoningOutputTokens"`
}

func (t codexTokens) add(o codexTokens) codexTokens {
	return codexTokens{t.Input + o.Input, t.Cached + o.Cached, t.Output + o.Output, t.Reasoning + o.Reasoning}
}

// claudeUsage is t as a Claude result's usage: Codex counts cached tokens
// inside inputTokens, Claude's input_tokens excludes cache reads.
func (t codexTokens) claudeUsage() map[string]any {
	return map[string]any{"input_tokens": max(t.Input-t.Cached, 0), "cache_read_input_tokens": t.Cached, "output_tokens": t.Output}
}

func streamLine(msgID string, event map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "uuid": msgID, "api_message_id": msgID, "event": event}
}

func assistantLine(msgID string, blocks ...map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"id": msgID, "role": "assistant", "content": blocks}}
}

// toolUse names a Codex item the way Claude names the same kind of tool, so the
// chat draws it with the right card.
func toolUse(it codexItem) (name string, input map[string]any) {
	switch it.Type {
	case "commandExecution":
		return "Bash", map[string]any{"command": it.Command, "cwd": it.Cwd}
	case "fileChange":
		return "Edit", map[string]any{"changes": it.Changes}
	case "mcpToolCall":
		return "mcp__" + it.Server + "__" + it.Tool, map[string]any{"arguments": it.Arguments}
	case "dynamicToolCall":
		return it.Tool, map[string]any{"arguments": it.Arguments}
	case "webSearch":
		return "WebSearch", map[string]any{"query": it.Query}
	}
	return "", nil
}

func (b *codexBridge) itemStarted(it codexItem) {
	switch it.Type {
	case "agentMessage":
		b.emit(streamLine(it.ID, map[string]any{"type": "message_start", "message": map[string]any{"id": it.ID}}))
		b.emit(streamLine(it.ID, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text"}}))
	case "reasoning":
		b.emit(streamLine(it.ID, map[string]any{"type": "message_start", "message": map[string]any{"id": it.ID}}))
		b.emit(streamLine(it.ID, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking"}}))
	default:
		if name, input := toolUse(it); name != "" {
			b.emit(assistantLine(it.ID, map[string]any{"type": "tool_use", "id": it.ID, "name": name, "input": input}))
		}
	}
}

func (b *codexBridge) itemCompleted(it codexItem) {
	switch it.Type {
	case "agentMessage":
		b.mu.Lock()
		b.lastText = it.Text
		b.mu.Unlock()
		b.emit(assistantLine(it.ID, map[string]any{"type": "text", "text": it.Text}))
		b.emit(streamLine(it.ID, map[string]any{"type": "content_block_stop", "index": 0}))
	case "reasoning":
		b.emit(assistantLine(it.ID, map[string]any{"type": "thinking", "thinking": strings.Join(it.Summary, "\n")}))
		b.emit(streamLine(it.ID, map[string]any{"type": "content_block_stop", "index": 0}))
	default:
		name, _ := toolUse(it)
		if name == "" {
			return
		}
		out, isErr := it.AggregatedOutput, false
		switch it.Type {
		case "commandExecution":
			isErr = it.ExitCode != nil && *it.ExitCode != 0
		case "fileChange":
			out, isErr = "file change "+it.Status, it.Status == "failed" || it.Status == "declined"
		default:
			if len(it.Error) > 0 && string(it.Error) != "null" {
				out, isErr = string(it.Error), true
			} else if out == "" {
				out = it.Status
			}
		}
		b.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": it.ID, "content": out, "is_error": isErr}}}})
	}
}

// serverRequest answers Codex's requests: approvals go to the user as Claude
// permission requests; anything else is declined so a turn never hangs.
func (b *codexBridge) serverRequest(id json.RawMessage, method string, params json.RawMessage) {
	var p struct {
		ItemID  string `json:"itemId"`
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(params, &p)
	var tool string
	var input map[string]any
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		tool, input = "Bash", map[string]any{"command": p.Command, "cwd": p.Cwd, "reason": p.Reason}
	case "item/fileChange/requestApproval", "applyPatchApproval":
		tool, input = "Edit", map[string]any{"item": p.ItemID, "reason": p.Reason}
	default:
		_ = b.send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "not supported by commander: " + method}})
		return
	}
	reqID := "codex-" + strings.Trim(string(id), `"`)
	b.mu.Lock()
	b.approval[reqID] = id
	b.mu.Unlock()
	b.emit(map[string]any{"type": "control_request", "request_id": reqID, "request": map[string]any{
		"subtype": "can_use_tool", "tool_name": tool, "input": input, "tool_use_id": p.ItemID}})
}

func (b *codexBridge) answerApproval(reqID, behavior string) {
	b.mu.Lock()
	id, ok := b.approval[reqID]
	delete(b.approval, reqID)
	b.mu.Unlock()
	if !ok {
		return
	}
	decision := "decline"
	if behavior == "allow" {
		decision = "accept"
	}
	_ = b.send(map[string]any{"id": id, "result": map[string]any{"decision": decision}})
}

// readRateLimits asks Codex for the account's plan usage and forwards it as a
// rate_limit_event. Codex sends account/rateLimits/updated only on a change it
// chooses to announce, which in practice is rarely, so without asking the
// account showed no usage at all. Runs off the reader goroutine (it waits on a
// reply the reader delivers).
func (b *codexBridge) readRateLimits() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := b.call(ctx, "account/rateLimits/read", map[string]any{})
	if err != nil {
		return
	}
	if info := codexRateLimitInfo(res); info != nil {
		b.emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": info})
	}
}

// codexRateLimitInfo turns a Codex account/rateLimits/updated into Claude's
// rate_limit_info shape: the 300-minute window as five_hour, the 10080-minute
// one as seven_day (by position when Codex leaves the duration out), status
// rejected at a reached limit or a full window, allowed_warning from 80%. The
// update is sparse: a window it leaves out is left out here too. nil = nothing usable.
func codexRateLimitInfo(params json.RawMessage) map[string]any {
	type window struct {
		UsedPercent        float64 `json:"usedPercent"`
		WindowDurationMins *int64  `json:"windowDurationMins"`
		ResetsAt           *int64  `json:"resetsAt"`
	}
	var p struct {
		RateLimits struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
			Reached   *string `json:"rateLimitReachedType"`
			PlanType  *string `json:"planType"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	rl := p.RateLimits
	windows := map[string]any{}
	status, worst, resets := "", -1.0, int64(0)
	for i, w := range []*window{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		name := []string{"five_hour", "seven_day"}[i]
		if d := w.WindowDurationMins; d != nil && *d >= 24*60 {
			name = "seven_day"
		} else if d != nil {
			name = "five_hour"
		}
		entry := map[string]any{"utilization": w.UsedPercent / 100}
		var at int64
		if w.ResetsAt != nil {
			at = *w.ResetsAt
			entry["resetsAt"] = at
		}
		windows[name] = entry
		if w.UsedPercent > worst {
			worst, resets = w.UsedPercent, at
		}
	}
	switch {
	case rl.Reached != nil || worst >= 100:
		status = "rejected"
	case worst >= 80:
		status = "allowed_warning"
	case worst >= 0:
		status = "allowed"
	}
	if status == "" && len(windows) == 0 {
		return nil
	}
	info := map[string]any{"unifiedWindows": windows}
	if status != "" {
		info["status"] = status
	}
	if resets > 0 {
		info["resetsAt"] = resets
	}
	if rl.PlanType != nil {
		info["planType"] = *rl.PlanType
	}
	return info
}

// A reused execution role must not resume another bead's Codex thread. The
// immutable session is preserved only by an explicitly validated checkpoint.
// NativeCodexThreadFile is the immutable conversation path. Callers must
// obtain the session from a validated canonical run/checkpoint, not role prefs.
func NativeCodexThreadFile(home, role, session string) string {
	sum := sha256.Sum256([]byte(session))
	return CodexThreadFile(home, fmt.Sprintf("%s-session-%x", role, sum))
}
func (s Spec) codexThreadFile(home string) string {
	if s.ExecutionRunID == 0 {
		return CodexThreadFile(home, s.Role)
	}
	return NativeCodexThreadFile(home, s.Role, s.SessionID)
}
