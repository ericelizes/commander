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
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/chat/chattest"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/execution"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/testguard"
	"github.com/example-user/commander/internal/tmuxctl"
)

func TestExhaustedCapacityScenario(t *testing.T) {
	var scenario struct{ Kinds []string }
	raw, e := os.ReadFile("../../tests/evals/scenarios/exhausted-capacity.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || len(scenario.Kinds) != 2 {
		t.Fatal(e)
	}
	for _, kind := range scenario.Kinds {
		for _, target := range []string{"backup", "codex", "codex-resume-auto", "codex-resume-pin"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				checkpoint := strings.Contains(target, "resume")
				pinned := strings.HasSuffix(target, "-pin")
				if checkpoint {
					target = "codex"
				}
				ctx := context.Background()
				home := t.TempDir()
				testguard.NoLeakedProcesses(t, home)
				fakeDir := t.TempDir()
				for key, value := range map[string]string{"HOME": home, "COMMANDER_HOME": home, "COMMANDER_WORKSPACE_ROOT": t.TempDir(), "XDG_CONFIG_HOME": t.TempDir(), "CODEX_HOME": t.TempDir(), chattest.EnvVar: "1", chattest.DirVar: fakeDir, chattest.ProjectsVar: filepath.Join(home, ".claude", "projects"), "COMMANDER_ROLE": "", "COMMANDER_RUN_ID": "", "COMMANDER_RUN_SESSION": "", "COMMANDER_MANAGER_ROLE": ""} {
					t.Setenv(key, value)
				}
				write := func(p, value string) {
					t.Helper()
					if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(p, []byte(value), 0600); e != nil {
						t.Fatal(e)
					}
				}
				write(filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"private-default-token"}}`)
				if target == "backup" {
					write(filepath.Join(home, "claude-accounts", "backup", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"private-backup-token"}}`)
					t.Setenv("COMMANDER_CODEX_BIN", filepath.Join(home, "no-codex"))
				} else {
					write(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), `{"tokens":{"access_token":"private-codex-token"}}`)
					t.Setenv("COMMANDER_CODEX_BIN", os.Args[0])
					t.Setenv("CMDR_FAKE_CODEX", "1")
				}
				dir := t.TempDir()
				if kind == "code" {
					if e = os.Mkdir(filepath.Join(dir, ".worktrees.noindex"), 0700); e != nil {
						t.Fatal(e)
					}
					if e = os.Symlink(".worktrees.noindex", filepath.Join(dir, ".worktrees")); e != nil {
						t.Fatal(e)
					}
				}
				for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Fixture"}, {"commit", "--allow-empty", "-m", "fixture"}} {
					cmd := exec.Command("git", args...)
					cmd.Dir = dir
					if out, e := cmd.CombinedOutput(); e != nil {
						t.Fatalf("git: %v %s", e, out)
					}
				}
				st, dsn := pgtest.Open(t)
				write(filepath.Join(home, "state.postgres"), dsn)
				os.Mkdir(filepath.Join(dir, ".beads"), 0700)
				pid, e := st.InsertProject(ctx, store.Project{Name: "usage-recovery", Path: dir, Type: kind, Status: "active"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'goal','Usage workstream','["kind:objective","kind:workstream"]','now','now')`, pid); e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,description,acceptance_criteria,labels,created_at,updated_at) VALUES(?,'usagelimit','goal','Bounded work','Preserve prepared work','Record evidence','["kind:objective"]','now','now')`, pid); e != nil {
					t.Fatal(e)
				}
				if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private usage scenario"); e != nil {
					t.Fatal(e)
				}
				if _, e = st.SetExecutionReady(ctx, pid, "usagelimit", "commander", 1, 1, true, nil); e != nil {
					t.Fatal(e)
				}
				if e = st.SetAgentAccounts(ctx, store.AgentKindAgent, []string{"default"}); e != nil {
					t.Fatal(e)
				}
				if e = st.ConfigureExecutionSlot(ctx, "usage-vm", []string{kind}, true); e != nil {
					t.Fatal(e)
				}
				server := httptest.NewServer(api.New(api.Options{Token: "private-usage", StateStore: st}).Handler())
				defer server.Close()
				write(filepath.Join(home, "api-url"), server.URL)
				write(filepath.Join(home, "api-token"), "private-usage")
				engine := chat.New(chat.Options{Home: home, Binary: os.Args[0], HostBinary: os.Args[0], States: st, Resolver: chat.StoreResolver{Store: st, Home: home}, ProjectsDir: filepath.Join(home, ".claude", "projects"), Procs: func(context.Context, string) ([]chat.Proc, error) { return nil, nil }, StopGrace: time.Second})
				t.Cleanup(func() {
					for _, s := range engine.List() {
						if s.Running {
							engine.Stop(ctx, s.Role)
						}
					}
					engine.Close()
				})
				inner := spawn.New(st, &opencode.MockClient{}, beads.New(), &agentmail.MockClient{}, &docker.MockClient{}, &tmuxctl.MockClient{}, t.TempDir()).WithNativeExecution(engine)
				adapter := &usageScenarioSpawner{inner: inner}
				worker := execution.Worker{Store: st, Spawner: adapter, Capacity: 1}
				if checkpoint {
					write(filepath.Join(fakeDir, "checkpoint-usage"), "question before usage")
				}
				if e = worker.PassSlot(ctx, "usage-vm"); e != nil {
					t.Fatal(e)
				}
				first := adapter.result
				if first == nil {
					t.Fatal("missing first execution")
				}
				if checkpoint {
					awaitNative(t, "question prompt", func() bool { s, _ := engine.Status(first.AgentName); return len(s.Pending) > 0 })
					if pinned {
						if e = chat.WriteRuntime(home, first.AgentName, chat.RuntimeClaude); e != nil {
							t.Fatal(e)
						}
					}
					var revision int64
					if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='usagelimit'`, pid).Scan(&revision); e != nil {
						t.Fatal(e)
					}
					q, err := st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "usagelimit", PlanRevision: revision, Actor: first.AgentName, RunID: first.RunID, Session: first.SessionID, ClientKey: "usage-checkpoint", Question: "Continue?", Checkpoint: "Prepared work", CheckpointRuntime: chat.RuntimeClaude, Blocking: true})
					if err != nil {
						t.Fatal(err)
					}
					if _, e = st.AnswerQuestion(ctx, q, "commander", "Continue", true); e != nil {
						t.Fatal(e)
					}
					status, _ := engine.Status(first.AgentName)
					for _, pending := range status.Pending {
						if e = engine.Approve(ctx, first.AgentName, pending.ID, chat.Decision{Behavior: "allow"}); e != nil {
							t.Fatal(e)
						}
					}
					awaitNative(t, "question physical exit", func() bool { r, e := st.GetAgentRun(ctx, first.RunID); return e == nil && r.Status == "exited" })
					if e = worker.PassSlot(ctx, "usage-vm"); e != nil {
						t.Fatal(e)
					}
					resumed := adapter.result
					if resumed.RunID == first.RunID || resumed.SessionID != first.SessionID {
						t.Fatalf("checkpoint not resumed: %+v", resumed)
					}
					first = resumed
				}
				awaitNative(t, "usage-limited physical exit", func() bool { r, e := st.GetAgentRun(ctx, first.RunID); return e == nil && r.Status == "native-error" })
				if checkpoint {
					if got := chat.ReadRuntimePin(home, first.AgentName); pinned && got != chat.RuntimeClaude {
						t.Fatalf("continuation changed runtime intent: pin=%q", got)
					}
					if pinned {
						if e = worker.PassSlot(ctx, "usage-vm"); e == nil {
							t.Fatal("real Claude pin escaped to Codex")
						}
						var runs int
						if e = st.DB().QueryRow(`SELECT count(*) FROM agent_runs WHERE project_id=?`, pid).Scan(&runs); e != nil || runs != 2 {
							t.Fatalf("pinned continuation launched another run: %d %v", runs, e)
						}
						return
					}
					// Only Codex has capacity now; recover directly without changing intent.
					if e = worker.PassSlot(ctx, "usage-vm"); e != nil {
						t.Fatal(e)
					}
					next := adapter.result
					if next == nil || next.RunID == first.RunID || next.SessionID == first.SessionID || next.AgentName != first.AgentName || next.Runtime != chat.RuntimeCodex {
						t.Fatalf("automatic continuation did not recover to Codex: first=%+v next=%+v", first, next)
					}
					if got := chat.ReadRuntimePin(home, first.AgentName); got != "" {
						t.Fatalf("automatic continuation created pin %q", got)
					}
					awaitNative(t, "checkpoint recovery completed", func() bool { r, e := st.GetAgentRun(ctx, next.RunID); return e == nil && r.Status == "exited" })
					return
				}
				workspace := dir
				if kind == "code" {
					workspace = first.WorktreePath
				}
				write(filepath.Join(workspace, "prepared.txt"), "uncommitted prepared artifact")
				key := chat.RuntimeClaude
				acct := target
				if target == "codex" {
					key = chat.RuntimeCodex
					acct = ""
				}
				engine.Usage().MarkOut(key, acct, "private-fixture", 0, time.Now())
				source := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: beads.New(), UsageCapacity: func() string { return chat.UsageCapacityCause(home, time.Now()) }}}
				snap := source.Snapshot(ctx)
				if len(snap.Display.Workstreams) != 1 || !strings.Contains(snap.Display.Workstreams[0].Text, "usage") || snap.Display.Workstreams[0].Working != 0 {
					t.Fatalf("exhaustion hidden: %+v", snap.Display)
				}
				if e = worker.PassSlot(ctx, "usage-vm"); e == nil {
					t.Fatal("all exhausted accounts silently launched work")
				}
				var attempts int
				if e = st.DB().QueryRow(`SELECT execution_attempts FROM bead_tasks WHERE project_id=? AND id='usagelimit'`, pid).Scan(&attempts); e != nil || attempts != 0 {
					t.Fatal("usage refusal spent launch budget", attempts, e)
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET execution_due_at=0 WHERE project_id=? AND id='usagelimit'`, pid); e != nil {
					t.Fatal(e)
				}
				if e = chat.WriteRuntime(home, first.AgentName, chat.RuntimeClaude); e != nil {
					t.Fatal(e)
				}
				engine.Usage().MarkOut(key, acct, "private-fixture", time.Now().Add(-time.Second).Unix(), time.Now())
				if target == "codex" {
					if e = worker.PassSlot(ctx, "usage-vm"); e == nil {
						t.Fatal("Claude runtime pin escaped to Codex")
					}
					if e = chat.WriteAutoRuntime(home, first.AgentName, chat.RuntimeClaude); e != nil {
						t.Fatal(e)
					}
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels=labels||'["hold"]'::jsonb WHERE project_id=? AND id='goal'`, pid); e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET execution_due_at=0 WHERE project_id=? AND id='usagelimit'`, pid); e != nil {
					t.Fatal(e)
				}
				if e = worker.PassSlot(ctx, "usage-vm"); e != nil {
					t.Fatal(e)
				}
				var runs int
				if e = st.DB().QueryRow(`SELECT count(*) FROM agent_runs WHERE project_id=?`, pid).Scan(&runs); e != nil || runs != 1 {
					t.Fatal("pause lost", runs, e)
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels=labels-'hold' WHERE project_id=? AND id='goal'`, pid); e != nil {
					t.Fatal(e)
				}
				if e = worker.PassSlot(ctx, "usage-vm"); e != nil {
					t.Fatal(e)
				}
				next := adapter.result
				if next == nil || next.RunID == first.RunID || next.SessionID == first.SessionID || next.AgentName != first.AgentName {
					t.Fatalf("recovery identity: first=%+v next=%+v", first, next)
				}
				if target == "backup" && next.Account != "backup" || target == "codex" && next.Runtime != chat.RuntimeCodex {
					t.Fatalf("available account not used: %+v", next)
				}
				data, e := os.ReadFile(filepath.Join(workspace, "prepared.txt"))
				if e != nil || string(data) != "uncommitted prepared artifact" {
					t.Fatal("workspace lost", e)
				}
				awaitNative(t, "replacement completed", func() bool { r, e := st.GetAgentRun(ctx, next.RunID); return e == nil && r.Status == "exited" })
				raw, e = os.ReadFile(filepath.Join(home, "chat", first.AgentName+".jsonl"))
				if e != nil || !strings.Contains(string(raw), "fresh provider conversation") || !strings.Contains(string(raw), first.SessionID) {
					t.Fatal("source handoff missing", e)
				}
			})
		}
	}
}

// Preserve the worker's request, including checkpoint binding and explicit intent.
type usageScenarioSpawner struct {
	inner  *spawn.Spawner
	result *spawn.SpawnResult
}

func (s *usageScenarioSpawner) Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error) {
	result, err := s.inner.Spawn(ctx, req)
	s.result = result
	return result, err
}
