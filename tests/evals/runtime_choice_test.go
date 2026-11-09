package evals

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/example-user/commander/internal/chat"
)

// Exercise the real CLI against a fake chat API and the daemon-down fallback.
// All runtime files live in temporary homes; no real agent is started.
func TestRuntimeChoiceScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/runtime-choice.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Roles   []string
		Runtime string
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "cmdr")
	build := exec.Command("go", "build", "-p", "1", "-o", bin, "../../cmd/cmdr")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, role := range sc.Roles {
		for _, online := range []bool{false, true} {
			mode := "offline"
			if online {
				mode = "api"
			}
			t.Run(role+"/"+mode, func(t *testing.T) {
				home := t.TempDir()
				apiURL := "http://127.0.0.1:1"
				var called atomic.Bool
				if online {
					ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called.Store(true)
						var body struct{ Runtime string }
						if r.Method != "POST" || r.URL.Path != "/api/chat/"+role+"/runtime" ||
							json.NewDecoder(r.Body).Decode(&body) != nil || body.Runtime != sc.Runtime {
							t.Errorf("unexpected runtime request: %s %s", r.Method, r.URL.Path)
							http.Error(w, "bad request", http.StatusBadRequest)
							return
						}
						w.Write([]byte(`{"ok":true}`))
					}))
					defer ts.Close()
					apiURL = ts.URL
				}
				if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(apiURL), 0600); err != nil {
					t.Fatal(err)
				}
				if err := chat.WriteAutoRuntime(home, role, chat.RuntimeClaude); err != nil {
					t.Fatal(err)
				}
				c := exec.Command(bin, "runtime", role, sc.Runtime)
				c.Env = append(os.Environ(), "COMMANDER_HOME="+home, "COMMANDER_ROLE="+role)
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("switch: %v %s", err, out)
				}
				if online && !called.Load() {
					t.Fatal("runtime switch bypassed the chat API")
				}
				// API mode proves request serialization/response only. Offline proves persistence.
				if online {
					return
				}
				query := exec.Command(bin, "runtime", role)
				query.Env = c.Env
				if out, err := query.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != sc.Runtime {
					t.Fatalf("read persisted choice: %v %s", err, out)
				}
				saved, err := os.ReadFile(filepath.Join(home, "chat", role+".runtime"))
				if err != nil {
					t.Fatal(err)
				}
				if !chat.RuntimePinned(home, role) {
					t.Fatal("explicit CLI choice retained automatic marker")
				}
				if string(saved) != "codex\n" && string(saved) != "codex" {
					t.Fatalf("runtime %q", saved)
				}
			})
		}
	}
}
