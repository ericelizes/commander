// fakecodex is a scripted `codex app-server` for the evals: it answers the
// bridge's JSON-RPC calls and, once the turn starts, replays the notifications
// of the scenario named by $FAKE_CODEX_SCENARIO, as Codex does when a native
// child agent runs inside the parent's turn.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type note struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type scenario struct {
	Thread         string `json:"thread"`
	Turn           string `json:"turn"`
	ParentProgress []note `json:"parent_progress"`
	Notifications  []note `json:"notifications"`
	ParentFinal    []note `json:"parent_final"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "app-server" {
		fmt.Fprintln(os.Stderr, "fakecodex: only app-server is scripted")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Getenv("FAKE_CODEX_SCENARIO"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex:", err)
		os.Exit(2)
	}
	var sc scenario
	if err := json.Unmarshal(raw, &sc); err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex:", err)
		os.Exit(2)
	}
	out := json.NewEncoder(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 64<<20)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue // a notification such as "initialized"
		}
		result := map[string]any{}
		switch req.Method {
		case "thread/start", "thread/resume":
			result = map[string]any{"model": "fake", "thread": map[string]any{"id": sc.Thread, "cliVersion": "0"}}
		case "turn/start":
			result = map[string]any{"turn": map[string]any{"id": sc.Turn}}
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if req.Method == "turn/start" {
			for _, group := range [][]note{sc.ParentProgress, sc.Notifications, sc.ParentFinal} {
				for _, n := range group {
					_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": n.Method, "params": n.Params})
				}
			}
		}
	}
}
