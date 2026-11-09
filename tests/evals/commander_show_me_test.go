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
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// promptTableCommands returns the cmdr commands prompts/commander.md's table
// gives for a phrase, so this eval fails when the prompt teaches another route.
func promptTableCommands(t *testing.T, phrase string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(), "prompts", "commander.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		cells := strings.Split(line, " | ")
		if !strings.HasPrefix(line, "| ") || len(cells) < 2 || !strings.Contains(cells[0], phrase) {
			continue
		}
		var cmds []string
		for _, m := range regexp.MustCompile("`(cmdr [^`]+)`").FindAllStringSubmatch(cells[1], -1) {
			cmds = append(cmds, m[1])
		}
		return cmds
	}
	t.Fatalf("no table row for %s", phrase)
	return nil
}

// The real cmdr as Commander (role set, no terminal) running what its prompt says,
// against a scripted beads binary, a real store, fake cmdrd and am servers, and tmux
// on a private socket through the bare-cmdr rig's logging shim.
func TestCommanderShowMeScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/commander-show-me.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Project string                    `json:"project"`
		Bead    string                    `json:"bead"`
		Beads   map[string]staleBoardBead `json:"beads"`
		Phrases []string                  `json:"phrases"`
		Cases   []struct {
			Name       string   `json:"name"`
			Cmdrd      string   `json:"cmdrd"`
			Cockpit    bool     `json:"cockpit"`
			WantExit   int      `json:"want_exit"`
			WantNative bool     `json:"want_native"`
			WantNoTmux bool     `json:"want_no_tmux"`
			WantFocus  bool     `json:"want_focused"`
			WantStderr []string `json:"want_stderr"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	r := newBareCmdrRig(t)
	// A Manager window's chat pane has to stay up for the columns to be laid out beside it.
	writeExecutable(t, filepath.Join(r.bin, "claude"), "#!/bin/sh\nexec sleep 300\n")

	project := filepath.Join(r.dir, "project")
	beadsDir := filepath.Join(r.dir, "beads")
	for _, d := range []string{project, beadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBeadsFixture(t, beadsDir, sc.Beads)
	var rows []json.RawMessage
	for id := range sc.Beads {
		row, err := os.ReadFile(filepath.Join(beadsDir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	list, _ := json.Marshal(rows)
	if err := os.WriteFile(filepath.Join(beadsDir, "list"), list, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(r.bin, "bd")
	writeExecutable(t, script, `#!/bin/sh
json=0
for a in "$@"; do [ "$a" = --json ] && json=1; done
case "$1" in
show)
  shift
  out=''
  text=''
  for a in "$@"; do
    if [ -f "$SHOWME_BEADS/$a.json" ]; then
      out="$out${out:+,}$(cat "$SHOWME_BEADS/$a.json")"
      text="$text$(cat "$SHOWME_BEADS/$a.txt")"
    fi
  done
  if [ $json = 1 ]; then echo "[$out]"; else echo "$text"; fi ;;
