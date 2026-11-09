package chat

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexBridgeChildNotificationsDoNotFinishParent(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "tests", "evals", "scenarios", "codex-child", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("missing scenarios", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) { testCodexChildNotifications(t, file) })
	}
}

func testCodexChildNotifications(t *testing.T, file string) {
	var out bytes.Buffer
	b := &codexBridge{cfg: CodexBridgeConfig{Prompt: "review"}, out: json.NewEncoder(&out), rpc: io.Discard, pending: map[int]chan rpcReply{}, thread: "parent", turn: "parent-turn", turnAt: time.Now(), lastText: "parent progress", done: make(chan error, 3)}
	fixture, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Notifications []struct {
			Method string
			Params json.RawMessage
		}
	}
	if err := json.Unmarshal(fixture, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Notifications) == 0 {
		t.Fatal("incomplete child completion scenario")
	}
	for _, event := range scenario.Notifications {
		b.notification(event.Method, event.Params)
	}

	if b.turn != "parent-turn" || b.lastText != "parent progress" || b.usage.Input != 0 || out.Len() != 0 {
		t.Fatalf("child altered parent: turn=%q text=%q usage=%+v output=%s", b.turn, b.lastText, b.usage, &out)
	}
	select {
	case <-b.done:
		t.Fatal("child completed parent bridge")
	default:
	}
	b.notification("turn/completed", json.RawMessage(`{"threadId":"parent","turn":{"id":"parent-turn","status":"completed"}}`))
	select {
	case err := <-b.done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("parent did not finish")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"result":"parent progress"`)) {
		t.Fatalf("missing parent result: %s", &out)
	}
}

func TestCodexBridgeParentCompletionIdentity(t *testing.T) {
	for _, thread := range []string{"parent", ""} {
		for _, status := range []string{"completed", "failed", "interrupted"} {
			t.Run(thread+"/"+status, func(t *testing.T) {
				var out bytes.Buffer
				b := &codexBridge{cfg: CodexBridgeConfig{Prompt: "review"}, out: json.NewEncoder(&out), rpc: io.Discard, pending: map[int]chan rpcReply{}, thread: "parent", turn: "parent-turn", turnAt: time.Now(), lastText: "parent report", usage: codexTokens{Input: 7}, done: make(chan error, 3)}
				raw, _ := json.Marshal(map[string]any{"threadId": thread, "turn": map[string]any{"id": "parent-turn", "status": status}})
				b.notification("turn/completed", raw)
				b.notification("turn/completed", raw)
				if len(b.done) != 1 || bytes.Count(out.Bytes(), []byte(`"type":"result"`)) != 1 {
					t.Fatalf("completion count: done=%d output=%s", len(b.done), &out)
				}
				err := <-b.done
				if (err != nil) != (status == "failed") {
					t.Fatalf("status=%s error=%v", status, err)
				}
				var result map[string]any
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result["result"] != "parent report" || result["usage"].(map[string]any)["input_tokens"] != float64(7) {
					t.Fatalf("parent result: %v", result)
				}
				if status == "interrupted" && result["terminal_reason"] != "aborted_streaming" {
					t.Fatalf("interrupted: %v", result)
				}
			})
		}
	}
}
