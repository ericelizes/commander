package evals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// The built cmdr asked to respawn a running Operator while a disk it writes to
// is over the refuse line (COMMANDER_DISK_REFUSE_PCT stands in for a full disk)
// is refused naming the disk, before the old run is closed: the run row stays
// running. Under the line the disk check passes. Private home and store; the
// beads CLI is a stub that answers nothing; no agent or cmdrd runs, and the
// one tmux call past the check (ending the old session) reaches only the
// harness's private tmux dir.
func TestDiskFullSpawnScenario(t *testing.T) {
	var sc struct {
		Role, Session string
		Cases         []struct {
			Name        string
			RefusePct   string `json:"refuse_pct"`
			Exit        int
			Says, Never []string
		}
	}
	raw, err := os.ReadFile("scenarios/disk-full-spawn.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), binary, "./cmd/cmdr")
	for _, c := range sc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			home, project := filepath.Join(dir, "home"), filepath.Join(dir, "project")
			for _, d := range []string{home, project} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			st, err := store.Open(filepath.Join(home, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			pid, err := st.InsertProject(ctx, store.Project{Name: "sample", Type: "knowledge", Path: project, Status: "active", ActivatedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "sample-1", AgentName: sc.Role,
				TmuxPaneID: sql.NullString{String: sc.Session, Valid: true}, SpawnTime: 1}); err != nil {
				t.Fatal(err)
			}
			st.Close()
			bd := filepath.Join(dir, "bd")
			writeExecutable(t, bd, "#!/bin/sh\necho 'unavailable in this eval' >&2\nexit 1\n")

			cctx, cancel := context.WithTimeout(ctx, evalTimeout(t, 30*time.Second))
			defer cancel()
			cmd := exec.CommandContext(cctx, binary, "internal-operator-provider", sc.Role, "--account", "default")
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "COMMANDER_ROLE=") && !strings.HasPrefix(kv, "COMMANDER_MANAGER_ROLE=") && !strings.HasPrefix(kv, "COMMANDER_DISK_") {
					env = append(env, kv)
				}
			}
			cmd.Env = append(env, "HOME="+dir, "COMMANDER_HOME="+home, "COMMANDER_BEADS_BIN="+bd, "AM_HTTP_URL=http://127.0.0.1:1", "AM_HTTP_BEARER_TOKEN=eval-not-a-token",
				"COMMANDER_WRITE_DOOR=off", "COMMANDER_DISK_REFUSE_PCT="+c.RefusePct)
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			t.Logf("exit %d:\n%s", code, out)
			if c.Exit >= 0 && code != c.Exit {
				t.Fatalf("exit %d, want %d:\n%s", code, c.Exit, out)
			}
			for _, w := range c.Says {
				if !strings.Contains(string(out), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range c.Never {
				if strings.Contains(string(out), w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
			if c.Exit == 1 {
				st, err := store.Open(filepath.Join(home, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				run, err := st.GetAgentRunByAgentName(ctx, sc.Role)
				if err != nil || run == nil || run.Status != "running" {
					t.Fatalf("a refused respawn touched the run: %+v %v", run, err)
				}
			}
		})
	}
}
