package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The built cmdr on a private home whose usage file (as cmdrd keeps it) says
// Codex and one Claude account are out: a spawn or provider switch that names
// either is refused with an account that has usage, before anything is
// created, ended or registered. No agent, store or cmdrd runs.
func TestSpawnUsageScenario(t *testing.T) {
	var scenario struct {
		Accounts []string
		Usage    map[string]any
		Cases    []struct {
			Name        string
			Argv        []string
			Exit        int
			Says, Never []string
		}
	}
	raw, err := os.ReadFile("scenarios/spawn-usage.json")
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
			for _, d := range []string{filepath.Join(user, ".claude")} {
				os.MkdirAll(d, 0o700)
				os.WriteFile(filepath.Join(d, ".credentials.json"), []byte("{}"), 0o600)
			}
			for _, a := range scenario.Accounts {
				d := filepath.Join(home, "claude-accounts", a)
				os.MkdirAll(d, 0o700)
				os.WriteFile(filepath.Join(d, ".credentials.json"), []byte("{}"), 0o600)
			}
			usage, _ := json.Marshal(scenario.Usage)
			os.MkdirAll(filepath.Join(home, "chat"), 0o700)
			if err := os.WriteFile(filepath.Join(home, "chat", "usage.json"), usage, 0o600); err != nil {
				t.Fatal(err)
			}
			before := treeOf(t, home)

			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, c.Argv...)
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "COMMANDER_ROLE=") && !strings.HasPrefix(kv, "COMMANDER_MANAGER_ROLE=") && !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
					env = append(env, kv)
				}
			}
			cmd.Env = append(env, "HOME="+user, "COMMANDER_HOME="+home, "AM_HTTP_URL=http://127.0.0.1:1", "COMMANDER_WRITE_DOOR=off")
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.Exit {
				t.Fatalf("exit %d, want %d:\n%s", code, c.Exit, out)
			}
			for _, w := range c.Says {
				if !strings.Contains(string(out), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range c.Never {
				if strings.Contains(string(out), w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
			if c.Exit != 0 {
				if after := treeOf(t, home); after != before {
					t.Errorf("a refusal wrote to the home:\n%s\n->\n%s", before, after)
				}
			}
		})
	}
}

// treeOf lists every file under dir with its size and modification time, so
// a write anywhere below it (chat/<role>.account, a runtime file) shows.
func treeOf(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(&b, "%s %d %d\n", rel, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
