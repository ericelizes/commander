package chattest

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// RunFakeCodex plays the recorded app-server message shapes without executing
// model/tool calls. Shared by native-host and scheduler integration fixtures.
func RunFakeCodex(fromBridge io.Reader, toBridge io.Writer, seen chan<- string) {
	sc := bufio.NewScanner(fromBridge)
	write := func(v any) {
		raw, _ := json.Marshal(v)
		_, _ = toBridge.Write(append(raw, '\n'))
	}
	const thread, turn = "th-1", "tu-1"
	for sc.Scan() {
		var m struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if seen != nil {
			seen <- m.Method
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"id": m.ID, "result": map[string]any{"userAgent": "codex"}})
		case "thread/start", "thread/resume":
			write(map[string]any{"id": m.ID, "result": map[string]any{"model": "gpt-5.5", "thread": map[string]any{"id": thread, "cliVersion": "0.163.0"}}})
		case "turn/start":
			if path := os.Getenv("CMDR_FAKE_CODEX_TURN_RELEASE"); path != "" {
				for {
					if _, err := os.Stat(path); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			write(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}}})
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn}}})
			write(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "ls", "cwd": "/tmp"}}})
			write(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "ls", "aggregatedOutput": "a.txt\n", "exitCode": 0}}})
			write(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]any{"type": "agentMessage", "id": "msg-1", "text": ""}}})
			for _, d := range []string{"Hi", "! 7 is prime."} {
				write(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"itemId": "msg-1", "delta": d}})
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "agentMessage", "id": "msg-1", "text": "Hi! 7 is prime."}}})
			// One update per model call: "last" is that call, "total" the thread's.
			for i, last := range []map[string]any{
				{"totalTokens": 15422, "inputTokens": 15362, "cachedInputTokens": 12288, "outputTokens": 60, "reasoningOutputTokens": 0},
				{"totalTokens": 15506, "inputTokens": 15485, "cachedInputTokens": 15232, "outputTokens": 21, "reasoningOutputTokens": 0},
			} {
				total := map[string]any{"totalTokens": 900000 + i} // a resumed thread's total includes its history
				write(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": thread, "turnId": turn,
					"tokenUsage": map[string]any{"last": last, "total": total}}})
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}}})
		case "turn/interrupt":
			write(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "account/rateLimits/read":
			write(map[string]any{"id": m.ID, "result": map[string]any{"rateLimits": map[string]any{
				"primary":   map[string]any{"usedPercent": 12, "windowDurationMins": 300, "resetsAt": 1793000000},
				"secondary": map[string]any{"usedPercent": 40, "windowDurationMins": 10080, "resetsAt": 1793500000},
			}}})
		}
	}
}

// MaybeRunFakeCodex re-execs a test binary as an isolated app-server fixture.
func MaybeRunFakeCodex() {
	if os.Getenv("CMDR_FAKE_CODEX") != "1" || len(os.Args) < 2 {
		return
	}
	if os.Args[1] == "features" {
		os.Stdout.WriteString("hooks stable true\n")
		os.Exit(0)
	}
	if os.Args[1] != "app-server" {
		return
	}
	dir := os.Getenv(DirVar)
	raw, _ := json.Marshal(os.Args[1:])
	_ = os.WriteFile(filepath.Join(dir, "codex-argv.json"), raw, 0600)
	record := map[string]any{"argv": os.Args[1:], "run_id": os.Getenv("COMMANDER_RUN_ID"), "run_session": os.Getenv("COMMANDER_RUN_SESSION"), "role": os.Getenv("COMMANDER_ROLE"), "manager_role": os.Getenv("COMMANDER_MANAGER_ROLE")}
	record["cwd"], _ = os.Getwd()
	raw, _ = json.Marshal(record)
	_ = os.WriteFile(filepath.Join(dir, "argv-"+os.Getenv("COMMANDER_RUN_SESSION")+".json"), raw, 0600)
	file, _ := os.OpenFile(filepath.Join(dir, "codex-requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	RunFakeCodex(io.TeeReader(os.Stdin, file), os.Stdout, nil)
	os.Exit(0)
}
