package chat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
)

type delayedNativeExitSink struct {
	*store.Store
	fail atomic.Bool
}

func (s *delayedNativeExitSink) FinishNativeExecution(ctx context.Context, role string, rid int64, sid, status string) error {
	if s.fail.Load() {
		return errors.New("simulated unavailable exit commit")
	}
	return s.Store.FinishNativeExecution(ctx, role, rid, sid, status)
}
func setupNativeExitHarness(t *testing.T, role string) (*harness, *store.Store, int64, int64, string) {
	t.Helper()
	h := newHarness(t)
	st, r, pid, initial, _ := nativeClaimFixture(t, h.home, role, "knowledge")
	ctx := context.Background()
	if e := st.MarkClosed(ctx, initial, "exited"); e != nil {
		t.Fatal(e)
	}
	if e := st.ConfigureExecutionSlot(ctx, "native-capacity", []string{"knowledge"}, true); e != nil {
		t.Fatal(e)
	}
	var revision int64
	if e := st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	if _, e := st.SetExecutionReady(ctx, pid, "assigned", "commander", revision, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	if e := st.ReapExecutionStartupClaims(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	claim, e := st.ClaimNextExecution(ctx, "native-capacity", 1, time.Now())
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	sid := agentrt.NewClaudeSessionID()
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: "assigned", AgentName: role, OpencodeSessionID: sid, SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	h.opts.States = st
	h.opts.Resolver = r
	h.e = New(h.opts)
	return h, st, pid, rid, sid
}
func TestNativeExecutionOneTurnReleasesCapacityWithoutCompletingWork(t *testing.T) {
	var scenario struct{ Role string }
	raw, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-native-exit-lifecycle.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &scenario); e != nil || scenario.Role == "" {
		t.Fatal("missing native lifecycle scenario", e)
	}
	h, st, pid, rid, _ := setupNativeExitHarness(t, scenario.Role)
	ctx := context.Background()
	if _, e := st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'next','Next','Next assigned task','Evidence','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e := st.SetExecutionReady(ctx, pid, "next", "commander", 1, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	process, e := h.e.Start(ctx, scenario.Role, StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if c, e := st.ClaimNextExecution(ctx, "native-capacity", 1, time.Now()); e != nil || c != nil {
		t.Fatal("capacity released before exit", c, e)
	}
	if e = h.e.Send(ctx, scenario.Role, "hello"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "actual model exited and canonical run closed", func() bool {
		r, e := st.GetAgentRun(ctx, rid)
		return e == nil && r.Status == "exited" && !alive(process.PID)
	})
	var status, owner string
	var results int
	if e = st.DB().QueryRow(`SELECT status,responsible_agent_id,(SELECT count(*) FROM needs_you) FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&status, &owner, &results); e != nil || status != "open" || owner != "commander" || results != 0 {
		t.Fatal("turn completion falsely completed work or escalated to user", status, owner, results, e)
	}
	claim, e := st.ClaimNextExecution(ctx, "native-capacity", 1, time.Now())
	if e != nil || claim == nil || claim.BeadID != "next" {
		t.Fatal("finished native process held capacity", claim, e)
	}
}
func TestNativeExitReceiptSurvivesFailedCommitAndRecoversAfterRestart(t *testing.T) {
	h, st, _, rid, sid := setupNativeExitHarness(t, "operator-native-recover")
	sink := &delayedNativeExitSink{Store: st}
	sink.fail.Store(true)
	h.e.opts.States = sink
	h.opts.States = sink
	ctx := context.Background()
	process, e := h.e.Start(ctx, "operator-native-recover", StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.e.Send(ctx, "operator-native-recover", "hello"); e != nil {
		t.Fatal(e)
	}
	path := nativeExitPath(h.home, "operator-native-recover", rid)
	waitUntil(t, "physical exit with durable receipt", func() bool {
		_, e := os.Stat(path)
		return e == nil && !alive(process.PID) && !h.e.Running("operator-native-recover")
	})
	row, e := st.GetAgentRun(ctx, rid)
	if e != nil || row.Status != "running" {
		t.Fatal("failed commit fabricated a released claim", row, e)
	}
	if _, e = h.e.Start(ctx, "operator-native-recover", StartOptions{}); e == nil {
		t.Fatal("uncommitted exit restarted completed work")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var receipt nativeExitReceipt
	if e = json.Unmarshal(raw, &receipt); e != nil || receipt.SessionID != sid || receipt.RunID != rid {
		t.Fatal(receipt, e)
	}
	h.e.Close()
	sink.fail.Store(false)
	h.e = New(h.opts)
	h.e.ResumeRunning(ctx)
	waitUntil(t, "receipt reconciled after restart", func() bool {
		row, e := st.GetAgentRun(ctx, rid)
		_, fileErr := os.Stat(path)
		return e == nil && row.Status == "exited" && os.IsNotExist(fileErr)
	})
}
func TestNativeExecutionUsageLimitDoesNotRestartARegisteredPlannerInline(t *testing.T) {
	h, st, _, rid, _ := setupNativeExitHarness(t, "manager-native-limit")
	ctx := context.Background()
	process, e := h.e.Start(ctx, "manager-native-limit", StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.e.Send(ctx, "manager-native-limit", "usagelimit"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "usage-limited execution physically ended", func() bool {
		row, e := st.GetAgentRun(ctx, rid)
		return e == nil && row.Status == "native-error" && !alive(process.PID)
	})
	if h.e.Running("manager-native-limit") {
		t.Fatal("execution was restarted through Manager usage steering")
	}
}
func TestNativeReceiptFailureDoesNotStarveOtherRecordedExits(t *testing.T) {
	h, st, pid, rid, sid := setupNativeExitHarness(t, "operator-good-exit")
	ctx := context.Background()
	dir := filepath.Dir(nativeExitPath(h.home, "operator-good-exit", rid))
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	// An unverifiable old source is retained, while an independent valid exit
	// continues through canonical reconciliation in the same bounded pass.
	bad := nativeExitReceipt{Role: "operator-a-invalid", RunID: 9999999, SessionID: "unknown", Status: "exited", ObservedAt: time.Now().Unix()}
	good := nativeExitReceipt{Role: "operator-good-exit", RunID: rid, SessionID: sid, Status: "exited", ObservedAt: time.Now().Unix()}
	for _, receipt := range []nativeExitReceipt{bad, good} {
		raw, _ := json.Marshal(receipt)
		if e := os.WriteFile(nativeExitPath(h.home, receipt.Role, receipt.RunID), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := h.e.flushNativeExits(ctx, ""); e == nil {
		t.Fatal("unverifiable source silently accepted")
	}
	row, e := st.GetAgentRun(ctx, rid)
	if e != nil || row.Status != "exited" {
		t.Fatal("one bad receipt starved another execution", row, e)
	}
	var status string
	if e = st.DB().QueryRow(`SELECT status FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&status); e != nil || status != "open" {
		t.Fatal("exit completion closed its bead", status, e)
	}
}

