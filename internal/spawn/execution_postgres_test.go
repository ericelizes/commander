package spawn_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/execution"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// Only provider choice is pinned by the test. The real scheduler still owns
// readiness, atomic reservation, request construction and failure handling.
type runtimeContractSpawner struct {
	inner   *spawn.Spawner
	runtime string
	request spawn.SpawnRequest
	result  *spawn.SpawnResult
}

func (s *runtimeContractSpawner) Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	s.request = req
	req.Runtime = s.runtime
	result, e := s.inner.Spawn(ctx, req)
	s.result = result
	return result, e
}

// Launch preparation uses the real claim/HTTP/store path, real worktree and
// installed prompt. Tmux is detached and mocked: no model process is started.
func TestClaimedExecutionLaunchCarriesCanonicalContractAndIdentity(t *testing.T) {
	var scenario struct {
		Kinds, Runtimes []string
		Commander       string
	}
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-executor-runtime.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil {
		t.Fatal(e)
	}
	if len(scenario.Kinds) != 2 || len(scenario.Runtimes) != 2 || scenario.Commander == "" {
		t.Fatal("executor runtime scenario missing")
	}
	for _, kind := range scenario.Kinds { // "" leaves the choice to the saved account order (none saved: the
		// default, Codex first, so Claude usage stays free for Commander).
		for _, asked := range append(append([]string{}, scenario.Runtimes...), "") {
			name := asked
			if name == "" {
				name = "share-default"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				runtime := asked
				if asked == "" {
					runtime = chat.RuntimeCodex
					codexHome := t.TempDir()
					t.Setenv("CODEX_HOME", codexHome)
					if e := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":{"access_token":"test"}}`), 0o600); e != nil {
						t.Fatal(e)
					}
				}

				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("COMMANDER_HOME", home)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				stub := filepath.Join(home, "codex-fixture")
				if e := os.WriteFile(stub, []byte(`#!/bin/sh
if [ "$1" = features ] && [ "$2" = list ]; then echo 'hooks stable true'; exit 0; fi
exit 99
`), 0700); e != nil {
					t.Fatal(e)
				}
				t.Setenv("COMMANDER_CODEX_BIN", stub)
				dir := t.TempDir()
				for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Fixture"}, {"commit", "--allow-empty", "-m", "fixture"}} {
					command := exec.Command("git", args...)
					command.Dir = dir
					if out, e := command.CombinedOutput(); e != nil {
						t.Fatalf("git fixture: %v %s", e, out)
					}
				}
				st, dsn := pgtest.Open(t)
				ctx := context.Background()
				dk := &docker.MockClient{}
				tx := &tmuxctl.MockClient{}
				if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
					t.Fatal(e)
				}
				for _, key := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION"} {
					t.Setenv(key, "")
				}
				if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
					t.Fatal(e)
				}
				pid, e := st.InsertProject(ctx, store.Project{Name: "runtime-" + kind, Path: dir, Type: kind, Status: "active"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,'task','Bounded task','Implement within this scope','Record evidence','["kind:objective"]','now','now')`, pid); e != nil {
					t.Fatal(e)
				}
				if e = st.RegisterCommander(ctx, scenario.Commander, "Urgent"); e != nil {
					t.Fatal(e)
				}
				if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "task", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: scenario.Commander, Reason: "urgent executor launch"}); e != nil {
					t.Fatal(e)
				}
				if _, e = st.SetOrchestrationControl(ctx, 1, "active", "executor launcher test"); e != nil {
					t.Fatal(e)
				}
				if _, e = st.SetExecutionReady(ctx, pid, "task", scenario.Commander, 1, 2, true, nil); e != nil {
					t.Fatal(e)
				}
				if e = st.ConfigureExecutionSlot(ctx, "vm-test", []string{kind}, true); e != nil {
					t.Fatal(e)
				}
				server := httptest.NewServer(api.New(api.Options{Token: "executor-test", StateStore: st}).Handler())
				defer server.Close()
				for key, value := range map[string]string{"api-url": server.URL, "api-token": "executor-test"} {
					if e = os.WriteFile(filepath.Join(home, key), []byte(value), 0600); e != nil {
						t.Fatal(e)
					}
				}
				native := &recordingNativeRuntime{store: st, home: home}
				sp := spawn.New(st, &opencode.MockClient{}, beads.New(), &agentmail.MockClient{}, dk, tx, t.TempDir()).WithNativeExecution(native)
				adapter := &runtimeContractSpawner{inner: sp, runtime: asked}
				worker := execution.Worker{Store: st, Spawner: adapter, Capacity: 1}
				if e = worker.PassSlot(ctx, "vm-test"); e != nil {
					t.Fatal(e)
				}
				result := adapter.result
				if result == nil {
					t.Fatal("ready bead did not reach its runtime adapter")
				}
				if adapter.request.Model != "" || adapter.request.ExecutionClaimToken == "" {
					t.Fatal("fixture did not exercise scheduler-owned claim and model selection")
				}
				if result.RunID <= 0 || result.Runtime != runtime || len(tx.Calls) != 0 || len(dk.Calls) != 0 {
					t.Fatalf("wrong execution adapter: %+v tmux=%v docker=%v", result, tx.Calls, dk.Calls)
				}
				prompt, e := os.ReadFile(agentrt.ClaudePromptPath(result.AgentName))
				if e != nil {
					t.Fatal(e)
				}
				for _, want := range []string{"You execute one claimed bead", "cmdr question ask", "--plan-revision", "Final report:"} {
					if !strings.Contains(string(prompt), want) {
						t.Fatalf("installed prompt lost %q", want)
					}
				}
				for _, bad := range []string{"internal-create-objective", "internal-close-objective", "never report to `commander`"} {
					if strings.Contains(string(prompt), bad) {
						t.Fatalf("installed prompt grants obsolete authority %q", bad)
					}
				}
				if native.spec.ExecutionRunID != result.RunID || native.spec.SessionID != result.SessionID || native.spec.Runtime != runtime || native.spec.ExecutionInitialPrompt == "" {
					t.Fatal("native execution lost its canonical identity", native.spec)
				}
				var driver, recordedRuntime, recordedAccount, model string
				if e = st.DB().QueryRow(`SELECT execution_driver,execution_runtime,execution_account,execution_model FROM agent_runs WHERE id=?`, result.RunID).Scan(&driver, &recordedRuntime, &recordedAccount, &model); e != nil || driver != "native" || recordedRuntime != runtime || model == "" || (runtime == chat.RuntimeCodex && model != chat.CodexModel(model)) || (runtime == chat.RuntimeCodex && strings.Contains(model, "claude")) {
					t.Fatal("provider selection was not bound with the run", driver, recordedRuntime, recordedAccount, model, e)
				}
				if _, e = os.Stat(filepath.Join(dir, ".commander", "operators", result.AgentName+".sh")); !os.IsNotExist(e) {
					t.Fatal("claimed execution still uses a separate shell launcher", e)
				}
				var slotRun, activeRun int64
				if e = st.DB().QueryRowContext(ctx, `SELECT x.run_id,b.active_run_id FROM execution_slots x JOIN bead_tasks b ON b.project_id=x.project_id AND b.id=x.bead_id WHERE x.agent_id='vm-test'`).Scan(&slotRun, &activeRun); e != nil || slotRun != result.RunID || activeRun != result.RunID {
					t.Fatal("launcher was not bound to its durable claim", slotRun, activeRun, e)
				}
				// A handoff while the execution exists changes question routing, preserving
				// the exact active run and its native wrapper session.
				j, e := st.Jurisdiction(ctx, pid, "task")
				if e != nil {
					t.Fatal(e)
				}
				if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "task", ExpectedOwner: scenario.Commander, ExpectedRevision: j.Revision, Inherit: true, Reason: "return urgent scope while execution continues"}); e != nil {
					t.Fatal(e)
				}
				b, e := beads.New().Show(ctx, dir, "task")
				if e != nil {
					t.Fatal(e)
				}
				id, e := st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "task", PlanRevision: b.PlanRevision, Actor: result.AgentName, RunID: result.RunID, Session: result.SessionID, ClientKey: "implementation-question", Question: "Does this require a scope extension?", Recommendation: "Keep the bounded implementation", Checkpoint: "Current branch and evidence retained; await the scope answer", CheckpointRuntime: runtime, Blocking: true})
				if e != nil {
					t.Fatal(e)
				}
				inbox, e := st.QuestionInbox(ctx, "commander")
				if e != nil || len(inbox) != 1 || inbox[0].ID != id {
					t.Fatalf("question did not follow the current owner: %+v %v", inbox, e)
				}
				prior, e := st.QuestionInbox(ctx, scenario.Commander)
				if e != nil || len(prior) != 0 {
					t.Fatal("former Commander retained the question", e)
				}
			})
		}
	}
}

func TestPostgresAdmissionRefusesBeforeRuntimeOrWorkspacePreparation(t *testing.T) {
	for _, mode := range []string{"active", "off", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("COMMANDER_HOME", home)
			st, _ := pgtest.Open(t)
			ctx := context.Background()
			if _, e := st.SetOrchestrationControl(ctx, 1, mode, "early launch refusal fixture"); e != nil {
				t.Fatal(e)
			}
			dir := t.TempDir()
			dk := &docker.MockClient{}
			tx := &tmuxctl.MockClient{}
			oc := &opencode.MockClient{}
			am := &agentmail.MockClient{}
			sp := spawn.New(st, oc, beads.New(), am, dk, tx, t.TempDir())
			token := ""
			if mode == "shadow" {
				token = "not-active"
			}
			_, e := sp.Spawn(ctx, spawn.SpawnRequest{ProjectID: 1, ProjectName: "fixture", ProjectPath: dir, ProjectType: "code", TaskBeadID: "task", Branch: "work/unreserved", ExecutionClaimToken: token})
			if e == nil || !strings.Contains(e.Error(), "spawn admission:") {
				t.Fatal("launch did not fail at admission", e)
			}
			if len(dk.Calls) > 0 || len(tx.Calls) > 0 {
				t.Fatal("unadmitted work reached a runtime", dk.Calls, tx.Calls)
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) > 0 {
				t.Fatal("unadmitted launch prepared the project workspace", entries, e)
			}
			entries, e = os.ReadDir(home)
			if e != nil || len(entries) > 0 {
				t.Fatal("unadmitted launch wrote chat/provider state", entries, e)
			}
		})
	}
}

type recordingNativeRuntime struct {
	store *store.Store
	home  string
	spec  chat.Spec
}

func (n *recordingNativeRuntime) Start(ctx context.Context, role string, _ chat.StartOptions) (chat.Status, error) {
	spec, e := (chat.StoreResolver{Store: n.store, Home: n.home}).Resolve(ctx, role)
	n.spec = spec
	return chat.Status{Role: role, SessionID: spec.SessionID, Runtime: spec.Runtime}, e
}
