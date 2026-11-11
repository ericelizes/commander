package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func executionFixture(t *testing.T) (*Store, int64, context.Context) {
	t.Helper()
	s, pid, ctx := jurisdictionFixture(t)
	if _, e := s.SetOrchestrationControl(ctx, 1, "active", "execution fixture"); e != nil {
		t.Fatal(e)
	}
	for _, bead := range []string{"leaf", "ordinary"} {
		if _, e := s.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='Implement the bounded change',acceptance_criteria='Recorded checks pass' WHERE project_id=? AND id=?`, pid, bead); e != nil {
			t.Fatal(e)
		}
		var revision int64
		if e := s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, bead).Scan(&revision); e != nil {
			t.Fatal(e)
		}
		if _, e := s.SetExecutionReady(ctx, pid, bead, "commander", revision, 1, true, nil); e != nil {
			t.Fatal(e)
		}
	}
	for _, slot := range []string{"vm-0", "vm-1"} {
		if e := s.ConfigureExecutionSlot(ctx, slot, []string{"code", "knowledge"}, true); e != nil {
			t.Fatal(e)
		}
	}
	return s, pid, ctx
}

func TestPostgresExecutionClaimsAreAtomicAndCapacityBounded(t *testing.T) {
	s, _, ctx := executionFixture(t)
	now := time.Now()
	start := make(chan struct{})
	claims := make(chan *ExecutionClaim, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, agent := range []string{"vm-0", "vm-1"} {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			<-start
			c, e := s.ClaimNextExecution(ctx, agent, 1, now)
			claims <- c
			errs <- e
		}(agent)
	}
	close(start)
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	var first *ExecutionClaim
	for c := range claims {
		if c != nil {
			count++
			first = c
		}
	}
	if count != 1 {
		t.Fatalf("capacity admitted %d claims", count)
	}
	other := "vm-0"
	if first.AgentID == other {
		other = "vm-1"
	}
	second, e := s.ClaimNextExecution(ctx, other, 2, now)
	if e != nil || second == nil || second.BeadID == first.BeadID {
		t.Fatalf("duplicate or missing independent claim: %+v %v", second, e)
	}
}

func TestPostgresExecutionRespectsCapabilitiesHoldsWaitsAndPriority(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	// The user's workstream rank takes precedence over leaf creation order.
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO bead_ranks(project_id,bead_id,rank) VALUES(?,'ordinary',1),(?,'urgent',2)`, pid, pid); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil || c.BeadID != "ordinary" {
		t.Fatalf("priority order ignored: %+v %v", c, e)
	}
	if e = s.FailExecutionClaim(ctx, *c, "fixture release", now); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET execution_capabilities='["mac"]' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	if c, e = s.ClaimNextExecution(ctx, "vm-0", 8, now); e != nil || c != nil {
		t.Fatalf("claimed unsupported capability: %+v %v", c, e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET execution_capabilities='[]' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	for _, label := range []string{"hold", "waiting:1:user", "waiting:1:ci:42", "w:1:un:2:external_20review"} {
		if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=jsonb_build_array(?::text) WHERE project_id=? AND id='urgent'`, label, pid); e != nil {
			t.Fatal(e)
		}
		if c, e = s.ClaimNextExecution(ctx, "vm-0", 8, now); e != nil || c != nil {
			t.Fatalf("claim bypassed %s: %+v %v", label, c, e)
		}
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels='["waiting:1:until:2:Manager_20to_20start"]' WHERE project_id=? AND id='urgent'`, pid); e != nil {
		t.Fatal(e)
	}
	if c, e = s.ClaimNextExecution(ctx, "vm-0", 8, now); e != nil || c == nil || c.BeadID != "leaf" {
		t.Fatalf("legacy custody blocked ready execution: %+v %v", c, e)
	}
}

func TestPostgresExecutionLaunchIsFencedAndSlotsSurviveRestart(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent handover"}); e != nil {
		t.Fatal(e)
	}
	r := AgentRun{ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-generic", OpencodeSessionID: "fixture", SpawnTime: now.Unix(), ExecutionClaimToken: c.Token}
	if _, e = s.InsertAgentRun(ctx, r); e == nil {
		t.Fatal("stale startup claim launched after handoff")
	}
	if e = s.ReapExecutionStartupClaims(ctx, now.Add(6*time.Minute)); e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimNextExecution(ctx, "vm-0", 8, now.Add(6*time.Minute))
	if e != nil || c == nil {
		t.Fatal("expired unbound claim was not recovered", e)
	}
	r.TaskBeadID = c.BeadID
	r.ExecutionClaimToken = c.Token
	rid, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if c2, e := s.ClaimNextExecution(ctx, "vm-0", 8, now.Add(24*time.Hour)); e != nil || c2 != nil {
		t.Fatalf("live run lost slot on timeout: %+v %v", c2, e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimNextExecution(ctx, "vm-0", 8, now.Add(24*time.Hour))
	if e != nil || next == nil || next.BeadID == c.BeadID {
		t.Fatalf("completed slot did not become available: %+v %v", next, e)
	}
}

func TestPostgresExecutionDependenciesAndReadinessAuthority(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'leaf','ordinary','blocks')`, pid); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil || c.BeadID != "ordinary" {
		t.Fatalf("dependency ignored: %+v %v", c, e)
	}
	var revision int64
	if e = s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetExecutionReady(ctx, pid, "leaf", "operator-other", revision, 1, false, nil); e == nil {
		t.Fatal("executor changed planning readiness")
	}
	if _, e = s.SetExecutionReady(ctx, pid, "leaf", "commander", revision-1, 1, false, nil); e == nil {
		t.Fatal("stale plan edit changed readiness")
	}
	// A parent waiting on its own child must not block that child from doing
	// the work needed to discharge the parent dependency.
	if _, e = s.DB().ExecContext(ctx, `DELETE FROM bead_dependencies WHERE project_id=?`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'root','leaf','blocks')`, pid); e != nil {
		t.Fatal(e)
	}
	if c, e = s.ClaimNextExecution(ctx, "vm-1", 8, now); e != nil || c == nil || c.BeadID != "leaf" {
		t.Fatalf("parent blocked its own required work: %+v %v", c, e)
	}
}

func TestPostgresExecutionClaimHeartbeatDoesNotPublishProgress(t *testing.T) {
	s, _, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	var before, after int64
	if e = s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM state_events`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = s.RenewExecutionClaim(ctx, *c, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM state_events`).Scan(&after); e != nil || after != before {
		t.Fatal(fmt.Sprintf("heartbeat published state/progress events %d -> %d", before, after), e)
	}
}

func TestPostgresExecutionLaunchRevalidatesEligibilityAfterReservation(t *testing.T) {
	for _, change := range []string{"new-child", "inactive-project", "disabled-slot", "changed-capabilities"} {
		t.Run(change, func(t *testing.T) {
			s, pid, ctx := executionFixture(t)
			c, e := s.ClaimNextExecution(ctx, "vm-0", 8, time.Now())
			if e != nil || c == nil {
				t.Fatal(e)
			}
			switch change {
			case "new-child":
				_, e = s.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,parent_id,created_at,updated_at) VALUES(?,'new-child','New child',?,'now','now')`, pid, c.BeadID)
			case "inactive-project":
				_, e = s.DB().ExecContext(ctx, `UPDATE projects SET status='inactive' WHERE id=?`, pid)
			case "disabled-slot":
				e = s.ConfigureExecutionSlot(ctx, "vm-0", []string{"code"}, false)
			case "changed-capabilities":
				e = s.ConfigureExecutionSlot(ctx, "vm-0", []string{"knowledge"}, true)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: c.Token, ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-late", OpencodeSessionID: "late", SpawnTime: time.Now().Unix()}); e == nil {
				t.Fatalf("launch bypassed %s", change)
			}
		})
	}
}

func TestPostgresExecutionReReadyDoesNotDependOnOldSlotBeingEnabled(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	rid, e := s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: c.Token, ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-disabled-slot", OpencodeSessionID: "fixture", SpawnTime: now.Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ConfigureExecutionSlot(ctx, "vm-0", []string{"code"}, false); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	var revision int64
	if e = s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?`, pid, c.BeadID).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetExecutionReady(ctx, pid, c.BeadID, "commander", revision, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.ReapExecutionStartupClaims(ctx, now); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimNextExecution(ctx, "vm-1", 8, now)
	if e != nil || next == nil || next.BeadID != c.BeadID {
		t.Fatalf("disabled slot stranded ready work: %+v %v", next, e)
	}
}

func TestPostgresExecutionWorkstreamCapacityCountsStartupReservations(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	if _, e := s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||'"max-operators:1"'::jsonb WHERE project_id=? AND id='root'`, pid); e != nil {
		t.Fatal(e)
	}
	first, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || first == nil {
		t.Fatal(e)
	}
	if second, e := s.ClaimNextExecution(ctx, "vm-1", 8, now); e != nil || second != nil {
		t.Fatalf("reserved work did not consume subtree capacity: %+v %v", second, e)
	}
	if _, e = s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: first.Token, ProjectID: pid, TaskBeadID: first.BeadID, AgentName: "operator-cap", OpencodeSessionID: "cap", SpawnTime: now.Unix()}); e != nil {
		t.Fatalf("launch counted its own reservation twice: %v", e)
	}
}

func TestPostgresExecutionDisabledModeStopsClaimsAndReservedLaunch(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	if _, e = s.SetOrchestrationControl(ctx, 2, "off", "disable execution"); e != nil {
		t.Fatal(e)
	}
	if next, e := s.ClaimNextExecution(ctx, "vm-1", 8, now); e != nil || next != nil {
		t.Fatalf("disabled mode still claimed work: %+v %v", next, e)
	}
	if _, e = s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: c.Token, ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-disabled-mode", OpencodeSessionID: "disabled", SpawnTime: now.Unix()}); e == nil {
		t.Fatal("reserved launch bypassed disabled execution mode")
	}
}

func TestPostgresExecutionNestedDefaultCapsAreIndependentInEitherOrder(t *testing.T) {
	for _, nestedFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(nestedFirst), func(t *testing.T) {
			s, pid, ctx := executionFixture(t)
			now := time.Now()
			for _, stmt := range []string{
				`UPDATE bead_tasks SET execution_ready=FALSE WHERE project_id=? AND id='ordinary'`,
				`UPDATE bead_tasks SET labels='["kind:workstream","max-operators:10"]' WHERE project_id=? AND id='root'`,
				`UPDATE bead_tasks SET labels='["kind:workstream"]' WHERE project_id=? AND id IN('urgent','nested')`,
				`UPDATE bead_tasks SET description='Nested work',acceptance_criteria='Evidence',execution_ready=TRUE WHERE project_id=? AND id='nested-leaf'`,
			} {
				if _, e := s.DB().ExecContext(ctx, stmt, pid); e != nil {
					t.Fatal(e)
				}
			}
			if nestedFirst {
				if _, e := s.DB().ExecContext(ctx, `INSERT INTO bead_ranks(project_id,bead_id,rank) VALUES(?,'nested',1),(?,'leaf',2)`, pid, pid); e != nil {
					t.Fatal(e)
				}
			}
			first, e := s.ClaimNextExecution(ctx, "vm-0", 8, now, 1)
			if e != nil || first == nil {
				t.Fatal(e)
			}
			expected := "leaf"
			if nestedFirst {
				expected = "nested-leaf"
			}
			if first.BeadID != expected {
				t.Fatalf("first=%s want=%s", first.BeadID, expected)
			}
			second, e := s.ClaimNextExecution(ctx, "vm-1", 8, now, 1)
			if e != nil || second == nil || second.BeadID == first.BeadID {
				t.Fatalf("nested default cap depends on claim order: first=%s next=%+v %v", first.BeadID, second, e)
			}
		})
	}
}

func TestPostgresExecutionRetirementBlocksReservationAndPendingLaunch(t *testing.T) {
	for _, label := range []string{"superseded", "retired"} {
		t.Run(label, func(t *testing.T) {
			s, pid, ctx := executionFixture(t)
			now := time.Now()
			c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
			if e != nil || c == nil {
				t.Fatal(e)
			}
			if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||jsonb_build_array(?::text) WHERE project_id=? AND id='root'`, label, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: c.Token, ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-retired", OpencodeSessionID: "retired", SpawnTime: now.Unix()}); e == nil {
				t.Fatal("ancestor retirement did not fence reserved launch")
			}
			if next, e := s.ClaimNextExecution(ctx, "vm-1", 8, now); e != nil || next != nil {
				t.Fatalf("retired ancestry still reserves: %+v %v", next, e)
			}
			if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels='[]' WHERE project_id=? AND id='root'`, pid); e != nil {
				t.Fatal(e)
			}
			if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||jsonb_build_array(?::text) WHERE project_id=? AND execution_ready`, label, pid); e != nil {
				t.Fatal(e)
			}
			if next, e := s.ClaimNextExecution(ctx, "vm-1", 8, now); e != nil || next != nil {
				t.Fatalf("retired bead still reserves: %+v %v", next, e)
			}
		})
	}
}
