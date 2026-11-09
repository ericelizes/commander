package evals

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
)

// The real CLI projects identity/provider without exporting tokens or changing
// the auth file. The app's Settings integration tests cover connected-only UI.
func TestAccountProviderIdentityScenario(t *testing.T) {
	var scenario struct {
		Email     string `json:"email"`
		AccountID string `json:"account_id"`
		Provider  string `json:"provider"`
	}
	raw, err := os.ReadFile("scenarios/account-provider-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	user, home, codex := t.TempDir(), t.TempDir(), t.TempDir()
	claims, _ := json.Marshal(map[string]string{"email": scenario.Email})
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	auth, _ := json.Marshal(map[string]any{"tokens": map[string]string{"id_token": token, "account_id": scenario.AccountID}})
	path := filepath.Join(codex, "auth.json")
	if err := os.WriteFile(path, auth, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "account", "list", "--json")
	cmd.Env = append(os.Environ(), "HOME="+user, "COMMANDER_HOME="+home, "CODEX_HOME="+codex)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var listed []chat.AccountView
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range listed {
		if a.Runtime == scenario.Provider {
			found = true
			if a.Email != scenario.Email || a.AccountUUID != scenario.AccountID || !a.LoggedIn {
				t.Fatal("identity/provider missing")
			}
		}
	}
	if !found {
		t.Fatal("provider missing")
	}
	if bytes.Contains(out, []byte(token)) || bytes.Contains(out, []byte(`"tokens"`)) {
		t.Fatal("credentials exposed")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(auth) {
		t.Fatal("credential file changed")
	}
}
