package evals

import (
	"context"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/example-user/commander/internal/execution"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type runtimeMacSpawner func(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error)

func (f runtimeMacSpawner) Spawn(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	return f(ctx, r)
}

// Exercise the real CLI and worker with a Mac-capable adapter. Runtime preference
// orders accounts, leaving the alternate provider available when Claude is spent.
func TestAStepRuntimeMacCapabilityScenario(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home, dir := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDER_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	pid, err := st.InsertProject(ctx, store.Project{Name: "runtime-mac", Path: dir, Type: "knowledge", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,'step','Mac runtime','Bounded work','Recorded result','["kind:objective"]','now','now')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetExecutionReady(ctx, pid, "step", "commander", 1, 1, true, []string{"mac"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "cmdr")
	mustBuild(t, os.Environ(), bin, "./cmd/cmdr")
	server := api.New(api.Options{Token: "synthetic-token", StateStore: st, Beads: beads.New()})
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	for name, value := range map[string]string{"api-url": ts.URL, "api-token": "synthetic-token"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command(bin, "internal-set-objective", "--project", "runtime-mac", "--bead", "step", "--runtime", "claude")
	c.Dir = dir
	c.Env = append(os.Environ(), "COMMANDER_HOME="+home, "HOME="+home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if _, err = st.SetOrchestrationControl(ctx, 1, "active", "runtime Mac fixture"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAgentAccounts(ctx, store.AgentKindAgent, []string{"codex", "claude"}); err != nil {
		t.Fatal(err)
	}
	if err = st.ConfigureExecutionSlot(ctx, "mac-adapter", []string{"knowledge", "mac"}, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	w := execution.Worker{Store: st, Capacity: 1, Spawner: runtimeMacSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		calls++
		if !reflect.DeepEqual(r.AgentAccounts, []string{"claude", "codex"}) {
			t.Fatalf("preference or fallback lost: %v", r.AgentAccounts)
		}
		if r.Runtime != "" {
			t.Fatalf("preference became a pin: %q", r.Runtime)
		}
		id, e := st.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: r.ExecutionClaimToken, ProjectID: r.ProjectID, TaskBeadID: r.TaskBeadID, AgentName: "operator-" + r.Name, OpencodeSessionID: r.Name, SpawnTime: time.Now().Unix()})
		return &spawn.SpawnResult{RunID: id}, e
	})}
	if err = w.PassSlot(ctx, "mac-adapter"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("launches: %d", calls)
	}
}
