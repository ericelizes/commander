package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// The real cmdr internal-place against a real store, a scripted beads binary whose
// parent edge moves on the write, and fake cmdrd and am servers.
func TestMovedBackManagerScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/moved-back-manager.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Project string                    `json:"project"`
		Args    []string                  `json:"args"`
		Before  map[string]staleBoardBead `json:"before"`
		After   map[string]staleBoardBead `json:"after"`
		OldRole string                    `json:"old_role"`
		NewRole string                    `json:"new_role"`
		Cases   []struct {
			Name string          `json:"name"`
			Chat json.RawMessage `json:"chat"`
			Told []string        `json:"told"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	script := filepath.Join(dir, "bd")
	writeExecutable(t, script, `#!/bin/sh
case "$1" in
show)
  shift
  out=''
  for a in "$@"; do
    if [ -f "$MOVED_NOW/$a.json" ]; then out="$out${out:+,}$(cat "$MOVED_NOW/$a.json")"; fi
  done
  echo "[$out]" ;;
update)
  case " $* " in *" --parent "*) cp "$MOVED_AFTER/$2.json" "$MOVED_NOW/$2.json" ;; esac ;;
list|dep) echo '[]' ;;
esac
`)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	const key = "/example/repo"
	t.Setenv("COMMANDER_REPO", key)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fixture")
	names := map[string]string{"commander": "EvalSender", sc.OldRole: "OldOwner", sc.NewRole: "NewOwner"}

	for _, tc := range sc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
			defer cancel()
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			now, after := filepath.Join(home, "now"), filepath.Join(home, "after")
			for path, db := range map[string]map[string]staleBoardBead{now: sc.Before, after: sc.After} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				writeBeadsFixture(t, path, db)
			}
			t.Setenv("MOVED_NOW", now)
			t.Setenv("MOVED_AFTER", after)

			st, err := store.Open(filepath.Join(home, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			id, err := st.InsertProject(ctx, store.Project{Name: sc.Project, Type: "code", Path: home, Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: id, WorkstreamBeadID: "demo-flow.2", AmRole: sc.OldRole, SessionFile: filepath.Join(home, "session")}); err != nil {
				t.Fatal(err)
			}
			for role, name := range names {
				if err := st.UpsertAmAgent(ctx, key, role, name); err != nil {
					t.Fatal(err)
				}
			}

			cmdrd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/chat/"+sc.OldRole {
					http.NotFound(w, r)
					return
				}
				status := map[string]any{}
				if err := json.Unmarshal(tc.Chat, &status); err != nil {
					t.Error(err)
				}
				// As Engine.foreignOwner: a terminal is found through the row's session file.
				if row, err := st.GetWorkstreamManagerByRole(r.Context(), sc.OldRole); err != nil || row == nil {
					delete(status, "owned_by")
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": status})
			}))
			defer cmdrd.Close()
			if err := os.WriteFile(filepath.Join(home, "api-url"), []byte(cmdrd.URL), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "api-token"), []byte("eval"), 0o600); err != nil {
				t.Fatal(err)
			}

			var mu sync.Mutex
			var told []string
			am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Params struct {
						Name      string
						Arguments struct {
							To []string `json:"to"`
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Params.Name == "send_message" {
					mu.Lock()
					told = append(told, req.Params.Arguments.To...)
					mu.Unlock()
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "{}"}}}})
			}))
			defer am.Close()
			t.Setenv("AM_HTTP_URL", am.URL)

			cmd := exec.CommandContext(ctx, binary, sc.Args...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("internal-place: %v\n%s", err, out.String())
			}
			var result struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Warnings) != 0 {
				t.Fatalf("result %q: %v", out.String(), err)
			}
			want := make([]string, 0, len(tc.Told))
			for _, role := range tc.Told {
				want = append(want, names[role])
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(told, want) {
				t.Fatalf("mail to %v, want %v", told, want)
			}
			if row, err := st.GetWorkstreamManagerByRole(ctx, sc.OldRole); err != nil || row != nil {
				t.Fatalf("still registered: %+v, %v", row, err)
			}
		})
	}
}