list) cat "$SHOWME_BEADS/list" ;;
*) echo '[]' ;;
esac
`)

	const amKey = "/example/repo"
	home := ""
	for _, kv := range r.env {
		if v, ok := strings.CutPrefix(kv, "COMMANDER_HOME="); ok {
			home = v
		}
	}
	role := "manager-" + sc.Bead
	ctx0, cancel0 := context.WithTimeout(context.Background(), evalTimeout(t, 30*time.Second))
	defer cancel0()
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(ctx0, store.Project{Name: sc.Project, Type: "code", Path: project, Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAmAgent(ctx0, amKey, role, "EvalManager"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	env := append(append([]string{}, r.env...), "COMMANDER_ROLE=commander", "COMMANDER_BEADS_BIN="+script, "SHOWME_BEADS="+beadsDir,
		"COMMANDER_REPO="+amKey, "AM_HTTP_BEARER_TOKEN=fixture", "COMMANDER_RUNTIME=claude", "COMMANDER_STATE_DB=")

	run := func(t *testing.T, line string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
		defer cancel()
		args := strings.Fields(strings.TrimPrefix(line, "cmdr "))
		cmd := exec.CommandContext(ctx, filepath.Join(r.bin, "cmdr"), args...)
		cmd.Env, cmd.Dir, cmd.WaitDelay = env, project, 3*time.Second
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("%s: %v", line, err)
			}
			code = exit.ExitCode()
		}
		return code, out.String(), stderr.String()
	}

	for _, phrase := range sc.Phrases {
		cmds := promptTableCommands(t, phrase)
		if len(cmds) != 2 || cmds[0] != "cmdr internal-workstreams" {
			t.Fatalf("%s: the table gives %q, want internal-workstreams and then the open", phrase, cmds)
		}
		for _, tc := range sc.Cases {
			t.Run(phrase+"/"+tc.Name, func(t *testing.T) {
				r.killServers()
				var starts atomic.Int32
				cmdrd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.Method == http.MethodPost && req.URL.Path == "/api/chat/"+role+"/start" {
						starts.Add(1)
						if tc.Cmdrd == "held" {
							http.Error(w, "session held by a terminal", http.StatusConflict)
							return
						}
						json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": map[string]any{"role": role, "running": true}})
						return
					}
					http.NotFound(w, req)
				}))
				defer cmdrd.Close()
				apiURL := filepath.Join(home, "api-url")
				_ = os.Remove(apiURL)
				if tc.Cmdrd != "down" {
					if err := os.WriteFile(apiURL, []byte(cmdrd.URL), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(home, "api-token"), []byte("eval"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.Cockpit {
					start := exec.Command(r.tmux, "-S", r.sock, "new-session", "-d", "-x", "200", "-y", "50", "-s", "commander")
					start.Env = r.env
					if out, err := start.CombinedOutput(); err != nil {
						t.Fatalf("cockpit: %v\n%s", err, out)
					}
				}

				code, out, stderr := run(t, cmds[0])
				if code != 0 {
					t.Fatalf("%s: exit %d: %s", cmds[0], code, stderr)
				}
				var listed []struct {
					N       int    `json:"n"`
					Project string `json:"project"`
					BeadID  string `json:"bead_id"`
				}
				if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 1 || listed[0].BeadID != sc.Bead || listed[0].Project != sc.Project {
					t.Fatalf("%s: %v, got %s (stderr %s)", cmds[0], err, out, stderr)
				}

				open := strings.NewReplacer("<project>", listed[0].Project, " X ", " "+listed[0].Project+" ", "<bead-id>", listed[0].BeadID).Replace(cmds[1])
				_ = os.Remove(r.calls)
				code, out, stderr = run(t, open)
				calls, _ := os.ReadFile(r.calls)
				if code != tc.WantExit {
					t.Fatalf("%s: exit %d, want %d: %s", open, code, tc.WantExit, stderr)
				}
				if strings.Contains(string(calls), "new-session") {
					t.Fatalf("%s built a session:\n%s", open, calls)
				}
				for _, want := range tc.WantStderr {
					if !strings.Contains(stderr, want) {
						t.Errorf("stderr %q does not say %q", stderr, want)
					}
				}
				if tc.WantNoTmux && len(calls) != 0 {
					t.Errorf("tmux touched:\n%s", calls)
				}
				if tc.WantExit != 0 {
					if r.hasCockpit() {
						t.Errorf("a failed open built a cockpit")
					}
					return
				}
				var res struct {
					Role   string `json:"manager_role"`
					Pane   string `json:"pane"`
					Native bool   `json:"native"`
				}
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("answer %q: %v", out, err)
				}
				if res.Role != role || res.Native != tc.WantNative {
					t.Errorf("answer %+v, want role %s native=%v", res, role, tc.WantNative)
				}
				if tc.WantNative && starts.Load() != 1 {
					t.Errorf("cmdrd saw %d starts, want 1", starts.Load())
				}
				if tc.WantFocus {
					active, err := exec.Command(r.tmux, "-S", r.sock, "list-panes", "-t", "=commander:", "-F", "#{pane_id}").Output()
					if err != nil {
						t.Fatal(err)
					}
					if res.Pane == "" || !strings.Contains(string(active), res.Pane) {
						t.Errorf("the selected window has panes %q, not the Manager's %q: --focus lost", active, res.Pane)
					}
				}
			})
		}
	}
}
