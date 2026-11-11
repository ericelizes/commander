package store

import (
	"testing"
	"time"
)

func TestPostgresChangedExecutionPlansTellsARunOncePerRevision(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 8, now)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	rid, e := s.InsertAgentRun(ctx, AgentRun{ExecutionClaimToken: c.Token, ExecutionDriver: "native", ExecutionRuntime: "codex", ExecutionAccount: "default", ExecutionModel: "gpt-test", ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-redirect", OpencodeSessionID: "redirect", SpawnTime: now.Unix()})
	if e != nil {
		t.Fatal(e)
	}
	changes, e := s.ChangedExecutionPlans(ctx)
	if e != nil || len(changes) != 0 {
		t.Fatalf("unchanged plan reported: %+v %v", changes, e)
	}
	edit := func(text string) {
		t.Helper()
		if _, e := s.DB().ExecContext(ctx, `UPDATE bead_tasks SET description=? WHERE project_id=? AND id=?`, text, pid, c.BeadID); e != nil {
			t.Fatal(e)
		}
	}
	edit("Implement the revised change")
	changes, e = s.ChangedExecutionPlans(ctx)
	if e != nil || len(changes) != 1 || changes[0].RunID != rid || changes[0].Agent != "operator-redirect" || changes[0].CurrentRevision <= changes[0].ClaimedRevision || changes[0].Status != "open" {
		t.Fatalf("changed plan: %+v %v", changes, e)
	}
	if e = s.RecordExecutionRedirect(ctx, changes[0], "told"); e != nil {
		t.Fatal(e)
	}
	if changes, e = s.ChangedExecutionPlans(ctx); e != nil || len(changes) != 0 {
		t.Fatalf("told twice about one revision: %+v %v", changes, e)
	}
	edit("Implement the second revision")
	if changes, e = s.ChangedExecutionPlans(ctx); e != nil || len(changes) != 1 {
		t.Fatalf("a later revision was not reported: %+v %v", changes, e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	if changes, e = s.ChangedExecutionPlans(ctx); e != nil || len(changes) != 0 {
		t.Fatalf("an exited run was reported: %+v %v", changes, e)
	}
}

func TestPostgresHasClaimableExecutionIgnoresClaimedWork(t *testing.T) {
	s, _, ctx := executionFixture(t)
	if ok, e := s.HasClaimableExecution(ctx); e != nil || !ok {
		t.Fatalf("ready work not seen: %v %v", ok, e)
	}
	now := time.Now()
	for _, slot := range []string{"vm-0", "vm-1"} {
		if c, e := s.ClaimNextExecution(ctx, slot, 8, now); e != nil || c == nil {
			t.Fatal(e)
		}
	}
	if ok, e := s.HasClaimableExecution(ctx); e != nil || ok {
		t.Fatalf("claimed work reported claimable: %v %v", ok, e)
	}
}
