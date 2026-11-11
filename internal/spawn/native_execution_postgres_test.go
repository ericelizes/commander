package spawn_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/example-user/commander/internal/statusclassify"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

func awaitNative(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out: " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type unavailableNativeRuntime struct{}

func (unavailableNativeRuntime) Start(context.Context, string, chat.StartOptions) (chat.Status, error) {
	return chat.Status{}, errors.New("private native runtime temporarily unavailable")
}

type fixtureStatusRunner struct{}

func (fixtureStatusRunner) Run(context.Context, chat.AccountView, string) (statusclassify.Answer, error) {
	return statusclassify.Answer{Kind: "working", Description: "Private native tool is preparing its bounded result", Input: 32, Output: 8}, nil
}

func TestClaimedExecutionUsesRealNativeHostAndRecoversCommittedStartup(t *testing.T) {
	var scenario struct{ Kinds []string }
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-native-worker.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || len(scenario.Kinds) != 2 {
		t.Fatal("missing native worker scenario", e)
	}
	for _, kind := range scenario.Kinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("COMMANDER_HOME", home)
			t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			fakeDir, projects := t.TempDir(), filepath.Join(home, ".claude", "projects")
			t.Setenv(chattest.EnvVar, "1")
			t.Setenv(chattest.DirVar, fakeDir)
			t.Setenv(chattest.ProjectsVar, projects)
			for _, key := range []string{"COMMANDER_ROLE", "COMMANDER_RUN_ID", "COMMANDER_RUN_SESSION", "COMMANDER_MANAGER_ROLE"} {
				t.Setenv(key, "")
			}
			// Both possible provider processes are private fakes. This fixture explicitly
			// selects Claude, and no real provider/model or mail service is contacted.
			t.Setenv("COMMANDER_CODEX_BIN", filepath.Join(home, "no-codex"))
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
			pid, e := st.InsertProject(ctx, store.Project{Name: "native-worker", Path: dir, Type: kind, Status: "active"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(?,'goal','Native workstream','["kind:objective","kind:workstream"]','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,parent_id,labels,created_at,updated_at) VALUES(?,'assigned','Bounded work','Implement the exact bounded change','Record verifiable evidence','goal','["kind:objective"]','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private native worker flow"); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetExecutionReady(ctx, pid, "assigned", "commander", 1, 1, true, nil); e != nil {
				t.Fatal(e)
			}
			if e = st.ConfigureExecutionSlot(ctx, "native-vm", []string{kind}, true); e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(api.New(api.Options{Token: "private-native-worker", StateStore: st}).Handler())
			defer server.Close()
			for key, value := range map[string]string{"api-url": server.URL, "api-token": "private-native-worker"} {
				if e = os.WriteFile(filepath.Join(home, key), []byte(value), 0600); e != nil {
					t.Fatal(e)
				}
			}
			resolver := chat.StoreResolver{Store: st, Home: home}
			opts := chat.Options{Home: home, Binary: os.Args[0], HostBinary: os.Args[0], States: st, Resolver: resolver, ProjectsDir: projects, Procs: func(context.Context, string) ([]chat.Proc, error) { return nil, nil }, StopGrace: time.Second, InterruptGrace: time.Second}
			engine := chat.New(opts)
			t.Cleanup(func() {
				for _, status := range engine.List() {
					if status.Running {
						_ = engine.Stop(ctx, status.Role)
					}
				}
				engine.Close()
			})
			dk, tx := &docker.MockClient{}, &tmuxctl.MockClient{}
			sp := spawn.New(st, &opencode.MockClient{}, beads.New(), &agentmail.MockClient{}, dk, tx, t.TempDir()).WithNativeExecution(unavailableNativeRuntime{})
			adapter := &runtimeContractSpawner{inner: sp, runtime: chat.RuntimeClaude}
			worker := execution.Worker{Store: st, Spawner: adapter, Capacity: 1}
			if e = worker.PassSlot(ctx, "native-vm"); e == nil {
				t.Fatal("lost initial host-start failure")
			}
			result := adapter.result
			if result == nil || result.RunID <= 0 {
				t.Fatal("startup did not retain committed run", result)
			}
			run, e := st.GetAgentRun(ctx, result.RunID)
			if e != nil || run.Status != "running" || run.TmuxPaneID.Valid || run.ContainerID.Valid {
				t.Fatal("native custody was replaced by a legacy process", run, e)
			}
			var attempts int
			var due int64
			var detail string
			if e = st.DB().QueryRow(`SELECT execution_attempts,execution_due_at,execution_error FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&attempts, &due, &detail); e != nil || attempts != 1 || due <= time.Now().Unix() || detail == "" {
				t.Fatal("startup recovery not bounded/durable", attempts, due, detail, e)
			}
			// A committed startup intent alone must never produce a live count.
			facts, e := st.ObservedExecutions(ctx)
			if e != nil || len(facts) != 1 || facts[0].Known {
				t.Fatalf("startup claim invented process evidence: %+v %v", facts, e)
			}
			roles, e := st.NativeExecutionRoles(ctx, time.Now().Unix())
			if e != nil || len(roles) != 0 {
				t.Fatal("startup retry ignored backoff", roles, e)
			}
			roles, e = st.NativeExecutionRoles(ctx, due)
			if e != nil || len(roles) != 1 || roles[0] != result.AgentName {
				t.Fatal("committed startup vanished from database recovery", roles, e)
			}
			// Native recovery uses the current recorded run, without another capacity
			// claim or a separate delegation message. The host supplies its first turn.
			status, e := engine.Start(ctx, result.AgentName, chat.StartOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if e = st.RecordNativeStartupAttempt(ctx, result.AgentName, result.RunID, result.SessionID, "", time.Now().Unix()); e != nil {
				t.Fatal(e)
			}
			awaitNative(t, "native first instruction and fake permission", func() bool { s, _ := engine.Status(result.AgentName); return len(s.Pending) > 0 })
			observations := engine.RuntimeObservations()
			if len(observations) != 1 || observations[0].RunID != result.RunID || observations[0].SessionID != result.SessionID || observations[0].PID != status.PID {
				t.Fatalf("host observation lost its identity: %+v", observations)
			}
			if e = st.RecordRuntimeEvidence(ctx, nil, time.Now(), observations); e != nil {
				t.Fatal(e)
			}
			// Exercise the same run-fenced activity write used by native tools.
			if e = st.SetAgentStateForRun(ctx, result.AgentName, result.RunID, result.SessionID, store.AgentWorking, "private fixture tool"); e != nil {
				t.Fatal(e)
			}
			source := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: beads.New()}}
			liveSnapshot := source.Snapshot(ctx)
			if liveSnapshot.Degraded != nil || len(liveSnapshot.Executions) != 1 || liveSnapshot.Executions[0].RunID != result.RunID || liveSnapshot.Executions[0].State != store.AgentWorking {
				t.Fatalf("real host missing from snapshot: %+v degraded=%+v", liveSnapshot.Executions, liveSnapshot.Degraded)
			}
			recordRaw, e := os.ReadFile(filepath.Join(fakeDir, "argv-"+result.SessionID+".json"))
			if e != nil {
				t.Fatal(e)
			}
			var record map[string]any
			if e = json.Unmarshal(recordRaw, &record); e != nil {
				t.Fatal(e)
			}
			if record["run_id"] != strconv.FormatInt(result.RunID, 10) || record["run_session"] != result.SessionID || record["role"] != result.AgentName || record["manager_role"] != "" {
				t.Fatal("provider did not inherit immutable run context", record)
			}
			// A normal connected-account worker consumes actual native event
			// history, but its model runner is a private deterministic fake.
			if e = os.MkdirAll(filepath.Join(home, ".claude"), 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"private-fixture-token"}}`), 0600); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				if !t.Failed() {
					return
				}
				var cursors, jobs, usage int
				_ = st.DB().QueryRow(`SELECT COUNT(*) FROM agent_sessions`).Scan(&cursors)
				_ = st.DB().QueryRow(`SELECT COUNT(*) FROM classification_jobs`).Scan(&jobs)
				_ = st.DB().QueryRow(`SELECT COUNT(*) FROM classification_usage`).Scan(&usage)
				t.Logf("private classifier evidence: cursors=%d jobs=%d usage=%d accounts=%d", cursors, jobs, usage, len(chat.Accounts(home, chat.ReadUsage(home), time.Now())))
				events, sub, err := engine.Subscribe(result.AgentName, 0)
				if err == nil {
					sub.Close()
					for _, ev := range events {
						t.Logf("private native event kind=%s run=%d sid=%s seq=%d", ev.Kind, ev.RunID, ev.SessionID, ev.Seq)
					}
				}
			})
			classifierCtx, stopClassifier := context.WithCancel(ctx)
			classifierDone := make(chan struct{})
			classifier := &statusclassify.Worker{Store: st, Engine: engine, Home: home, Runner: fixtureStatusRunner{}, MonthlyTokens: 100000}
			go func() { defer close(classifierDone); classifier.Run(classifierCtx) }()
			t.Cleanup(func() { stopClassifier(); <-classifierDone })
			awaitNative(t, "native event description reaches its exact bead", func() bool {
				var text string
				var rid int64
				err := st.DB().QueryRow(`SELECT b.status_text,a.run_id FROM bead_tasks b JOIN agent_sessions a ON a.role=b.status_role WHERE b.project_id=? AND b.id='assigned'`, pid).Scan(&text, &rid)
				return err == nil && rid == result.RunID && text == "Private native tool is preparing its bounded result"
			})
			stopClassifier()
			<-classifierDone
			before, e := resolver.Resolve(ctx, result.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = engine.SetRuntime(ctx, result.AgentName, chat.RuntimeCodex, chat.SwitchOptions{}); e == nil || !strings.Contains(e.Error(), "bound") {
				t.Fatal("runtime switch could mutate claimed execution", e)
			}
			if _, e = engine.Fresh(ctx, result.AgentName); e == nil || !strings.Contains(e.Error(), "bound") {
				t.Fatal("fresh session could replace claimed execution", e)
			}
			if e = os.MkdirAll(filepath.Join(home, "claude-accounts", "different-account"), 0700); e != nil {
				t.Fatal(e)
			}
			if e = chat.WriteRuntime(home, result.AgentName, chat.RuntimeCodex); e != nil {
				t.Fatal(e)
			}
			if e = chat.WriteAccount(home, result.AgentName, "different-account"); e != nil {
				t.Fatal(e)
			}
			if _, e = st.DB().Exec(`UPDATE agent_runs SET model='unrelated-observed-model' WHERE id=?`, result.RunID); e != nil {
				t.Fatal(e)
			}
			after, e := resolver.Resolve(ctx, result.AgentName)
			if e != nil || after.Runtime != chat.RuntimeClaude || after.Account != before.Account || after.Model != before.Model {
				t.Fatal("mutable role files or usage changed bound runtime", before, after, e)
			}
			engine.Close()
			engine = chat.New(opts)
			adopted, e := engine.Start(ctx, result.AgentName, chat.StartOptions{})
			if e != nil || adopted.PID != status.PID {
				t.Fatal("native recovery duplicated its process", adopted, status, e)
			}
			awaitNative(t, "permission replay", func() bool { s, _ := engine.Status(result.AgentName); return len(s.Pending) > 0 })
			preservedWorkspace := dir
			if kind == "code" {
				preservedWorkspace = result.WorktreePath
			}
			if e = os.WriteFile(filepath.Join(preservedWorkspace, "checkpoint-artifact.txt"), []byte("prepared checkpoint artifact"), 0600); e != nil {
				t.Fatal(e)
			}
			var checkpointHead []byte
			if kind == "code" {
				for _, args := range [][]string{{"add", "checkpoint-artifact.txt"}, {"commit", "-m", "preserved checkpoint"}} {
					cmd := exec.Command("git", args...)
					cmd.Dir = preservedWorkspace
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("checkpoint git: %v %s", err, out)
					}
				}
				checkpointHead, e = exec.Command("git", "-C", preservedWorkspace, "rev-parse", "HEAD").Output()
				if e != nil {
					t.Fatal(e)
				}
			}
			var revision int64
			if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&revision); e != nil {
				t.Fatal(e)
			}
			question, e := st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "assigned", PlanRevision: revision, Actor: result.AgentName, RunID: result.RunID, Session: result.SessionID, ClientKey: "native-checkpoint", Question: "Which bounded implementation should continue?", Recommendation: "Keep the prepared implementation", Checkpoint: "Workspace and conversation retained; await the planning answer", CheckpointRuntime: chat.RuntimeClaude, Blocking: true})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.AnswerQuestion(ctx, question, "commander", "Continue the prepared implementation within the existing acceptance criteria", true); e != nil {
				t.Fatal(e)
			}
			current, _ := engine.Status(result.AgentName)
			for _, request := range current.Pending {
				if e = engine.Approve(ctx, result.AgentName, request.ID, chat.Decision{Behavior: "allow"}); e != nil {
					t.Fatal(e)
				}
			}
			awaitNative(t, "real host exit committed", func() bool {
				r, e := st.GetAgentRun(ctx, result.RunID)
				return e == nil && r.Status == "exited" && !engine.Running(result.AgentName)
			})
			exitedSnapshot := source.Snapshot(ctx)
			if exitedSnapshot.Degraded != nil || len(exitedSnapshot.Executions) != 0 {
				t.Fatalf("exited host still counted: %+v degraded=%+v", exitedSnapshot.Executions, exitedSnapshot.Degraded)
			}
			events, sub, e := engine.Subscribe(result.AgentName, 0)
			if e != nil {
				t.Fatal(e)
			}
			sub.Close()
			starts := 0
			for _, event := range events {
				if event.Kind == chat.KindUser && event.Origin == "execution" {
					if event.RunID != result.RunID {
						t.Fatalf("journal event lost run identity: %+v", event)
					}
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("first instruction delivered %d times across adoption", starts)
			}
			if len(tx.Calls) != 0 || len(dk.Calls) != 0 {
				t.Fatal("native execution reached legacy runtime", tx.Calls, dk.Calls)
			}
			var beadStatus string
			if e = st.DB().QueryRow(`SELECT status FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&beadStatus); e != nil || beadStatus != "open" {
				t.Fatal("native process falsely completed bead", beadStatus, e)
			}
			if kind == "code" {
				if _, e = os.Stat(result.WorktreePath); e != nil {
					t.Fatal("native execution lost its workspace", e)
				}
				if fmt.Sprint(record["cwd"]) != result.WorktreePath {
					t.Fatal("wrong native code cwd", record["cwd"], result.WorktreePath)
				}
			}
			// Delete the derived role session file. Its source identity and provider
			// binding come from the canonical run, not role preferences or this copy.
			files, _ := filepath.Glob(filepath.Join(dir, ".commander", "operators", "*.claude.session-id"))
			for _, file := range files {
				if e = os.Remove(file); e != nil {
					t.Fatal(e)
				}
			}
			sp.WithNativeExecution(engine)
			if e = worker.PassSlot(ctx, "native-vm"); e != nil {
				t.Fatal("native checkpoint did not resume through capacity", e)
			}
			resumed := adapter.result
			if resumed == nil || resumed.RunID == result.RunID || resumed.SessionID != result.SessionID || resumed.AgentName != result.AgentName || resumed.Account != result.Account || resumed.Runtime != result.Runtime {
				t.Fatal("checkpoint lost source identity/provider", resumed, result)
			}
			awaitNative(t, "checkpoint first turn", func() bool { s, _ := engine.Status(resumed.AgentName); return len(s.Pending) > 0 })
			resumedSpec, e := resolver.Resolve(ctx, resumed.AgentName)
			if e != nil || !resumedSpec.ExecutionResume || !strings.Contains(resumedSpec.ExecutionInitialPrompt, fmt.Sprintf("run %d", result.RunID)) {
				t.Fatal("resumed host lost its saved answer/source", resumedSpec, e)
			}
			resumedRecordRaw, e := os.ReadFile(filepath.Join(fakeDir, "argv-"+result.SessionID+".json"))
			if e != nil {
				t.Fatal(e)
			}
			var resumedRecord map[string]any
			if e = json.Unmarshal(resumedRecordRaw, &resumedRecord); e != nil {
				t.Fatal(e)
			}
			if resumedRecord["run_id"] != strconv.FormatInt(resumed.RunID, 10) || !strings.Contains(fmt.Sprint(resumedRecord["argv"]), "--resume") {
				t.Fatal("checkpoint started fresh Claude context", resumedRecord)
			}
			artifact, e := os.ReadFile(filepath.Join(preservedWorkspace, "checkpoint-artifact.txt"))
			if e != nil || string(artifact) != "prepared checkpoint artifact" {
				t.Fatal("checkpoint lost prepared work", e)
			}
			if kind == "code" {
				head, err := exec.Command("git", "-C", resumed.WorktreePath, "rev-parse", "HEAD").Output()
				if err != nil || string(head) != string(checkpointHead) {
					t.Fatal("resuming a code checkpoint reset its committed branch", err)
				}
			}
			resumedStatus, _ := engine.Status(resumed.AgentName)
			for _, request := range resumedStatus.Pending {
				if e = engine.Approve(ctx, resumed.AgentName, request.ID, chat.Decision{Behavior: "allow"}); e != nil {
					t.Fatal(e)
				}
			}
			awaitNative(t, "checkpoint process exited", func() bool {
				r, e := st.GetAgentRun(ctx, resumed.RunID)
				return e == nil && r.Status == "exited" && !engine.Running(resumed.AgentName)
			})

			// The next ready bead uses the same capacity through the actual worker and
			// native spawner, proving capacity reuse on this complete process path.
			reviewedSnapshot := source.Snapshot(ctx)
			if reviewedSnapshot.Degraded != nil || len(reviewedSnapshot.Executions) != 0 || len(reviewedSnapshot.Workstreams) != 1 || reviewedSnapshot.Display == nil || len(reviewedSnapshot.Display.Workstreams) != 1 {
				t.Fatalf("terminal result missing from live board: %+v", reviewedSnapshot)
			}
			row := reviewedSnapshot.Display.Workstreams[0]
			// A finished result is the planning Commander's move: review it.
			if row.Kind != "working" || row.Text != "Commander to review the finished result" || row.Working != 0 || row.Actor == nil || row.Actor.Lead.Reason != "review_result" {
				t.Fatalf("completed native turn has the wrong handoff: %+v", row)
			}
			steps := reviewedSnapshot.Workstreams[0].Steps
			if len(steps) != 1 || steps[0].ResultRunID != resumed.RunID || steps[0].ResultReadyAt == 0 || steps[0].Broken {
				t.Fatalf("result is not a held review obligation: %+v", steps)
			}
			if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'next','Next work','Fresh bounded work','Recorded evidence','now','now')`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetExecutionReady(ctx, pid, "next", "commander", 1, 1, true, nil); e != nil {
				t.Fatal(e)
			}
			sp.WithNativeExecution(engine)
			if e = worker.PassSlot(ctx, "native-vm"); e != nil {
				t.Fatal(e)
			}
			next := adapter.result
			if next == nil || next.RunID == result.RunID || next.SessionID == result.SessionID || next.AgentName == result.AgentName {
				t.Fatal("next bead reused old execution identity", next, result)
			}
			awaitNative(t, "next execution receives its first turn", func() bool { s, _ := engine.Status(next.AgentName); return len(s.Pending) > 0 })
			nextStatus, _ := engine.Status(next.AgentName)
			for _, request := range nextStatus.Pending {
				if e = engine.Approve(ctx, next.AgentName, request.ID, chat.Decision{Behavior: "allow"}); e != nil {
					t.Fatal(e)
				}
			}
			awaitNative(t, "next native execution exits", func() bool {
				r, e := st.GetAgentRun(ctx, next.RunID)
				return e == nil && r.Status == "exited" && !engine.Running(next.AgentName)
			})

		})
	}
}
