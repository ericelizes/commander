package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

// Exercise the real CLI and store against a synthetic server. The server
// rejects all tools except identity reads and the explicitly expected send.
func TestExistingAgentMailScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/existing-agent-mail.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		ProjectKey string `json:"project_key"`
		SenderRole string `json:"sender_role"`
		SenderName string `json:"sender_name"`
		Recipient  string
		Steps      []struct {
			Route, Identity, Error string
			Sent, Attempted        bool
			NoCalls                bool   `json:"no_calls"`
			CallerRole             string `json:"caller_role"`
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	home := filepath.Join(dir, "ch")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_REPO", sc.ProjectKey)
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fixture")
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertAmAgent(context.Background(), sc.ProjectKey, sc.SenderRole, sc.SenderName); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	for _, step := range sc.Steps {
		t.Run(step.Route+"/"+step.Identity+"/"+step.CallerRole, func(t *testing.T) {
			t.Setenv("COMMANDER_ROLE", step.CallerRole)
			t.Setenv("COMMANDER_MANAGER_ROLE", "")
			var mu sync.Mutex
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var req struct {
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				calls = append(calls, req.Params.Name)
				a := req.Params.Arguments
				if a["project_key"] != sc.ProjectKey {
					t.Errorf("project=%v", a)
				}
				payload, isErr := `{}`, false
				switch req.Params.Name {
				case "whois":
					name, id, pid := sc.SenderName, 1, 9
					retired := ""
					if a["agent_name"] == sc.Recipient {
						name, id = sc.Recipient, 2
						switch step.Identity {
						case "normalized":
							name = strings.ToLower(name)
						case "wrong_project":
							pid = 10
						case "retired":
							retired = `,"retired_at":"2026-01-01"`
						}
					}
					payload = fmt.Sprintf(`{"id":%d,"project_id":%d,"name":%q%s}`, id, pid, name, retired)
					if a["agent_name"] == sc.Recipient {
						switch step.Identity {
						case "absent":
							payload, isErr = fmt.Sprintf("Agent '%s' not found in project '%s'", sc.Recipient, sc.ProjectKey), true
						case "whois_failure":
							payload, isErr = "database is busy", true
						case "unsupported":
							payload, isErr = "Unknown tool: whois", true
						}
					}
				case "send_message":
					if !step.Sent && !step.Attempted || a["sender_name"] != sc.SenderName || fmt.Sprint(a["to"]) != "["+sc.Recipient+"]" || a["body_md"] != "synthetic handoff" || a["ack_required"] != true {
						t.Errorf("unexpected send: %v", a)
					}
					if v, sent := a["auto_contact_if_blocked"]; !sent || v != false {
						t.Errorf("auto_contact_if_blocked=%v sent=%v, want explicit false", v, sent)
					}
					if step.Identity == "contact_required" {
						payload, isErr = "CONTACT_REQUIRED: no approved contact link", true
					}
				default:
					t.Errorf("forbidden tool %s", req.Params.Name)
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": payload}}, "isError": isErr}})
			}))
			defer srv.Close()
			t.Setenv("AM_HTTP_URL", srv.URL)
			flag := "--to-existing-agent"
			if step.Route == "role" {
				flag = "--to"
			}
			cmd := exec.Command(bin, "internal-mail", "--from", sc.SenderRole, flag, sc.Recipient, "--subject", "fixture handoff", "--body", "synthetic handoff", "--ack")
			out, err := cmd.CombinedOutput()
			if (err == nil) != step.Sent {
				t.Fatalf("out=%s err=%v", out, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if (step.Sent || step.Attempted) && strings.Join(calls, ",") != "whois,whois,send_message" {
				t.Fatalf("calls=%v", calls)
			}
			if !step.Sent && !step.Attempted && strings.Contains(strings.Join(calls, ","), "send_message") {
				t.Fatalf("unexpected send: %v", calls)
			}
			if step.NoCalls && len(calls) != 0 {
				t.Fatalf("refused caller reached the server: %v", calls)
			}
			if !step.Sent && strings.Contains(string(out), `"sent":true`) {
				t.Fatalf("reported a send: %s", out)
			}
			if !strings.Contains(string(out), step.Error) {
				t.Fatalf("out=%s, want %q", out, step.Error)
			}
			rows, err := st.ListAmAgents(context.Background(), sc.ProjectKey)
			if err != nil || len(rows) != 1 || rows[0].Role != sc.SenderRole {
				t.Fatalf("registry changed: %v %v", rows, err)
			}
			t.Logf("route=%s identity=%s sent=%v calls=%v", step.Route, step.Identity, step.Sent, calls)
		})
	}
}
