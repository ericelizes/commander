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
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/chat/chattest"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/execution"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

func TestClaimedCodexCheckpointResumesSameThreadFromCanonicalSource(t *testing.T) {
	for _, kind := range []string{"code", "knowledge"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			fakeDir := t.TempDir()
			t.Setenv("CMDR_FAKE_CODEX", "1")
			t.Setenv(chattest.DirVar, fakeDir)
			t.Setenv("COMMANDER_CODEX_BIN", os.Args[0])
			t.Setenv("CODEX_HOME", t.TempDir())
			if e := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), []byte(`{"tokens":{"access_token":"private-test-token"}}`), 0600); e != nil {
				t.Fatal(e)
			}
			release := filepath.Join(home, "release-turn")
			t.Setenv("CMDR_FAKE_CODEX_TURN_RELEASE", release)
			for _, key := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
				t.Setenv(key, "")
			}
			dir := t.TempDir()
			for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Fixture"}, {"commit", "--allow-empty", "-m", "fixture"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("private git: %v %s", e, out)
				}
			}
			st, dsn := pgtest.Open(t)
			if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(filepath.Join(dir, ".beads"), 0700); e != nil {
				t.Fatal(e)
			}
			pid, e := st.InsertProject(ctx, store.Project{Name: "codex-checkpoint", Path: dir, Type: kind, Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'task','Task','Bounded implementation','Evidence recorded','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private Codex checkpoint"); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetExecutionReady(ctx, pid, "task", "commander", 1, 1, true, nil); e != nil {
				t.Fatal(e)
			}
			if e = st.ConfigureExecutionSlot(ctx, "codex-vm", []string{kind}, true); e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(api.New(api.Options{Token: "private-codex", StateStore: st}).Handler())
			defer server.Close()
			for key, value := range map[string]string{"api-url": server.URL, "api-token": "private-codex"} {
				if e = os.WriteFile(filepath.Join(home, key), []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}
			noClaude := filepath.Join(home, "no-claude")
			if e = os.WriteFile(noClaude, []byte("#!/bin/sh\nexit 93\n"), 0700); e != nil {
				t.Fatal(e)
			}
			opts := chat.Options{Home: home, Binary: noClaude, HostBinary: os.Args[0], States: st, Resolver: chat.StoreResolver{Store: st, Home: home}, Procs: func(context.Context, string) ([]chat.Proc, error) { return nil, nil }}
			engine := chat.New(opts)
			t.Cleanup(func() {
				for _, status := range engine.List() {
					if status.Running {
						_ = engine.Stop(ctx, status.Role)
					}
				}
				engine.Close()
			})
			tx, dk := &tmuxctl.MockClient{}, &docker.MockClient{}
			sp := spawn.New(st, &opencode.MockClient{}, beads.New(), &agentmail.MockClient{}, dk, tx, t.TempDir()).WithNativeExecution(engine)
			adapter := &runtimeContractSpawner{inner: sp, runtime: chat.RuntimeCodex}
			worker := execution.Worker{Store: st, Spawner: adapter, Capacity: 1}
			if e = worker.PassSlot(ctx, "codex-vm"); e != nil {
				t.Fatal(e)
			}
			first := adapter.result
			if first == nil {
				t.Fatal("no native Codex execution")
			}
			threadFile := chat.NativeCodexThreadFile(home, first.AgentName, first.SessionID)
			awaitNative(t, "first native Codex thread", func() bool { raw, _ := os.ReadFile(threadFile); return strings.TrimSpace(string(raw)) != "" })
			threadRaw, e := os.ReadFile(threadFile)
			if e != nil {
				t.Fatal(e)
			}
			thread := strings.TrimSpace(string(threadRaw))
			var revision int64
			if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&revision); e != nil {
				t.Fatal(e)
			}
			question, e := st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "task", PlanRevision: revision, Actor: first.AgentName, RunID: first.RunID, Session: first.SessionID, ClientKey: "codex-checkpoint", Question: "Which bounded option should continue?", Recommendation: "Keep the prepared work", Checkpoint: "Native Codex conversation and workspace retained", CheckpointRuntime: chat.RuntimeCodex, CheckpointThread: thread, Blocking: true})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.AnswerQuestion(ctx, question, "commander", "Continue the prepared work under the current acceptance criteria", true); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(release, []byte("release private fake turn"), 0600); e != nil {
				t.Fatal(e)
			}
			awaitNative(t, "first physical Codex exit", func() bool {
				r, e := st.GetAgentRun(ctx, first.RunID)
				return e == nil && r.Status == "exited" && !engine.Running(first.AgentName)
			})
			// Derived session/chat metadata and mutable provider preferences are not
			// authority to select a different conversation for this source checkpoint.
			files, e := filepath.Glob(filepath.Join(dir, ".commander", "operators", "*.claude.session-id"))
			if e != nil || len(files) == 0 {
				t.Fatal("missing derived session fixture", files, e)
			}
			for _, file := range files {
				if e = os.Remove(file); e != nil {
					t.Fatal(e)
				}
			}
			engine.Close()
			if e = os.Remove(filepath.Join(home, "chat", first.AgentName+".meta.json")); e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(threadFile); e != nil {
				t.Fatal(e)
			} // Recover only the DB-saved cursor, then require actual server resume.
			if e = chat.WriteRuntime(home, first.AgentName, chat.RuntimeClaude); e != nil {
				t.Fatal(e)
			}
			engine = chat.New(opts)
			sp.WithNativeExecution(engine)
			if e = worker.PassSlot(ctx, "codex-vm"); e != nil {
				t.Fatal("native Codex checkpoint did not resume", e)
			}
			next := adapter.result
			if next == nil || next.RunID == first.RunID || next.SessionID != first.SessionID || next.AgentName != first.AgentName || next.Runtime != chat.RuntimeCodex {
				t.Fatal("checkpoint source identity changed", first, next)
			}
			awaitNative(t, "resumed physical Codex exit", func() bool {
				r, e := st.GetAgentRun(ctx, next.RunID)
				return e == nil && r.Status == "exited" && !engine.Running(next.AgentName)
			})
			requests, e := os.ReadFile(filepath.Join(fakeDir, "codex-requests.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			starts, resumes := 0, 0
			for _, line := range strings.Split(string(requests), "\n") {
				var req struct {
					Method string
					Params struct {
						ThreadID string `json:"threadId"`
					}
				}
				if json.Unmarshal([]byte(line), &req) != nil {
					continue
				}
				if req.Method == "thread/start" {
					starts++
				}
				if req.Method == "thread/resume" {
					resumes++
					if req.Params.ThreadID != thread {
						t.Fatal("resumed a different saved thread", req.Params.ThreadID, thread)
					}
				}
			}
			if starts != 1 || resumes != 1 {
				t.Fatal("checkpoint silently created a fresh Codex context", starts, resumes)
			}
			if len(tx.Calls) > 0 || len(dk.Calls) > 0 {
				t.Fatal("checkpoint used a second runtime", tx.Calls, dk.Calls)
			}
			var state string
			if e = st.DB().QueryRow(`SELECT status FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&state); e != nil || state != "open" {
				t.Fatal("checkpoint completion closed unfinished work", state, e)
			}
		})
	}
}
