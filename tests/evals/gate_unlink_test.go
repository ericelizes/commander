package evals

import (
	"bytes"
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

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

// The real cmdr's internal-gate unlink against a scripted bd holding a
// superseded step and a live step that wait on an open user gate: only the
// owning Manager's call removes an edge, only the superseded step's, and the
// gate is never closed or relabelled.
func TestGateUnlinkScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/gate-unlink.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Gate  string
		Beads []struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			Status      string   `json:"status"`
			CloseReason string   `json:"close_reason"`
			Labels      []string `json:"labels"`
			Parent      string   `json:"parent"`
			BlocksOn    []string `json:"blocks_on"`
			Notes       []string `json:"notes"`
		}
		Cases []struct {
			Name, Role, Waiters string
			Exit                int
			NoNote              bool
			Removes             []string
			Stdout, Stderr      string
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	db := filepath.Join(dir, "db")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, b := range sc.Beads {
		type dep struct {
			ID   string `json:"id"`
			Type string `json:"dependency_type"`
		}
		var deps []dep
		if b.Parent != "" {
			deps = append(deps, dep{b.Parent, "parent-child"})
		}
		for _, on := range b.BlocksOn {
			deps = append(deps, dep{on, "blocks"})
		}
		var comments []map[string]any
		for i, n := range b.Notes {
			comments = append(comments, map[string]any{"id": i + 1, "text": n})
		}
		js, _ := json.Marshal(map[string]any{"id": b.ID, "title": b.Title, "status": b.Status, "close_reason": b.CloseReason, "labels": b.Labels, "dependencies": deps, "comments": comments})
		text := fmt.Sprintf("ID: %s\nTitle: %s\nStatus: %s\nLabels: %s\n", b.ID, b.Title, b.Status, strings.Join(b.Labels, ", "))
		if err := os.WriteFile(filepath.Join(db, b.ID+".json"), js, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(db, b.ID+".txt"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1")
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "bd")
	calls := filepath.Join(dir, "calls")
	t.Setenv("GATE_UNLINK_DB", db)
	t.Setenv("GATE_UNLINK_CALLS", calls)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$GATE_UNLINK_CALLS"
case "$1" in
show)
  shift; ids=""; js=0
  for a in "$@"; do case "$a" in --json) js=1 ;; --*) ;; *) ids="$ids $a" ;; esac; done
  if [ $js = 1 ]; then
    printf '['; sep=''
    for i in $ids; do printf '%s' "$sep"; cat "$GATE_UNLINK_DB/$i.json" || exit 1; sep=','; done
    printf ']\n'
  else
    for i in $ids; do cat "$GATE_UNLINK_DB/$i.txt" || exit 1; done
  fi ;;
dep|comments) ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, c := range sc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			_ = os.Remove(calls)
			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "internal-gate", "unlink", "--project", "example", "--bead", sc.Gate, "--waiter", c.Waiters, "--reason", "dropped plan")
			cmd.Env = append(envWithout(os.Environ(), "COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"), "COMMANDER_ROLE="+c.Role)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			t.Logf("exit %d\nstdout: %s\nstderr: %s", code, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
			if code != c.Exit {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.Exit, stdout.String(), stderr.String())
			}
			// JSON escapes the quotes of the error; compare unescaped text.
			errText := strings.ReplaceAll(stderr.String(), `\"`, `"`)
			if c.Stderr != "" && !strings.Contains(errText, c.Stderr) {
				t.Fatalf("stderr %s, want %q", errText, c.Stderr)
			}
			if c.Stdout != "" && !strings.Contains(stdout.String(), c.Stdout) {
				t.Fatalf("stdout %s, want %q", stdout.String(), c.Stdout)
			}
			log, _ := os.ReadFile(calls)
			var removes []string
			for _, line := range strings.Split(string(log), "\n") {
				if strings.HasPrefix(line, "dep ") {
					removes = append(removes, line)
				}
				for _, w := range []string{"close", "update", "label", "set-state", "reopen"} {
					if strings.HasPrefix(line, w+" ") {
						t.Fatalf("wrote beyond the edge: %s", line)
					}
				}
			}
			if strings.Join(removes, "|") != strings.Join(c.Removes, "|") {
				t.Fatalf("edge writes %q, want %q", removes, c.Removes)
			}
			noted := strings.Contains(string(log), "comments add "+sc.Gate)
			if c.Exit == 0 && !c.NoNote && !noted {
				t.Fatalf("no audit note on the gate: %s", log)
			}
			if (c.Exit != 0 || c.NoNote) && noted {
				t.Fatalf("unexpected note on the gate: %s", log)
			}
		})
	}
}

func envWithout(env []string, keys ...string) []string {
	var out []string
	for _, e := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(e, k+"=") {
				drop = true
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
