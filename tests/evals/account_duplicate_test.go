package evals

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
)

// In process, not end to end: this calls chat.Accounts and Engine.NextAccount
// directly, with no binary, launcher or API. TestAccountListBinaryScenario below
// runs the built cmdr over the same scenario.
func TestAccountDuplicateScenario(t *testing.T) {
	var scenario struct {
		Profiles []struct{ Name, Email, UUID, Duplicate string }
	}
	raw, err := os.ReadFile("scenarios/account-duplicate.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	user, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "/invented/inherited")
	for _, p := range scenario.Profiles {
		dir, cfg := filepath.Join(user, ".claude"), filepath.Join(user, ".claude.json")
		if p.Name != "default" {
			dir = filepath.Join(home, "claude-accounts", p.Name)
			cfg = filepath.Join(dir, ".claude.json")
		}
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0600)
		raw, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"emailAddress": p.Email, "accountUuid": p.UUID}})
		os.WriteFile(cfg, raw, 0600)
	}
	accounts := chat.Accounts(home, nil, time.Now())
	for i, p := range scenario.Profiles {
		if accounts[i].Email != p.Email || accounts[i].DuplicateOf != p.Duplicate {
			t.Fatalf("%+v", accounts[i])
		}
	}
	e := chat.New(chat.Options{Home: home})
	if next := e.NextAccount("default"); next != "spare" {
		t.Fatalf("same login counted as spare: %s", next)
	}
	if next := e.NextAccount("second"); next != "spare" {
		t.Fatalf("duplicate source: %s", next)
	}
}

// The built cmdr, under an inherited CLAUDE_CONFIG_DIR, reports the scenario's
// profiles from ~/.claude and marks the same login. No claude runs.
func TestAccountListBinaryScenario(t *testing.T) {
	var scenario struct {
		Profiles []struct{ Name, Email, UUID, Duplicate string }
	}
	raw, err := os.ReadFile("scenarios/account-duplicate.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	user, home := t.TempDir(), t.TempDir()
	for _, p := range scenario.Profiles {
		dir, cfg := filepath.Join(user, ".claude"), filepath.Join(user, ".claude.json")
		if p.Name != "default" {
			dir = filepath.Join(home, "claude-accounts", p.Name)
			cfg = filepath.Join(dir, ".claude.json")
		}
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"test-token"}}`), 0o600)
		raw, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"emailAddress": p.Email, "accountUuid": p.UUID}})
		os.WriteFile(cfg, raw, 0o600)
	}
	binary := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")

	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "account", "list", "--json")
	cmd.Env = append(os.Environ(), "HOME="+user, "COMMANDER_HOME="+home, "CLAUDE_CONFIG_DIR="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cmdr account list: %v", err)
	}
	var listed []struct {
		Name        string `json:"name"`
		Runtime     string `json:"runtime"`
		Email       string `json:"email"`
		Dir         string `json:"dir"`
		DuplicateOf string `json:"duplicate_of"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	seen := 0
	for _, a := range listed {
		for _, p := range scenario.Profiles {
			if a.Runtime == chat.RuntimeClaude && a.Name == p.Name {
				seen++
				if a.Email != p.Email || a.DuplicateOf != p.Duplicate {
					t.Errorf("%s: %+v, want %+v", a.Name, a, p)
				}
				if p.Name == "default" && a.Dir != filepath.Join(user, ".claude") {
					t.Errorf("default dir followed the inherited value: %s", a.Dir)
				}
			}
		}
	}
	if seen != len(scenario.Profiles) {
		t.Fatalf("listed %d of %d profiles:\n%s", seen, len(scenario.Profiles), out)
	}
}
