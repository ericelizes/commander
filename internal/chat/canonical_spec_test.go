package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func nativeClaimFixture(t *testing.T, home, role, kind string) (*store.Store, StoreResolver, int64, int64, string) {
	t.Helper()
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	dir := t.TempDir()
	pid, e := st.InsertProject(ctx, store.Project{Name: "native-claim", Type: kind, Path: dir, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'assigned','Canonical task','Read the exact current brief','Prove the requested outcome','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "native execution fixture"); e != nil {
		t.Fatal(e)
	}
	session := agentrt.NewClaudeSessionID()
	worktree := dir
	if kind == "code" {
		worktree = t.TempDir()
	}
	rid := admitNativeTestRun(t, st, pid, "assigned", role, session, worktree)
	return st, StoreResolver{Store: st, Home: home}, pid, rid, session
}
func TestCanonicalNativeExecutionResolvesClaimWorkspaceAndContract(t *testing.T) {
	for _, kind := range []string{"code", "knowledge"} {
		for _, runtime := range []string{RuntimeClaude, RuntimeCodex} {
			t.Run(kind+"/"+runtime, func(t *testing.T) {
				home := t.TempDir()
				role := "operator-canonical"
				st, r, pid, rid, sid := nativeClaimFixture(t, home, role, kind)
				if e := WriteRuntime(home, role, runtime); e != nil {
					t.Fatal(e)
				}
				spec, e := r.Resolve(context.Background(), role)
				if e != nil || spec.ExecutionRunID != rid || spec.SessionID != sid || spec.ExecutionBlocked || spec.Runtime != runtime || spec.Tier != agentrt.TierOperator {
					t.Fatal("wrong canonical spec", spec, e)
				}
				run, e := st.GetAgentRun(context.Background(), rid)
				if e != nil || spec.Cwd != run.WorktreePath.String {
					t.Fatal("workspace not from current claim", spec, e)
				}
				body, e := os.ReadFile(spec.PromptPath)
				if e != nil {
					t.Fatal(e)
				}
				for _, want := range []string{"Canonical task", "Read the exact current brief", "Prove the requested outcome", "assigned", "cmdr bead --project-dir"} {
					if !strings.Contains(string(body), want) {
						t.Fatal("execution contract missing current work", want)
					}
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET status='in_progress' WHERE project_id=? AND id='assigned'`, pid); e != nil {
					t.Fatal(e)
				}
				spec, e = r.Resolve(context.Background(), role)
				if e != nil || spec.ExecutionBlocked {
					t.Fatal("current in-progress execution could not resume", spec, e)
				}
				if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='assigned'`, pid); e != nil {
					t.Fatal(e)
				}
				spec, e = r.Resolve(context.Background(), role)
				if e != nil || !spec.ExecutionBlocked {
					t.Fatal("held execution could start fresh work", spec, e)
				}
				if e = st.MarkClosed(context.Background(), rid, "exited"); e != nil {
					t.Fatal(e)
				}
				if _, e = r.Resolve(context.Background(), role); e == nil {
					t.Fatal("terminal execution fell back to stale operator session files")
				}
			})
		}
	}
}
func TestNativeChatProcessStateAndEnvironmentRetainImmutableExecutionIdentity(t *testing.T) {
	var scenario struct{ Role string }
	rawScenario, e := os.ReadFile(filepath.Join("..", "..", "tests", "evals", "scenarios", "commander-native-execution.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(rawScenario, &scenario); e != nil || scenario.Role == "" {
		t.Fatal("missing native execution scenario", e)
	}
	h := newHarness(t)
	role := scenario.Role
	st, r, pid, rid, sid := nativeClaimFixture(t, h.home, role, "knowledge")
	h.opts.States = st
	h.opts.Resolver = r
	h.e = New(h.opts)
	t.Setenv("COMMANDER_RUN_ID", "ambient-wrong-run")
	t.Setenv("COMMANDER_RUN_SESSION", "ambient-wrong-session")
	t.Setenv("COMMANDER_MANAGER_ROLE", "ambient-manager")
	ctx := context.Background()
	spec, e := r.Resolve(ctx, role)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.e.Start(ctx, role, StartOptions{}); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "fake execution started", func() bool { _, e := os.Stat(filepath.Join(h.fakeDir, "argv-"+sid+".json")); return e == nil })
	record := h.fakeRecord("argv-" + sid)
	if record["run_id"] != strconv.FormatInt(rid, 10) || record["run_session"] != sid || record["role"] != role || record["manager_role"] != "" {
		t.Fatal("native child inherited the wrong execution context", record)
	}
	c, _ := h.e.chat(role)
	c.mu.Lock()
	oldProcess := c.proc
	c.mu.Unlock()
	if e = h.e.Send(ctx, role, "tool"); e != nil {
		t.Fatal(e)
	}
	waitUntil(t, "run-aware state recorded", func() bool {
		states, e := st.ListAgentStates(ctx)
		return e == nil && len(states) > 0 && states[0].Role == role
	})
	if e = st.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	nextSID := agentrt.NewClaudeSessionID()
	next := admitNativeTestRun(t, st, pid, "assigned", role, nextSID, "")
	c.writeStateFor(State{State: store.AgentWorking, Detail: "late old callback"}, spec)
	states, e := st.ListAgentStates(ctx)
	if e != nil || len(states) == 0 || states[0].State != "unknown" {
		t.Fatal("old native process changed new claim state", states, e)
	}
	if e = h.e.Stop(ctx, role); e != nil {
		t.Fatal(e)
	}
	if _, e = h.e.Start(ctx, role, StartOptions{}); e != nil {
		t.Fatal(e)
	}
	if c.publishFrom(oldProcess, Event{Kind: KindExit, Source: SourceEngine}) {
		t.Fatal("old process published into the replacement chat")
	}
	current, e := r.Resolve(ctx, role)
	if e != nil || current.ExecutionRunID != next || current.SessionID != nextSID {
		t.Fatal(current, e)
	}
	before, e := h.e.Status(role)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='assigned'`, pid); e != nil {
		t.Fatal(e)
	}
	h.e.Close()
	h.e = New(h.opts)
	adopted, e := h.e.Start(ctx, role, StartOptions{})
	if e != nil || adopted.PID != before.PID {
		t.Fatal("host restart lost its immutable claim identity", adopted, e)
	}
	nextSpec, e := r.Resolve(ctx, role)
	if e != nil || nextSpec.codexThreadFile(h.home) == spec.codexThreadFile(h.home) {
		t.Fatal("replacement claim reused the old Codex thread", e)
	}

}

func TestCanonicalNativeImportedPlannerUsesPlanningContractAndRetiredScopeCannotRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	t.Setenv("COMMANDER_WORKSPACE_ROOT", t.TempDir())
	st, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, store.Project{Name: "imported-planner", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'scope','Scope','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	role := "manager-imported"
	sessionFile := filepath.Join(home, "retained-manager-session")
	sid := agentrt.NewClaudeSessionID()
	if e = os.WriteFile(sessionFile, []byte(sid), 0600); e != nil {
		t.Fatal(e)
	}
	if e = st.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: "scope", AmRole: role, SessionFile: sessionFile}); e != nil {
		t.Fatal(e)
	}
	if e = st.WithTx(ctx, func(q store.Querier) error { return store.SeedImportedJurisdictions(ctx, q, pid) }); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "logical planner test"); e != nil {
		t.Fatal(e)
	}
	r := StoreResolver{Store: st, Home: home}
	spec, e := r.Resolve(ctx, role)
	if e != nil || spec.SessionID != sid || spec.ExecutionRunID != 0 || spec.Tier != agentrt.TierCommander {
		t.Fatal(spec, e)
	}
	body, e := os.ReadFile(spec.PromptPath)
	if e != nil || !strings.Contains(string(body), "do not delegate tree management") {
		t.Fatal("imported identity retained Manager orchestration", string(body), e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 2, "shadow", "consolidate imported ownership"); e != nil {
		t.Fatal(e)
	}
	inventory, e := st.LegacyPlanningInventory(ctx, pid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.ConsolidateLegacyPlanning(ctx, "commander", store.JurisdictionConsolidation{ProjectID: pid, Revision: inventory.Revision, ControlRevision: inventory.ControlRevision, DefaultCommander: inventory.DefaultCommander, Snapshot: inventory.Snapshot, RequestID: "retire-native-planner", Reason: "Return imported scope to Commander"}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 3, "active", "retirement restart test"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Resolve(ctx, role); e == nil {
		t.Fatal("retired planning host could restart from its legacy registration")
	}
	raw, e := os.ReadFile(sessionFile)
	if e != nil || string(raw) != sid {
		t.Fatal("retirement erased conversation session", e)
	}
}

func TestCanonicalNativeExecutionBlocksFreshHeldProcessAndAmbiguousIdentity(t *testing.T) {
	h := newHarness(t)
	role := "operator-held-native"
	st, r, pid, _, _ := nativeClaimFixture(t, h.home, role, "knowledge")
	h.opts.States = st
	h.opts.Resolver = r
	h.e = New(h.opts)
	if _, e := st.DB().Exec(`UPDATE bead_tasks SET labels='["hold"]' WHERE project_id=? AND id='assigned'`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e := h.e.Start(context.Background(), role, StartOptions{}); e == nil {
		t.Fatal("native model started fresh work under a hold")
	}
	if h.e.Running(role) {
		t.Fatal("held native process was launched")
	}
	if _, e := st.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'other','Other','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	admitNativeTestRun(t, st, pid, "other", role, agentrt.NewClaudeSessionID(), "")
	if _, e := r.Resolve(context.Background(), role); e == nil || !strings.Contains(e.Error(), "multiple live execution claims") {
		t.Fatal("native resolver guessed between claims", e)
	}
}

func TestNativeExecutionRechecksClaimBeforeLaunchingAProcess(t *testing.T) {
	h := newHarness(t)
	role := "operator-recheck"
	st, r, pid, rid, _ := nativeClaimFixture(t, h.home, role, "knowledge")
	calls := 0
	h.opts.States = st
	h.opts.Resolver = ResolverFunc(func(ctx context.Context, requested string) (Spec, error) {
		calls++
		if calls == 2 {
			if e := st.MarkClosed(ctx, rid, "exited"); e != nil {
				return Spec{}, e
			}
			admitNativeTestRun(t, st, pid, "assigned", role, agentrt.NewClaudeSessionID(), "")
		}
		return r.Resolve(ctx, requested)
	})
	h.e = New(h.opts)
	if _, e := h.e.Start(context.Background(), role, StartOptions{}); e == nil || !strings.Contains(e.Error(), "custody or authority changed") {
		t.Fatal("native start used an obsolete claim", calls, e)
	}
	if h.e.Running(role) {
		t.Fatal("obsolete claim launched a model")
	}
}

func TestNativeExecutionRejectsDifferentOrUnstampedHostWithoutKillingIt(t *testing.T) {
	for _, kind := range []string{"different-session", "unstamped"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			role := "operator-protected-host"
			oldSID := h.role(role)
			old, e := h.e.Start(context.Background(), role, StartOptions{})
			if e != nil {
				t.Fatal(e)
			}
			original := h.opts.Resolver
			h.e.Close()
			h.opts.Resolver = ResolverFunc(func(ctx context.Context, role string) (Spec, error) {
				spec, e := original.Resolve(ctx, role)
				spec.ExecutionRunID = 99
				if kind == "different-session" {
					spec.SessionID = agentrt.NewClaudeSessionID()
				}
				return spec, e
			})
			h.e = New(h.opts)
			if _, e = h.e.Start(context.Background(), role, StartOptions{}); e == nil {
				t.Fatal("unproven host was accepted as current execution", kind)
			}
			if !alive(old.PID) {
				t.Fatal("native custody check killed a different/unproven process", kind)
			}
			// Restore the original private harness binding solely to stop its fake host.
			// Its PID/session and model history survived the rejected execution start.
			h.e.Close()
			h.opts.Resolver = original
			h.e = New(h.opts)
			restored, e := h.e.Start(context.Background(), role, StartOptions{})
			if e != nil || restored.PID != old.PID || restored.SessionID != oldSID {
				t.Fatal("original fake host was not preserved", restored, e)
			}
		})
	}
}

// New native test runs take the same ready/capability/capacity path as the
// scheduler. Imported legacy records are tested separately, before activation.
func admitNativeTestRun(t *testing.T, st *store.Store, pid int64, bead, role, sid, workspace string) int64 {
	t.Helper()
	ctx := context.Background()
	if _, e := st.DB().Exec(`UPDATE bead_tasks SET description=CASE WHEN description='' THEN 'Fixture brief' ELSE description END,acceptance_criteria=CASE WHEN acceptance_criteria='' THEN 'Fixture evidence' ELSE acceptance_criteria END WHERE project_id=? AND id=?`, pid, bead); e != nil {
		t.Fatal(e)
	}
	var revision, jrev int64
	var owner, kind string
	if e := st.DB().QueryRow(`SELECT b.plan_revision,COALESCE(j.revision,1),commander_planning_owner(b.project_id,b.id),p.type FROM bead_tasks b JOIN projects p ON p.id=b.project_id LEFT JOIN jurisdiction_projects j ON j.project_id=b.project_id WHERE b.project_id=? AND b.id=?`, pid, bead).Scan(&revision, &jrev, &owner, &kind); e != nil {
		t.Fatal(e)
	}
	if _, e := st.SetExecutionReady(ctx, pid, bead, owner, revision, jrev, true, nil); e != nil {
		t.Fatal(e)
	}
	if e := st.ReapExecutionStartupClaims(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	slot := "native-fixture-" + sid
	if e := st.ConfigureExecutionSlot(ctx, slot, []string{kind}, true); e != nil {
		t.Fatal(e)
	}
	claim, e := st.ClaimNextExecution(ctx, slot, 8, time.Now())
	if e != nil || claim == nil || claim.BeadID != bead {
		t.Fatal("native fixture did not reserve its intended bead", claim, e)
	}
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: bead, AgentName: role, OpencodeSessionID: sid, SpawnTime: time.Now().Unix(), WorktreePath: sql.NullString{String: workspace, Valid: workspace != ""}})
	if e != nil {
		t.Fatal(e)
	}
	return rid
}
