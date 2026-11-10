package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/example-user/commander/internal/chat"
)

// Run the user's CLI flow in an isolated home. MCP-only credential files
// must not create phantom accounts; Codex is selected using the account verb.
func TestUnifiedAccountsScenario(t *testing.T) {
	var sc struct {
		Role, Account string
		StaleProfile  string `json:"stale_profile"`
	}
	raw, err := os.ReadFile("scenarios/unified-accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	user, home, codex := t.TempDir(), t.TempDir(), t.TempDir()
	dir := filepath.Join(home, "claude-accounts", sc.StaleProfile)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"mcpOAuth":{"example":{"accessToken":"not-a-Claude-login"}}}`), 0600)
	os.WriteFile(filepath.Join(codex, "auth.json"), []byte(`{"tokens":{"access_token":"test-token"}}`), 0600)
	os.WriteFile(filepath.Join(home, "api-url"), []byte("http://127.0.0.1:1"), 0600)
	bin := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	env := append(os.Environ(), "HOME="+user, "COMMANDER_HOME="+home, "CODEX_HOME="+codex)
	list := exec.Command(bin, "account", "list", "--json")
	list.Env = env
	out, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var accounts []chat.AccountView
	if err := json.Unmarshal(out, &accounts); err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if a.Name == sc.StaleProfile && a.LoggedIn {
			t.Fatal("phantom connected account")
		}
	}
	use := exec.Command(bin, "account", "use", sc.Role, sc.Account)
	use.Env = env
	if out, err := use.CombinedOutput(); err != nil {
		t.Fatalf("account use: %v %s", err, out)
	}
	if chat.ReadRuntime(home, sc.Role) != chat.RuntimeCodex {
		t.Fatal("account selection did not choose Codex")
	}
}
