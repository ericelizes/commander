package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestQuestionCLIRecordsNativeProviderAndThreadFromItsCanonicalRun(t *testing.T) {
	st, dsn := pgtest.Open(t)
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e := os.WriteFile(filepath.Join(home, "state.postgres"), []byte(dsn), 0600); e != nil {
		t.Fatal(e)
	}
	pid, e := st.InsertProject(ctx, store.Project{Name: "question-cli", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'task','Task','Bounded work','Evidence','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "private native question CLI"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetExecutionReady(ctx, pid, "task", "commander", 1, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	if e = st.ConfigureExecutionSlot(ctx, "native-question", []string{"knowledge"}, true); e != nil {
		t.Fatal(e)
	}
	claim, e := st.ClaimNextExecution(ctx, "native-question", 1, time.Now())
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	role, sid := "operator-native-question", "canonical-question-session"
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: role, OpencodeSessionID: sid, SpawnTime: time.Now().Unix(), ExecutionClaimToken: claim.Token, ExecutionDriver: "native", ExecutionRuntime: chat.RuntimeCodex, ExecutionAccount: "default", ExecutionModel: "gpt-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if e = chat.WriteRuntime(home, role, chat.RuntimeClaude); e != nil {
		t.Fatal(e)
	}
	path := chat.NativeCodexThreadFile(home, role, sid)
	if e = os.WriteFile(path, []byte("canonical-native-thread"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(chat.CodexThreadFile(home, role), []byte("foreign-role-thread"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("COMMANDER_ROLE", role)
	t.Setenv("COMMANDER_RUN_ID", strconv.FormatInt(rid, 10))
	t.Setenv("COMMANDER_RUN_SESSION", sid)
	t.Setenv("COMMANDER_MANAGER_ROLE", "")
	var revision int64
	if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	var out, errs bytes.Buffer
	code := runQuestion([]string{"ask", "--project-id", strconv.FormatInt(pid, 10), "--bead", "task", "--plan-revision", strconv.FormatInt(revision, 10), "--request", "native-cli-checkpoint", "--text", "Which bounded choice should continue?", "--checkpoint", "Saved native conversation and prepared work"}, &out, &errs)
	if code != 0 {
		t.Fatalf("native question CLI: %d %s", code, errs.String())
	}
	var response struct {
		ID string `json:"question_id"`
	}
	if e = json.Unmarshal(out.Bytes(), &response); e != nil || response.ID == "" {
		t.Fatal(out.String(), e)
	}
	runtime, thread, e := st.QuestionNativeCheckpoint(ctx, rid)
	if e != nil || runtime != chat.RuntimeCodex || thread != "canonical-native-thread" {
		t.Fatal("CLI used mutable role context", runtime, thread, e)
	}
	if _, _, e = questionCheckpoint(ctx, st, home, role, rid, "different-session", true); e == nil {
		t.Fatal("foreign execution session borrowed checkpoint")
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if _, _, e = questionCheckpoint(ctx, st, home, role, rid, sid, true); e == nil {
		t.Fatal("missing native thread borrowed foreign role thread")
	}
	if runtime, thread, e = questionCheckpoint(ctx, st, home, role, rid, sid, false); e != nil || runtime != chat.RuntimeCodex || thread != "" {
		t.Fatal("nonblocking question required a paused conversation", runtime, thread, e)
	}
}
