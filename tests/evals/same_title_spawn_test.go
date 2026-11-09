package evals

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// The real cmdr against a scripted beads binary: a spawn's create is killed
// after a same-title task of another spawn appeared under the step, seconds old
// and with no owner yet. The spawn takes nothing, deletes nothing and says so.
func TestSameTitleSpawnScenario(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_STATE_DB", "")
	t.Setenv("AM_HTTP_URL", "http://127.0.0.1:1")
	t.Setenv("AM_HTTP_BEARER_TOKEN", "eval")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "example", Type: "code", Path: dir, Status: "active", ActivatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "bd")
	log := filepath.Join(dir, "calls")
	t.Setenv("STATE_WRITE_CALLS", log)
	t.Setenv("COMMANDER_BEADS_BIN", script)
	sibling := `{"id":"p-1.1","title":"same title","status":"open","labels":["kind:task","spawn-attempt:0000000000000000"],"created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	writeExecutable(t, script, `#!/bin/sh
printf '%s\n' "$*" >> "$STATE_WRITE_CALLS"
case "$1 $2" in
"create --help") exit 0 ;;
create*) kill -9 $$ ;;
list*) echo '[`+sibling+`]' ;;
show*) echo '[{"id":"p-1","title":"step","status":"open","labels":["kind:objective"]}]' ;;
esac
`)
	binary := filepath.Join(dir, "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout(t, 60*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "internal-spawn-operator", "--project", "example", "--step", "p-1", "--title", "same title", "--writes-workflow")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("the spawn reported success: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "not repeated") {
		t.Fatalf("stderr: %s", stderr.String())
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	creates := 0
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "create ") && !strings.Contains(line, "--help") {
			creates++
			if !strings.Contains(line, "spawn-attempt:") {
				t.Errorf("create carries no marker: %s", line)
			}
		}
		if strings.HasPrefix(line, "delete ") || strings.HasPrefix(line, "update p-1.1") {
			t.Errorf("touched the other spawn's task: %s", line)
		}
	}
	if creates != 1 {
		t.Fatalf("creates=%d:\n%s", creates, calls)
	}
}
