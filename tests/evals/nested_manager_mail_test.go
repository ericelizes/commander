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

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type nestedMail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
}

// The real cmdr internal-mail, as a running Operator under a nested workstream,
// against a real store, a scripted bd holding the tree and a fake am server: its
// mail lands at the nested Manager.
func TestNestedManagerMailScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/nested-manager-mail.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Project  string                    `json:"project"`
		Operator string                    `json:"operator"`
		Task     string                    `json:"task"`
		Beads    map[string]staleBoardBead `json:"beads"`
		Names    map[string]string         `json:"names"`
		Sends    []struct {
			Name string       `json:"name"`
			To   string       `json:"to"`
			Want []nestedMail `json:"want"`
		} `json:"sends"`
		SwitchOff struct {
			To   string       `json:"to"`
			Want []nestedMail `json:"want"`
		} `json:"switch_off"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	script := filepath.Join(dir, "bd")
	// show prints the named beads; list --id a,b prints those beads.
	writeExecutable(t, script, `#!/bin/sh
cmd="$1"; shift
ids=''
case "$cmd" in
show) ids="$*" ;;
list)
  while [ $# -gt 0 ]; do
    case "$1" in --id) ids=$(echo "$2" | tr ',' ' '); shift ;; --id=*) ids=$(echo "${1#--id=}" | tr ',' ' ') ;; esac
    shift
  done ;;
*) echo '[]'; exit 0 ;;
esac
out=''
for a in $ids; do
  if [ -f "$NESTED_BEADS/$a.json" ]; then out="$out${out:+,}$(cat "$NESTED_BEADS/$a.json")"; fi
done
echo "[$out]"
`)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	const key = "/example/repo"
	t.Setenv("COMMANDER_REPO", key)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("COMMANDER_WRITE_DOOR", "off")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "fixture")

	run := func(t *testing.T, on bool, sends func(send func(to string) []nestedMail)) {
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 90*time.Second))
		defer cancel()
		home := t.TempDir()
		t.Setenv("COMMANDER_HOME", home)
		t.Setenv("COMMANDER_ROLE", sc.Operator)
		project := filepath.Join(home, "project")
		fixture := filepath.Join(home, "beads")
		for _, d := range []string{project, fixture} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeBeadsFixture(t, fixture, sc.Beads)
		t.Setenv("NESTED_BEADS", fixture)
		if err := beads.SetNestedManagers(project, on); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(home, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		pid, err := st.InsertProject(ctx, store.Project{Name: sc.Project, Type: "code", Path: project, Status: "active", ActivatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: sc.Task, AgentName: sc.Operator, SpawnTime: time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}
		for role, name := range sc.Names {
			if err := st.UpsertAmAgent(ctx, key, role, name); err != nil {
				t.Fatal(err)
			}
		}
		var mu sync.Mutex
		var got []nestedMail
		am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Params struct {
					Name      string
					Arguments struct {
						To      []string `json:"to"`
						Subject string   `json:"subject"`
					}
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Params.Name == "send_message" {
				mu.Lock()
				for _, to := range req.Params.Arguments.To {
					got = append(got, nestedMail{To: to, Subject: req.Params.Arguments.Subject})
				}
				mu.Unlock()
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "{}"}}}})
		}))
		defer am.Close()
		t.Setenv("AM_HTTP_URL", am.URL)
		sends(func(to string) []nestedMail {
			mu.Lock()
			got = nil
			mu.Unlock()
			cmd := exec.CommandContext(ctx, binary, "internal-mail", "--from", sc.Operator, "--to", to, "--subject", "done", "--body", "report")
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("internal-mail: %v\n%s", err, out.String())
			}
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(got)
		})
	}

	t.Run("switched on", func(t *testing.T) {
		run(t, true, func(send func(string) []nestedMail) {
			for _, s := range sc.Sends {
				if got := send(s.To); !sameMail(got, s.Want) {
					t.Fatalf("%s: mail %+v, want %+v", s.Name, got, s.Want)
				}
			}
		})
	})
	t.Run("switched off", func(t *testing.T) {
		run(t, false, func(send func(string) []nestedMail) {
			if got := send(sc.SwitchOff.To); !sameMail(got, sc.SwitchOff.Want) {
				t.Fatalf("switch off: mail %+v, want %+v", got, sc.SwitchOff.Want)
			}
		})
	})
}

func sameMail(got, want []nestedMail) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}
