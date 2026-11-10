package evals

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// Execute the real CLI with a missing (or unreadable) step. It must stop before
// creating a task or deriving a fabricated Manager sender for failure mail.
func TestSpawnMissingStepScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/spawn-missing-step.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Steps      []string
		Expected   string
		Unreadable string
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	isolateSpawnMail(t)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "bd")
	t.Setenv("SPAWN_MISSING_CALLS", calls)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$SPAWN_MISSING_CALLS"
case "$1" in
show)
 case " $* " in
  *" p-unreadable "*) echo 'read unavailable' >&2; exit 1 ;;
 esac
 echo "Issue $2 not found" >&2; exit 1 ;;
list) echo "Issue $2 not found" >&2; exit 1 ;;
*) echo 'unexpected mutation' >&2; exit 1 ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, step := range sc.Steps {
		t.Run(step, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "internal-spawn-operator", "--project", "example", "--step", step, "--title", "Build example", "--writes-workflow")
			out, err := cmd.CombinedOutput()
			// A missing bead stops quietly; a read that merely failed is a spawn
			// failure and takes the report-and-mail path.
			unreadable := step == "p-unreadable"
			want := sc.Expected
			if unreadable {
				want = sc.Unreadable
			}
			if err == nil || !strings.Contains(string(out), want) {
				t.Fatalf("err=%v output=%s", err, out)
			}
			mailed := strings.Contains(string(out), "mailing Commander")
			if unreadable != mailed && unreadable {
				t.Fatalf("unreadable read did not reach the failure mail: %s", out)
			}
			if !unreadable && (mailed || strings.Contains(string(out), "manager-null")) {
				t.Fatalf("attempted failure mail: %s", out)
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
				if !strings.HasPrefix(line, "show ") && !(unreadable && strings.HasPrefix(line, "list ")) {
					t.Fatalf("unexpected operation before target validation: %s", line)
				}
			}
		})
	}
}
