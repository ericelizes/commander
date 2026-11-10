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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

// The real cmdr's edge verbs against a scripted bd answering in real bd's shapes:
// the parent only in the JSON, the text form with no line for it, so a nested
// workstream reads nested, and a description line that says otherwise does not count.
func TestNestedEdgesScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/nested-edges.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Beads []struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Labels      []string `json:"labels"`
			Parent      string   `json:"parent"`
			BlocksOn    []string `json:"blocks_on"`
		}
		Cases []struct {
			Name   string
			Argv   []string
			Exit   int
			Writes []string
			// Receipt: the user's write put the Manager's "Manager to start" receipt on the waiter
			// before the edge went (the user is never refused, il9.204.3.2).
			Receipt string
			Stderr string
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
		fields := map[string]any{"id": b.ID, "title": b.Title, "status": "open", "description": b.Description, "labels": b.Labels, "dependencies": deps}
		if b.Parent != "" {
			fields["parent"] = b.Parent
		}
		js, _ := json.Marshal(fields)
		text := fmt.Sprintf("○ %s · %s   [P2 · OPEN]\nCreated by: Sample · Type: task\n\nDESCRIPTION\n  %s\n\nLABELS: %s\n", b.ID, b.Title, b.Description, strings.Join(b.Labels, ", "))
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
	t.Setenv("NESTED_EDGES_DB", db)
	t.Setenv("NESTED_EDGES_CALLS", calls)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$NESTED_EDGES_CALLS"
case "$1" in
show)
  shift; ids=""; js=0
  for a in "$@"; do case "$a" in --json) js=1 ;; --*) ;; *) ids="$ids $a" ;; esac; done
  if [ $js = 1 ]; then
    printf '['; sep=''
    for i in $ids; do printf '%s' "$sep"; cat "$NESTED_EDGES_DB/$i.json" || exit 1; sep=','; done
    printf ']\n'
  else
    for i in $ids; do cat "$NESTED_EDGES_DB/$i.txt" || exit 1; done
  fi ;;
dep) case "$2" in list) printf '[]\n' ;; esac ;;
list) printf '[]\n' ;;
comments) case "$2" in add) ;; *) printf '[]\n' ;; esac ;;
update) ;;
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
			cmd := exec.CommandContext(ctx, binary, c.Argv...)
			cmd.Env = envWithout(os.Environ(), "COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			var exit *exec.ExitError
			if err := cmd.Run(); errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			t.Logf("exit %d\nstdout: %s\nstderr: %s", code, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
			if code != c.Exit {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, c.Exit, stdout.String(), stderr.String())
			}
			if c.Stderr != "" && !strings.Contains(stderr.String(), c.Stderr) {
				t.Fatalf("stderr %s, want %q", stderr.String(), c.Stderr)
			}
			log, _ := os.ReadFile(calls)
			var writes []string
			for _, line := range strings.Split(string(log), "\n") {
				if strings.HasPrefix(line, "dep add") || strings.HasPrefix(line, "dep remove") {
					writes = append(writes, line)
				}
			}
			if c.Exit == 0 && slices.Contains(c.Argv, "--leave-unheld") && !strings.Contains(string(log), "comments add") {
				t.Fatalf("--leave-unheld was not recorded on the bead:\n%s", log)
			}
			if c.Receipt != "" {
				at, rm := strings.Index(string(log), "update "+c.Receipt+" --add-label"), strings.Index(string(log), "dep remove")
				if at < 0 || !strings.Contains(string(log), "Manager_20to_20start") || rm < at {
					t.Fatalf("no Manager receipt on %s before the edge went:\n%s", c.Receipt, log)
				}
			}
			if strings.Join(writes, "|") != strings.Join(c.Writes, "|") {
				t.Fatalf("edge writes %q, want %q\ncalls:\n%s", writes, c.Writes, log)
			}
		})
	}
}
