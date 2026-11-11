package beads

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func TestPostgresActiveExecutionAgentCanReportButCannotPlanThroughLegacyDoors(t *testing.T) {
	c, dir, st := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	bead, e := c.Create(ctx, dir, "Assigned task", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	other, e := c.Create(ctx, dir, "Other task", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var pid int64
	if e = st.DB().QueryRowContext(ctx, `SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	// Seed existing execution before activation; imported custody must retain reporting rights.
	rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: bead, AgentName: "executor-generic", OpencodeSessionID: "current-session", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.SetOrchestrationControl(ctx, 1, "active", "executor authority test"); e != nil {
		t.Fatal(e)
	}
	executor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=executor-generic", "COMMANDER_RUN_ID=" + strconv.FormatInt(rid, 10), "COMMANDER_RUN_SESSION=current-session"})
	for _, op := range [][]string{
		{"create", "Unauthorized child", "--parent", bead},
		{"update", bead, "--title", "Different goal"},
		{"update", bead, "--description", "Different brief"},
		{"update", bead, "--acceptance", "Lower standard"},
		{"update", bead, "--priority", "0"},
		{"update", bead, "--assignee", "other-agent"},
		{"update", bead, "--parent", other},
		{"dep", "add", bead, other},
		{"close", bead}, {"delete", bead},
		{"update", bead, "--remove-label", "hold"},
		{"set-state", bead, "cmdr=done"},
		{"set-state", other, "cmdr=executing"},
		{"comments", "add", other, "Foreign evidence"},
	} {
		if _, _, e = postgresRun(executor, dir, op); e == nil {
			t.Fatalf("executor bypassed ownership through %v", op)
		}
	}
	for _, op := range [][]string{{"set-state", bead, "cmdr=executing"}, {"update", bead, "--status", "in_progress"}, {"comments", "add", bead, "Current run observed a verified outcome"}} {
		if _, _, e = postgresRun(executor, dir, op); e != nil {
			t.Fatalf("current execution evidence rejected: %v %v", op, e)
		}
	}
	stale := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=executor-generic", "COMMANDER_RUN_ID=" + strconv.FormatInt(rid, 10), "COMMANDER_RUN_SESSION=old-session"})
	if _, _, e = postgresRun(stale, dir, []string{"comments", "add", bead, "Old evidence"}); e == nil {
		t.Fatal("old session contaminated current evidence")
	}
	if e = st.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = postgresRun(executor, dir, []string{"comments", "add", bead, "Late old execution evidence"}); e == nil {
		t.Fatal("terminal execution contaminated current evidence")
	}
	planner := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=commander"})
	if _, _, e = postgresRun(planner, dir, []string{"update", bead, "--title", "Commander revised goal"}); e != nil {
		t.Fatal("current planning owner lost planning authority", e)
	}
}

func TestPostgresRegisteredPlanningIdentityExecutionContextCannotBorrowAuthority(t *testing.T) {
	for _, role := range []string{"commander", "commander-urgent", "manager-imported", "manager.imported"} {
		t.Run(role, func(t *testing.T) {
			c, dir, st := postgresBeads(t)
			ctx := context.Background()
			if e := c.Init(ctx, dir); e != nil {
				t.Fatal(e)
			}
			bead, e := c.Create(ctx, dir, "Assigned work", CreateOptions{Silent: true})
			if e != nil {
				t.Fatal(e)
			}
			other, e := c.Create(ctx, dir, "Other owned scope", CreateOptions{Silent: true})
			if e != nil {
				t.Fatal(e)
			}
			var pid int64
			if e = st.DB().QueryRow(`SELECT id FROM projects WHERE path=?`, dir).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if strings.HasPrefix(role, "manager") {
				if _, e = st.DB().Exec(`INSERT INTO commander_sessions(agent_id,title) VALUES(?,?)`, role, "Imported planning conversation"); e != nil {
					t.Fatal(e)
				}
			} else if e = st.RegisterCommander(ctx, role, role); e != nil {
				t.Fatal(e)
			}
			for i, id := range []string{bead, other} {
				if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: id, ExpectedOwner: "commander", ExpectedRevision: int64(i + 1), NextCommander: role, Reason: "own both planning scopes"}); e != nil {
					t.Fatal(e)
				}
			}
			// Seed existing execution before activation; imported custody must retain reporting rights.
			rid, e := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: bead, AgentName: role, OpencodeSessionID: "actual-execution-session", SpawnTime: time.Now().Unix()})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = st.SetOrchestrationControl(ctx, 1, "active", "execution context fences planning registration"); e != nil {
				t.Fatal(e)
			}
			executor := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + strings.ReplaceAll(role, ".", "-"), "COMMANDER_RUN_ID=" + strconv.FormatInt(rid, 10), "COMMANDER_RUN_SESSION=actual-execution-session"})
			for _, op := range [][]string{{"create", "Unauthorized child", "--parent", bead}, {"update", bead, "--title", "Different goal"}, {"update", bead, "--remove-label", "hold"}, {"dep", "add", bead, other}, {"close", bead}, {"set-state", other, "cmdr=working"}, {"comments", "add", other, "Borrowed planning identity evidence"}} {
				if _, _, e = postgresRun(executor, dir, op); e == nil {
					t.Fatal("execution borrowed planning authority", role, op)
				}
			}
			for _, op := range [][]string{{"set-state", bead, "cmdr=executing"}, {"update", bead, "--status", "in_progress"}, {"comments", "add", bead, "Current execution evidence"}} {
				if _, _, e = postgresRun(executor, dir, op); e != nil {
					t.Fatal("registered identity could not report its actual work", role, op, e)
				}
			}
			current, e := c.Show(ctx, dir, bead)
			if e != nil {
				t.Fatal(e)
			}
			basis := PlanBasis{RequestID: "execution-cannot-plan", JurisdictionRevision: current.JurisdictionRevision, Beads: map[string]int64{bead: current.PlanRevision}}
			ops := [][]string{{"comments", "add", bead, "Evidence disguised as a versioned plan"}}
			if r := postgresApplyOps(WithPlanRequest(executor, &basis, ops, nil), dir, ops); r.Exit == 0 {
				t.Fatal("execution submitted a versioned planning batch")
			}
			if _, e = ProposePlan(WithPlanRequest(executor, &basis, ops, nil), dir, ops, nil); e == nil {
				t.Fatal("execution proposed a planning change")
			}
			for _, env := range [][]string{
				{"COMMANDER_ROLE=" + role, "COMMANDER_RUN_ID=" + strconv.FormatInt(rid, 10), "COMMANDER_RUN_SESSION=old-session"},
				{"COMMANDER_ROLE=" + role, "COMMANDER_RUN_ID=not-a-run", "COMMANDER_RUN_SESSION=actual-execution-session"},
				{"COMMANDER_ROLE=" + role, "COMMANDER_MANAGER_ROLE=" + role},
				{"COMMANDER_RUN_SESSION=actual-execution-session"},
			} {
				if _, _, e = postgresRun(WithDoorEnv(ctx, env), dir, []string{"update", bead, "--title", "Cannot masquerade as planner or user"}); e == nil {
					t.Fatal("partial/stale attribution borrowed planning authority", env)
				}
			}
			if e = st.RegisterCommander(ctx, "commander-next", "Next"); e != nil {
				t.Fatal(e)
			}
			if _, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: bead, ExpectedOwner: role, ExpectedRevision: current.JurisdictionRevision, NextCommander: "commander-next", Reason: "planning handoff while execution continues"}); e != nil {
				t.Fatal(e)
			}
			if _, _, e = postgresRun(executor, dir, []string{"comments", "add", bead, "Execution retained custody after planning handoff"}); e != nil {
				t.Fatal("handoff silenced a current execution", role, e)
			}
			logical := WithDoorEnv(ctx, []string{"COMMANDER_ROLE=" + role})
			if _, _, e = postgresRun(logical, dir, []string{"update", other, "--title", "Logical planning conversation still owns this scope"}); e != nil {
				t.Fatal("logical planning context lost authority", role, e)
			}
			if e = st.MarkClosed(ctx, rid, "exited"); e != nil {
				t.Fatal(e)
			}
			if _, _, e = postgresRun(executor, dir, []string{"set-state", bead, "cmdr=working"}); e == nil {
				t.Fatal("terminal registered execution changed current phase")
			}
		})
	}
}

func TestExecutionAttributionSurvivesDoorFilteringAndExplicitContextsDoNotInherit(t *testing.T) {
	t.Setenv("COMMANDER_RUN_ID", "ambient-run")
	t.Setenv("COMMANDER_RUN_SESSION", "ambient-session")
	t.Setenv("COMMANDER_MANAGER_ROLE", "ambient-manager")
	parent := WithDoorEnv(context.Background(), []string{"COMMANDER_ROLE=commander", "COMMANDER_MANAGER_ROLE=manager-imported"})
	if !ExecutionAttributed(parent) {
		t.Fatal("write door dropped inherited execution attribution")
	}
	for _, env := range [][]string{nil, {"COMMANDER_ROLE=commander"}, {"PATH=/ignored"}} {
		if ExecutionAttributed(WithDoorEnv(parent, env)) {
			t.Fatal("explicit service attribution inherited a different execution context", env)
		}
	}
}
