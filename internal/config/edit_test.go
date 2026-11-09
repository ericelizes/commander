package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetValueKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	orig := "# my settings\n[runtime]\ndefault = \"claude\" # keep\n\n[api]\nport = 8787\n"
	os.WriteFile(path, []byte(orig), 0o644)
	if err := SetValue(path, "runtime", "codex_manager_share", "0.25"); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(path, "runtime", "codex_manager_share", "0.5"); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(path, "user", "name", `"Ada"`); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	want := "# my settings\n[runtime]\ndefault = \"claude\" # keep\ncodex_manager_share = 0.5\n\n[api]\nport = 8787\n\n[user]\nname = \"Ada\"\n"
	if string(raw) != want {
		t.Fatalf("file =\n%s\nwant\n%s", raw, want)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Runtime.CodexManagerShare == nil || *cfg.Runtime.CodexManagerShare != 0.5 || UserName(cfg) != "Ada" {
		t.Fatalf("load: %+v %v", cfg, err)
	}
	if err := SetValue(path, "runtime", "codex_operator_share", "2"); err == nil || !strings.Contains(err.Error(), "between 0 and 1") {
		t.Fatalf("out of range accepted: %v", err)
	}
	if raw2, _ := os.ReadFile(path); string(raw2) != want {
		t.Error("a refused edit changed the file")
	}
}

func TestSetValueCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SetValue(path, "runtime", "codex_operator_share", "0.3"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "[runtime]\ncodex_operator_share = 0.3\n" {
		t.Fatalf("file = %q", raw)
	}
}

func TestUserNameFallsBackToThisMachinesUser(t *testing.T) {
	// No name configured: this machine's own git name or account, never a
	// fixed placeholder (the app shows whoever runs it).
	for _, cfg := range []*Config{nil, {}, {User: User{Name: "  "}}} {
		if got := UserName(cfg); strings.TrimSpace(got) == "" || got == "the operator" {
			t.Fatalf("default = %q", got)
		}
	}
	if got := UserName(&Config{User: User{Name: " Alice "}}); got != "Alice" {
		t.Fatalf("configured = %q", got)
	}
}
