package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/example-user/commander/internal/chat/chattest"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCodex plays `codex app-server` with the message shapes captured from a
// live 0.163 session.
func fakeCodex(fromBridge io.Reader, toBridge io.Writer, seen chan<- string) {
	chattest.RunFakeCodex(fromBridge, toBridge, seen)
}

func TestCodexBridgeSpeaksStreamJSON(t *testing.T) {
	dir := t.TempDir()
	prompt := filepath.Join(dir, "role.md")
	os.WriteFile(prompt, []byte("You are a Manager."), 0o600)
	cfg := CodexBridgeConfig{Cwd: dir, PromptPath: prompt, ThreadFile: filepath.Join(dir, "x.codex-thread"), SessionID: "sess-1", SkipPermissions: true}

	engineIn, engineInW := io.Pipe()   // engine -> bridge
	engineOutR, engineOut := io.Pipe() // bridge -> engine
	codexInR, codexIn := io.Pipe()     // bridge -> codex
	codexOutR, codexOut := io.Pipe()   // codex -> bridge
	seen := make(chan string, 64)
	go fakeCodex(codexInR, codexOut, seen)
	go func() { _ = RunCodexBridge(context.Background(), cfg, engineIn, engineOut, codexOutR, codexIn) }()

	lines := make(chan []byte, 64)
	go func() {
		sc := bufio.NewScanner(engineOutR)
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
	}()
	n := NewNormalizer(0, true, nil)
	var events []Event
	var acks []string
	collect := func(until func() bool) {
		deadline := time.After(5 * time.Second)
		for !until() {
			select {
			case l := <-lines:
				var raw struct {
					Type     string `json:"type"`
					Response struct {
						RequestID string `json:"request_id"`
					} `json:"response"`
				}
				_ = json.Unmarshal(l, &raw)
				if raw.Type == "control_response" {
					acks = append(acks, raw.Response.RequestID)
				}
				evs, _ := n.Line(l)
				events = append(events, evs...)
			case <-deadline:
				t.Fatalf("timed out; events so far: %d", len(events))
			}
		}
	}
	has := func(k Kind) bool {
		for _, e := range events {
			if e.Kind == k {
				return true
			}
		}
		return false
	}
	collect(func() bool { return has(KindSession) && has(KindRateLimit) })
	if got, _ := os.ReadFile(cfg.ThreadFile); strings.TrimSpace(string(got)) != "th-1" {
		t.Fatalf("thread id not kept for resume: %q", got)
	}

	user, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "say hi"}})
	engineInW.Write(append(user, '\n'))
	collect(func() bool { return has(KindResult) })
	var text string
	for _, e := range events {
		if e.Kind == KindText {
			text = e.Text
		}
	}
	if text != "Hi! 7 is prime." {
		t.Errorf("final text = %q", text)
	}
	for _, k := range []Kind{KindTextDelta, KindToolStart, KindToolInput, KindToolResult} {
		if !has(k) {
			t.Errorf("no %s event (of %d)", k, len(events))
		}
	}

	ctrl, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "cmdr-interrupt-1", "request": map[string]any{"subtype": "interrupt"}})
	engineInW.Write(append(ctrl, '\n'))
	collect(func() bool { return len(acks) > 0 })
	if acks[0] != "cmdr-interrupt-1" {
		t.Errorf("interrupt ack = %v", acks)
	}
}

