package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestNativeStartupRecoveryUsesDatabaseIntentAndBoundsFailuresWithoutHumanDecision(t *testing.T) {
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	pid, e := st.InsertProject(ctx, store.Project{Name: "startup-recovery", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'task','Task','Do the bounded work','Record evidence','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private startup recovery fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetExecutionReady(ctx, pid, "task", "commander", 1, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	if e = st.ConfigureExecutionSlot(ctx, "native", []string{"knowledge"}, true); e != nil {
		t.Fatal(e)
	}
	claim, e := st.ClaimNextExecution(ctx, "native", 1, time.Now())
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-native-recovery", OpencodeSessionID: "private-recovery-session", SpawnTime: time.Now().Unix(), ExecutionClaimToken: claim.Token, ExecutionDriver: "native", ExecutionRuntime: chat.RuntimeClaude, ExecutionAccount: "default", ExecutionModel: "haiku"})
	if e != nil {
		t.Fatal(e)
	}
	// No metadata/session file exists: the crash boundary is immediately after
	// database binding. The binary is deliberately absent; no model can start.
	versionStub := filepath.Join(home, "private-version")
	if e = os.WriteFile(versionStub, []byte("#!/bin/sh\necho private-test-version\n"), 0700); e != nil {
		t.Fatal(e)
	}
	engine := chat.New(chat.Options{Home: home, Resolver: chat.StoreResolver{Store: st, Home: home}, States: st, Binary: versionStub, HostBinary: filepath.Join(home, "absent-host"), Procs: func(context.Context, string) ([]chat.Proc, error) { return nil, nil }})
	defer engine.Close()
	d := Daemon{store: st, chat: engine}
	if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='task'`, pid); e != nil {
		t.Fatal(e)
	}
	d.recoverNativeExecutions(ctx)
	var attempts int
	var due int64
	var detail string
	if e = st.DB().QueryRow(`SELECT execution_attempts,execution_due_at,execution_error FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&attempts, &due, &detail); e != nil || attempts != 0 || due != 0 || detail != "" {
		t.Fatal("a legitimate hold consumed startup retries", attempts, due, detail, e)
	}
	if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels='[]' WHERE project_id=? AND id='task'`, pid); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 5; i++ {
		d.recoverNativeExecutions(ctx)
		if e = st.DB().QueryRow(`SELECT execution_attempts,execution_due_at,execution_error FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&attempts, &due, &detail); e != nil || attempts != i || due <= time.Now().Unix() || detail == "" {
			t.Fatal("startup failure was not durably bounded", attempts, i, due, detail, e)
		}
		d.recoverNativeExecutions(ctx)
		var unchanged int
		if e = st.DB().QueryRow(`SELECT execution_attempts FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&unchanged); e != nil || unchanged != i {
			t.Fatal("retry ignored backoff", unchanged, i, e)
		}
		// Simulate the due time passing without a wall-clock minute per retry.
		if _, e = st.DB().Exec(`UPDATE bead_tasks SET execution_due_at=0 WHERE project_id=? AND id='task'`, pid); e != nil {
			t.Fatal(e)
		}
	}
	d.recoverNativeExecutions(ctx)
	if e = st.DB().QueryRow(`SELECT execution_attempts FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&attempts); e != nil || attempts != 5 {
		t.Fatal("startup recovery retried beyond its budget", attempts, e)
	}
	run, e := st.GetAgentRun(ctx, rid)
	if e != nil || run.Status != "running" {
		t.Fatal("unobserved process absence fabricated a terminal verdict", run, e)
	}
	var humans int
	if e = st.DB().QueryRow(`SELECT count(*) FROM needs_you`).Scan(&humans); e != nil || humans != 0 {
		t.Fatal("runtime failure was turned into a human decision", humans, e)
	}
}