func TestNativeCodexCheckpointSelectsOnlyMatchingHostRunThread(t *testing.T) {
	home := t.TempDir()
	role := "operator-checkpoint-thread"
	sid := agentrt.NewClaudeSessionID()
	if e := os.MkdirAll(filepath.Join(home, "chat"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := writeMeta(filepath.Join(home, "chat"), role, meta{ExecutionRunID: 17, SessionID: sid, Running: true}); e != nil {
		t.Fatal(e)
	}
	native := (Spec{Role: role, ExecutionRunID: 17, SessionID: sid}).codexThreadFile(home)
	if got := CodexThreadFileForRun(home, role, 17, sid); got != native {
		t.Fatal("native question used a different execution's thread", got, native)
	}
	for _, identity := range []struct {
		rid     int64
		session string
	}{{18, sid}, {17, "another-session"}} {
		if got := CodexThreadFileForRun(home, role, identity.rid, identity.session); got != CodexThreadFile(home, role) {
			t.Fatal("another run borrowed native thread metadata", got)
		}
	}
}

func TestNativeHostFinishesAndRecordsExitWhileEngineIsDetached(t *testing.T) {
	role := "operator-detached-finish"
	h, st, _, rid, _ := setupNativeExitHarness(t, role)
	ctx := context.Background()
	process, e := h.e.Start(ctx, role, StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.e.Send(ctx, role, "tool"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "fake tool awaits permission", func() bool { status, e := h.e.Status(role); return e == nil && len(status.Pending) == 1 })
	status, _ := h.e.Status(role)
	requestID := status.Pending[0].ID
	h.e.Close()
	connection, _, e := dialHost(hostSock(h.home, role))
	if e != nil {
		t.Fatal(e)
	}
	response, _ := json.Marshal(controlSuccess(requestID, map[string]any{"behavior": "allow"}))
	if e = connection.send(hostMsg{Op: "in", Line: string(response)}); e != nil {
		t.Fatal(e)
	}
	connection.close()
	path := nativeExitPath(h.home, role, rid)
	waitUntil(t, "host independently ended native model", func() bool { _, e := os.Stat(path); return e == nil && !alive(process.PID) })
	row, e := st.GetAgentRun(ctx, rid)
	if e != nil || row.Status != "running" {
		t.Fatal("exit was committed without its daemon consumer", row, e)
	}
	h.e = New(h.opts)
	h.e.ResumeRunning(ctx)
	waitUntil(t, "detached exit consumed after daemon recovery", func() bool { row, e := st.GetAgentRun(ctx, rid); return e == nil && row.Status == "exited" })
}
func TestNativeCheckpointAnswerAndExitResumeThroughCanonicalCapacity(t *testing.T) {
	for _, outcome := range []string{"hello", "usagelimit"} {
		t.Run(outcome, func(t *testing.T) {
			role := "operator-native-checkpoint"
			h, st, pid, rid, sid := setupNativeExitHarness(t, role)
			ctx := context.Background()
			process, e := h.e.Start(ctx, role, StartOptions{})
			if e != nil {
				t.Fatal(e)
			}
			var revision int64
			if e = st.DB().QueryRow(`SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&revision); e != nil {
				t.Fatal(e)
			}
			qid, e := st.AskQuestion(ctx, store.QuestionRequest{ProjectID: pid, BeadID: "assigned", Actor: role, Session: sid, RunID: rid, PlanRevision: revision, ClientKey: "native-checkpoint", Question: "Which safe path should I use?", Checkpoint: "Continue the assigned work using the recorded safe path", CheckpointRuntime: RuntimeClaude, Blocking: true})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.AnswerQuestion(ctx, qid, "commander", "Use the bounded safe path", true); e != nil {
				t.Fatal(e)
			}
			var ready bool
			if e = st.DB().QueryRow(`SELECT execution_ready FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&ready); e != nil || ready {
				t.Fatal("answer alone released a live native claim", ready, e)
			}
			if e = h.e.Send(ctx, role, outcome); e != nil {
				t.Fatal(e)
			}
			waitUntil(t, "native checkpoint physically exited", func() bool {
				row, e := st.GetAgentRun(ctx, rid)
				return e == nil && row.Status != "running" && !alive(process.PID)
			})
			claim, e := st.ClaimNextExecution(ctx, "native-capacity", 1, time.Now())
			if e != nil || claim == nil || claim.ResumeRunID != rid || claim.BeadID != "assigned" {
				t.Fatal("saved answer/exit did not queue the exact checkpoint", claim, e)
			}
			next, e := st.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: claim.Token, ResumeSourceRunID: rid, ProjectID: pid, TaskBeadID: "assigned", AgentName: role, OpencodeSessionID: sid, SpawnTime: time.Now().Unix()})
			if e != nil || next == rid {
				t.Fatal("checkpoint did not bind a new canonical execution", next, e)
			}
		})
	}
}

func TestNativeConnectionLossDoesNotReleaseAStillLiveExecution(t *testing.T) {
	role := "operator-native-disconnect"
	h, st, _, rid, _ := setupNativeExitHarness(t, role)
	ctx := context.Background()
	started, e := h.e.Start(ctx, role, StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.e.Send(ctx, role, "tool"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "native tool waiting", func() bool { status, e := h.e.Status(role); return e == nil && len(status.Pending) > 0 })
	c, _ := h.e.chat(role)
	c.mu.Lock()
	process := c.proc
	c.mu.Unlock()
	h.e.nativeDisconnected(c, process)
	row, e := st.GetAgentRun(ctx, rid)
	if e != nil || row.Status != "running" || !alive(started.PID) {
		t.Fatal("connection loss fabricated process exit", row, e)
	}
	adopted, e := h.e.Start(ctx, role, StartOptions{})
	if e != nil || adopted.PID != started.PID {
		t.Fatal("native connection could not recover its actual process", adopted, e)
	}
}

func TestNativeCodexExecutionFinishesAndReplacementStartsItsOwnThread(t *testing.T) {
	role := "operator-native-codex"
	h, st, pid, rid, sid := setupNativeExitHarness(t, role)
	ctx := context.Background()
	t.Setenv(fakeCodexVar, "1")
	t.Setenv("COMMANDER_CODEX_BIN", os.Args[0])
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if e := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":{"access_token":"fake-native-test"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := WriteRuntime(h.home, role, RuntimeCodex); e != nil {
		t.Fatal(e)
	}
	spec, e := h.opts.Resolver.Resolve(ctx, role)
	if e != nil || spec.Runtime != RuntimeCodex || spec.ExecutionRunID != rid {
		t.Fatal("wrong native Codex binding", spec, e)
	}
	process, e := h.e.Start(ctx, role, StartOptions{})
	if e != nil || process.Runtime != RuntimeCodex {
		t.Fatal("native Codex did not start", process, e)
	}
	if e = h.e.Send(ctx, role, "Finish this bounded native Codex test turn"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "Codex host physically exited and committed its run", func() bool {
		run, e := st.GetAgentRun(ctx, rid)
		return e == nil && run.Status == "exited" && !alive(process.PID) && !h.e.Running(role)
	})
	thread, e := os.ReadFile(spec.codexThreadFile(h.home))
	if e != nil || len(thread) == 0 {
		t.Fatal("native Codex did not save its immutable-session thread", e)
	}
	if _, e = os.Stat(CodexThreadFile(h.home, role)); !os.IsNotExist(e) {
		t.Fatal("native execution wrote the shared role thread", e)
	}
	if len(h.codexRequests("thread/start")) != 1 || len(h.codexRequests("thread/resume")) != 0 {
		t.Fatal("fresh native run borrowed an old thread")
	}
	nextSID := agentrt.NewClaudeSessionID()
	next := admitNativeTestRun(t, st, pid, "assigned", role, nextSID, "")
	nextSpec, e := h.opts.Resolver.Resolve(ctx, role)
	if e != nil || nextSpec.ExecutionRunID != next || nextSpec.SessionID != nextSID || nextSpec.SessionID == sid || nextSpec.codexThreadFile(h.home) == spec.codexThreadFile(h.home) {
		t.Fatal("replacement lost immutable identity", nextSpec, e)
	}
	process, e = h.e.Start(ctx, role, StartOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.e.Send(ctx, role, "Finish the replacement Codex test turn"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "replacement Codex execution finished", func() bool {
		run, e := st.GetAgentRun(ctx, next)
		return e == nil && run.Status == "exited" && !alive(process.PID) && !h.e.Running(role)
	})
	if len(h.codexRequests("thread/start")) != 2 || len(h.codexRequests("thread/resume")) != 0 {
		t.Fatal("replacement resumed another execution's Codex thread")
	}
	var status string
	if e = st.DB().QueryRow(`SELECT status FROM bead_tasks WHERE project_id=? AND id='assigned'`, pid).Scan(&status); e != nil || status != "open" {
		t.Fatal("Codex lifecycle declared unfinished work complete", status, e)
	}
}
