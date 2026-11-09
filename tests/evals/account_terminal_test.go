package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The built cmdr against a scripted cmdrd: cmdr account use must not claim a
// role moved when a terminal pane holds it. No claude runs.
func TestAccountUseOnATerminalHeldRoleScenario(t *testing.T) {
	var scenario struct {
		Role, Account string
		Cases         []struct {
			Name, Says, Never string
			Status            map[string]any
		}
	}
	raw, err := os.ReadFile("scenarios/account-terminal-held.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			user, home := t.TempDir(), t.TempDir()
			dir := filepath.Join(home, "claude-accounts", scenario.Account)
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("{}"), 0o600)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": c.Status})
			}))
			defer srv.Close()
			os.WriteFile(filepath.Join(home, "api-url"), []byte(srv.URL), 0o600)
			os.WriteFile(filepath.Join(home, "api-token"), []byte("invented"), 0o600)

			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "account", "use", scenario.Role, scenario.Account)
			cmd.Env = append(os.Environ(), "HOME="+user, "COMMANDER_HOME="+home)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cmdr account use: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), c.Says) || strings.Contains(string(out), c.Never) {
				t.Errorf("printed %q, want %q and never %q", out, c.Says, c.Never)
			}
		})
	}
}
