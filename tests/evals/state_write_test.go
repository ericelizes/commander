package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// Drive the same authenticated command boundary as the app with a real cmdr
// process and a br recorder, without touching live projects or agents.
func TestStateWriteScenario(t *testing.T) {
	files, err := filepath.Glob("scenarios/state-writes/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no state-write scenarios: %v", err)
	}
	for _, f := range files {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".json"), func(t *testing.T) { runStateWriteScenario(t, f) })
	}
}

func runStateWriteScenario(t *testing.T, file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		Argv     []string `json:"argv"`
		Labels   []string `json:"bead_labels"` // the bead's labels as br shows them; default kind:objective
		Contains []string `json:"update_contains"`
		Excludes []string `json:"update_excludes"`
		Updates  int      `json:"expected_updates"`
		Comments []string `json:"comment_contains"` // each in the one comment the command writes
		Refused  string   `json:"refused_contains"` // the command fails saying this and writes nothing
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Labels) == 0 {
		sc.Labels = []string{"kind:objective"}
	}
	labels, _ := json.Marshal(sc.Labels)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pid, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The door refuses in-progress owned by an Operator that is not running.
	_, err = st.InsertAgentRun(context.Background(), store.AgentRun{ProjectID: pid, TaskBeadID: "p-1", AgentName: "operator-x", SpawnTime: 2})
	if err != nil {
		t.Fatal(err)
	}
	writerID, err := st.InsertAgentRun(context.Background(), store.AgentRun{ProjectID: pid, TaskBeadID: "p-1", AgentName: "operator-writer", OpencodeSessionID: "writer-session", SpawnTime: 1, Status: "exited", TokensUsed: 42})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.GetAgentRun(context.Background(), writerID)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "br")
	log := filepath.Join(dir, "calls")
	t.Setenv("STATE_WRITE_CALLS", log)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$STATE_WRITE_CALLS"
case "$1" in
list)
 case "$*" in *kind:objective*) case '`+strings.Join(sc.Labels, ", ")+`' in *kind:objective*) ;; *) echo '[]'; exit 0 ;; esac ;; esac
 echo '[{"id":"p-1","status":"open","labels":`+string(labels)+`}]' ;;
show)
  if [ "$3" = "--json" ]; then echo '[{"id":"p-1","status":"open","labels":`+string(labels)+`}]'
  else printf '○ p-1 · step   [OPEN]\nLabels: %s\n' '`+strings.Join(sc.Labels, ", ")+`'; fi ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	server := httptest.NewServer(api.New(api.Options{Token: "eval", Run: api.ExecRunner(binary), Source: func(context.Context) livedata.Snapshot { return livedata.Snapshot{} }}).Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]any{"argv": sc.Argv})
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 20*time.Second))
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/cmd", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer eval")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result api.CmdResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if sc.Refused == "" && !result.OK {
		t.Fatalf("command failed: %+v", result)
	}
	if sc.Refused != "" && (result.OK || !strings.Contains(result.Stderr, sc.Refused)) {
		t.Fatalf("want refused with %q: %+v", sc.Refused, result)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	updates, comments := 0, 0
	for _, line := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(line, "comments add ") {
			comments++
			for _, want := range sc.Comments {
				if !strings.Contains(line, want) {
					t.Errorf("comment has no %s: %s", want, line)
				}
			}
		}
		if strings.HasPrefix(line, "update ") {
			updates++
			for _, unwanted := range sc.Excludes {
				if strings.Contains(line, unwanted) {
					t.Errorf("unexpected %s: %s", unwanted, line)
				}
			}
			for _, want := range sc.Contains {
				if !strings.Contains(line, want) {
					t.Errorf("missing %s: %s", want, line)
				}
			}
		}
	}
	after, err := st.GetAgentRun(context.Background(), writerID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("writer run changed: before=%+v after=%+v", before, after)
	}
	if updates != sc.Updates {
		t.Fatalf("updates=%d want %d: %s", updates, sc.Updates, calls)
	}
	if want := min(len(sc.Comments), 1); comments != want {
		t.Fatalf("comments=%d want %d: %s", comments, want, calls)
	}
}