func TestCodexInputMapsTextAndImages(t *testing.T) {
	in := codexInput(json.RawMessage(`{"role":"user","content":"hello"}`))
	if len(in) != 1 || in[0]["text"] != "hello" {
		t.Fatalf("text: %v", in)
	}
	in = codexInput(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}`))
	if len(in) != 2 || in[1]["type"] != "localImage" {
		t.Fatalf("blocks: %v", in)
	}
	os.Remove(in[1]["path"].(string))
}

func TestReadRuntimeDefaultsToClaude(t *testing.T) {
	home := t.TempDir()
	if ReadRuntime(home, "manager-x") != RuntimeClaude {
		t.Fatal("no file must mean claude")
	}
	os.MkdirAll(filepath.Join(home, "chat"), 0o700)
	os.WriteFile(RuntimeFile(home, "manager-x"), []byte("codex\n"), 0o600)
	if ReadRuntime(home, "manager-x") != RuntimeCodex {
		t.Fatal("runtime file says codex")
	}
}

// An Operator's bridge (--prompt) runs one turn without stdin and exits, like claude -p.
func TestCodexBridgeOneShotExitsAfterTheTurn(t *testing.T) {
	dir := t.TempDir()
	cfg := CodexBridgeConfig{Cwd: dir, ThreadFile: filepath.Join(dir, "op.codex-thread"), SessionID: "sess-op", SkipPermissions: true, Prompt: "read your bead"}
	codexInR, codexIn := io.Pipe()
	codexOutR, codexOut := io.Pipe()
	go fakeCodex(codexInR, codexOut, nil)
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- RunCodexBridge(context.Background(), cfg, strings.NewReader(""), &out, codexOutR, codexIn)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("one-shot bridge did not exit after its turn")
	}
	if !strings.Contains(out.String(), `"type":"result"`) || !strings.Contains(out.String(), "7 is prime") {
		t.Fatalf("output: %s", out.String())
	}
}

// Bridge rate-limit notifications can finish after RunCodexBridge returns.
// Keep test output readable while those background emitters finish.
type bridgeTestBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bridgeTestBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *bridgeTestBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A hooked bridge trusts its own -c hooks for the thread, and its result line
// carries the turn's tokens the way Claude's does (uncached input + output),
// summed over the turn's model calls, not the thread's carried-over total.
func TestCodexBridgeHooksTrustAndTokenUsage(t *testing.T) {
	dir := t.TempDir()
	cfg := CodexBridgeConfig{Cwd: dir, ThreadFile: filepath.Join(dir, "op.codex-thread"), SessionID: "sess-op", SkipPermissions: true,
		Prompt: "read your bead", HookRole: "operator-op-x", GuardOnly: true}
	codexInR, codexIn := io.Pipe()
	codexOutR, codexOut := io.Pipe()
	var sent strings.Builder
	fakeDone := make(chan struct{})
	go func() {
		defer close(fakeDone)
		fakeCodex(io.TeeReader(codexInR, &sent), codexOut, nil)
	}()
	defer codexIn.Close()
	defer codexOutR.Close()
	var out bridgeTestBuffer
	err := RunCodexBridge(context.Background(), cfg, strings.NewReader(""), &out, codexOutR, codexIn)
	// The bridge returning does not join the fake's TeeReader. End its I/O
	// and join it before inspecting the builder it owns.
	_ = codexIn.Close()
	_ = codexOutR.Close()
	select {
	case <-fakeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("fake Codex did not finish")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.String(), `"method":"thread/start","params":{`) || !strings.Contains(sent.String(), `"config":{"bypass_hook_trust":true}`) {
		t.Fatalf("thread/start does not trust the session's hooks: %s", sent.String())
	}
	var res struct {
		SessionID string           `json:"session_id"`
		Usage     map[string]int64 `json:"usage"`
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, `"type":"result"`) {
			_ = json.Unmarshal([]byte(line), &res)
		}
	}
	if res.SessionID != "sess-op" {
		t.Errorf("result line has no session_id (the watchdog skips it): %q", res.SessionID)
	}
	want := map[string]int64{"input_tokens": (15362 - 12288) + (15485 - 15232), "cache_read_input_tokens": 12288 + 15232, "output_tokens": 81}
	for k, v := range want {
		if res.Usage[k] != v {
			t.Errorf("usage %s = %d, want %d (%v)", k, res.Usage[k], v, res.Usage)
		}
	}
}

// The app-server gets the agent's hooks as -c overrides; no role, no hooks.
func TestCodexAppServerArgvCarriesHooks(t *testing.T) {
	home := t.TempDir()
	argsFor := func(cfg CodexBridgeConfig) []string {
		cfg.Cwd = home
		args, err := codexAppServerArgv(cfg, home)
		if err != nil {
			t.Fatal(err)
		}
		return args
	}
	argv := strings.Join(argsFor(CodexBridgeConfig{HookRole: "manager-ws"}), " ")
	for _, want := range []string{"hooks.PreToolUse=", "internal-manager-guard --role manager-ws", `matcher="Bash|apply_patch|Edit|Write"`,
		"hooks.Stop=", "hooks.UserPromptSubmit=", "internal-mail-hook --role manager-ws --event Stop"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	argv = strings.Join(argsFor(CodexBridgeConfig{HookRole: "operator-op-x", GuardOnly: true}), " ")
	if !strings.Contains(argv, "internal-operator-guard --role operator-op-x") || strings.Contains(argv, "mail-hook") {
		t.Errorf("one-shot Operator: %s", argv)
	}
	if argv := argsFor(CodexBridgeConfig{}); strings.Contains(strings.Join(argv, " "), "hooks.") {
		t.Errorf("no hook role: %v", argv)
	}
	args := CodexBridgeArgs(Spec{Role: "manager-ws", Agent: "manager-ws", SessionID: "s"}, home)
	if !strings.Contains(strings.Join(args, " "), "--hooks-role manager-ws") {
		t.Errorf("chat bridge args lack the hook role: %v", args)
	}
}
