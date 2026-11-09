package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
)

// The real cmdr's internal-spawn-operator --prod-gate against a scripted bd
// holding a closed gate: a retired owner name the installation lists in
// $COMMANDER_HOME/legacy-owner-aliases approves it, no other name does, and
// a name the Manager sets in its own environment never does.
func TestLegacyApproverScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/legacy-approver.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Aliases []string
		Cases   []struct {
			Name, Approver string
			EnvAlias       string // COMMANDER_LEGACY_OWNER_ALIASES the Manager sets itself
			Refused        bool
		}
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testguard.NoLeakedProcesses(t, dir)
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "legacy-owner-aliases"), []byte(strings.Join(sc.Aliases, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("COMMANDER_LEGACY_OWNER_ALIASES", "")
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
	t.Setenv("COMMANDER_BEADS_BIN", script)
	t.Setenv("LEGACY_APPROVER_DIR", dir)
	writeExecutable(t, script, `#!/bin/sh
case "$1" in
show)
  shift; ids=""
  for a in "$@"; do case "$a" in --*) ;; *) ids="$ids $a" ;; esac; done
  printf '['; sep=''
  for id in $ids; do
    printf '%s' "$sep"; sep=','
    case "$id" in
    p-1.7) printf '{"id":"p-1.7","title":"Prod gate","status":"closed","description":"run migration 42","labels":["kind:gate","approver:%s","gate-of:p-1-2"]}' "$(cat "$LEGACY_APPROVER_DIR/approver")" ;;
    # p-1.2 is a step under workstream p-1: a bead with no parent is its own workstream (nested Manager routing), so manager-p-1 would be refused before the gate check
    p-1.2) printf '{"id":"p-1.2","title":"Step","status":"open","parent":"p-1","dependencies":[{"id":"p-1","dependency_type":"parent-child"}],"labels":["kind:objective"]}' ;;
    p-1) printf '{"id":"p-1","title":"Workstream","status":"open","labels":["kind:objective","kind:workstream"]}' ;;
    *) printf '{"id":"%s","title":"Step","status":"open","labels":["kind:objective"]}' "$id" ;;
    esac
  done
  printf ']\n' ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, c := range sc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "approver"), []byte(c.Approver), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "internal-spawn-operator", "--project", "example", "--step", "p-1.2", "--title", "t", "--prod-gate", "p-1.7")
			cmd.Env = append(envWithout(os.Environ(), "COMMANDER_ROLE", "COMMANDER_MANAGER_ROLE"), "COMMANDER_ROLE=manager-p-1", "COMMANDER_LEGACY_OWNER_ALIASES="+c.EnvAlias)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			out := stdout.String() + stderr.String()
			t.Logf("%s", out)
			if got := strings.Contains(out, "not approved by the user"); got != c.Refused {
				t.Fatalf("refused=%v, want %v: %s", got, c.Refused, out)
			}
			if !c.Refused && !strings.Contains(out, "--prod-gate") && !strings.Contains(out, "p-1.7") && stderr.Len() == 0 {
				t.Fatalf("no output at all: %s", out)
			}
		})
	}
}
