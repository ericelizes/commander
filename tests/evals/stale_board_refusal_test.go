package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

type staleBoardBead struct {
	Status   string   `json:"status"`
	Labels   []string `json:"labels"`
	Parent   string   `json:"parent"`
	BlocksOn []string `json:"blocks_on"`
}

// writeBeadsFixture writes what `bd show <id> [--json]` prints for each bead.
func writeBeadsFixture(t *testing.T, dir string, db map[string]staleBoardBead) {
	t.Helper()
	for id, b := range db {
		deps := []map[string]string{}
		if b.Parent != "" {
			deps = append(deps, map[string]string{"id": b.Parent, "dependency_type": "parent-child"})
		}
		for _, on := range b.BlocksOn {
			deps = append(deps, map[string]string{"id": on, "dependency_type": "blocks"})
		}
		raw, err := json.Marshal(map[string]any{"id": id, "title": id, "status": b.Status, "labels": b.Labels, "dependencies": deps})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		text := "○ " + id + " · " + id + "   [P2 · " + strings.ToUpper(b.Status) + "]\n\nLABELS: " + strings.Join(b.Labels, ", ") + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".txt"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The real cmdr against a board that lags a write (a fake cmdrd serving the
// snapshot) and a scripted beads binary holding the truth. A spawn or a state
// write the board calls too early is refused only when a fresh read agrees; a
// false refusal cost a Manager a replacement bead.
func TestStaleBoardRefusalScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/stale-board-refusal.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Board     json.RawMessage           `json:"board"`
		DB        map[string]staleBoardBead `json:"db"`
		StateArgs []string                  `json:"state_args"`
		SpawnArgs []string                  `json:"spawn_args"`
		Cases     []struct {
			Name      string                    `json:"name"`
			Edit      map[string]staleBoardBead `json:"edit"`
			ShowFails string                    `json:"show_fails"`
			Refused   bool                      `json:"refused"`
			Only      string                    `json:"only"` // spawn's own gate check reads the blocker first
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "eval")
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	cmdrd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Write(sc.Board)
	}))
	defer cmdrd.Close()
	if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(cmdrd.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "api-token"), []byte("eval"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixtures := filepath.Join(dir, "fixtures")
	calls := filepath.Join(dir, "calls")
	failing := filepath.Join(dir, "show-fails")
	script := filepath.Join(dir, "bd")
	t.Setenv("STALE_BOARD_FIXTURES", fixtures)
	t.Setenv("STALE_BOARD_CALLS", calls)
	t.Setenv("STALE_BOARD_SHOW_FAILS", failing)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$STALE_BOARD_CALLS"
case "$1" in
show)
  shift
  case " $* " in *" --json "*) form=json ;; *) form=txt ;; esac
  out=''
  for a in "$@"; do
    if [ "$a" = "$(cat "$STALE_BOARD_SHOW_FAILS" 2>/dev/null)" ]; then echo 'show failed' >&2; exit 1; fi
    if [ -f "$STALE_BOARD_FIXTURES/$a.$form" ]; then
      if [ "$form" = txt ]; then cat "$STALE_BOARD_FIXTURES/$a.txt"; exit 0; fi
      out="$out${out:+,}$(cat "$STALE_BOARD_FIXTURES/$a.json")"
    fi
  done
  echo "[$out]" ;;
list) echo '[]' ;;
create) echo 'create refused by the fixture' >&2; exit 1 ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")

	for _, tc := range sc.Cases {
		for verb, args := range map[string][]string{"state write": sc.StateArgs, "spawn": sc.SpawnArgs} {
			if tc.Only != "" && tc.Only != verb {
				continue
			}
			t.Run(tc.Name+"/"+verb, func(t *testing.T) {
				os.RemoveAll(fixtures)
				if err := os.MkdirAll(fixtures, 0o700); err != nil {
					t.Fatal(err)
				}
				db := maps.Clone(sc.DB)
				maps.Copy(db, tc.Edit)
				writeBeadsFixture(t, fixtures, db)
				if err := os.WriteFile(failing, []byte(tc.ShowFails), 0o600); err != nil {
					t.Fatal(err)
				}
				os.Remove(calls)
				ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				var stderr, stdout bytes.Buffer
				cmd.Stderr, cmd.Stdout = &stderr, &stdout
				runErr := cmd.Run()
				said := stdout.String() + stderr.String()
				refused := strings.Contains(said, "refused: it waits on demo-flow.1")
				if refused != tc.Refused {
					t.Fatalf("refused=%v, want %v (err %v):\n%s", refused, tc.Refused, runErr, said)
				}
				log, _ := os.ReadFile(calls)
				wrote := strings.Contains(string(log), "\nupdate ") || strings.Contains(string(log), "\nset-state ") || strings.Contains(string(log), "\ncreate ")
				if tc.Refused && (runErr == nil || wrote) {
					t.Fatalf("a refusal wrote or exited 0 (err %v):\n%s", runErr, log)
				}
				if !tc.Refused && !wrote {
					t.Fatalf("the command never got past the door (err %v):\n%s\n%s", runErr, said, log)
				}
			})
		}
	}
}
