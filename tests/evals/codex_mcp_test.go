package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/testguard"
)

// Launch a recording stub with the generated arguments. No real Codex, MCP,
// browser, chat session, user config or managed config participates.
func TestCodexMCPLaunchScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/codex-mcp/launch.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		User, Managed string
		Steps         []struct {
			Selected string
			Disabled []string
		}
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	paths := []string{filepath.Join(dir, "config.toml"), filepath.Join(dir, "managed_config.toml")}
	for i, s := range []string{scenario.User, scenario.Managed} {
		if err = os.WriteFile(paths[i], []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stub := filepath.Join(dir, "codex-stub")
	if err = os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, step := range scenario.Steps {
		args, err := chat.CodexMCPLaunchArgs(paths, step.Selected)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(stub, args...).Output()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(out), "app-server\n") {
			t.Fatal(string(out))
		}
		for _, name := range []string{"local-browser", "remote-browser", "mcp_agent_mail", "search"} {
			want := false
			for _, n := range step.Disabled {
				want = want || n == name
			}
			if strings.Contains(string(out), "mcp_servers."+name+".enabled=false") != want {
				t.Fatalf("selected %q: %s", step.Selected, out)
			}
		}
	}
	for i, want := range []string{scenario.User, scenario.Managed} {
		got, err := os.ReadFile(paths[i])
		if err != nil || string(got) != want {
			t.Fatalf("config mutated: %v", err)
		}
	}
}

func TestCodexMCPEffectiveTrustScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/codex-mcp/effective-trust.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Project string
		Steps   []struct {
			TrustLayer string `json:"trust_layer"`
			Symlink    bool
			Canonical  bool
			Trust      string
			Disabled   bool
		}
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, step := range scenario.Steps {
		t.Run(fmt.Sprintf("%s/%s/symlink=%t/canonical=%t", step.TrustLayer, step.Trust, step.Symlink, step.Canonical), func(t *testing.T) {
			dir := t.TempDir()
			testguard.NoLeakedProcesses(t, dir)
			repo := filepath.Join(dir, "repo")
			cwd := filepath.Join(repo, "child")
			for _, p := range []string{cwd, filepath.Join(repo, ".git"), filepath.Join(repo, ".codex")} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// HEAD-less metadata between cwd and the trusted parent must not
			// hide the parent's browser from the launch policy.
			if err := os.Mkdir(filepath.Join(cwd, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			if step.Symlink {
				alias := filepath.Join(dir, "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				repo = filepath.Join(alias, "repo")
				cwd = filepath.Join(repo, "child")
			}
			project := filepath.Join(repo, ".codex", "config.toml")
			if err := os.WriteFile(project, []byte(scenario.Project), 0600); err != nil {
				t.Fatal(err)
			}
			system, user, managed := filepath.Join(dir, "system.toml"), filepath.Join(dir, "user.toml"), filepath.Join(dir, "managed_config.toml")
			trustPath := system
			if step.TrustLayer == "managed" {
				trustPath = managed
			}
			if step.TrustLayer != "none" {
				key := repo
				if step.Canonical {
					key, err = filepath.EvalSymlinks(repo)
					if err != nil {
						t.Fatal(err)
					}
				}
				content := "[projects." + strconv.Quote(key) + "]\ntrust_level='" + step.Trust + "'\n"
				if err := os.WriteFile(trustPath, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args, err := chat.CodexMCPLaunchArgsFromConfig(system, user, managed, cwd, "")
			if err != nil {
				t.Fatal(err)
			}
			stub := filepath.Join(dir, "codex-stub")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(stub, args...).Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "mcp_servers.project-browser.enabled=false") != step.Disabled {
				t.Fatalf("%s", out)
			}
			if strings.Contains(string(out), "search") {
				t.Fatalf("nonbrowser changed: %s", out)
			}
			got, err := os.ReadFile(project)
			if err != nil || string(got) != scenario.Project {
				t.Fatal("project config mutated")
			}
		})
	}
}
