package store

import (
	"testing"
	"time"
)

// Regression adapted from independent reviewer operator-exec-vm-10-b5bc5c74d358.
// Pause run binding at the graph lock and exercise actual readiness concurrently.
func TestReviewPlanningClaimLockOrder(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	c, e := s.ClaimNextExecution(ctx, "vm-0", 2, time.Now())
	if e != nil || c == nil {
		t.Fatal(c, e)
	}
	// Claiming holds the new qualified graph lock before the per-project lock.
	tx, e := s.DB().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031823)`); e != nil {
		t.Fatal(e)
	}
	var rev int64
	if e = s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='ordinary'`, pid).Scan(&rev); e != nil {
		t.Fatal(e)
	}
	ready := make(chan error, 1)
	go func() {
		_, err := s.SetExecutionReady(ctx, pid, "ordinary", "commander", rev, 1, false, nil)
		ready <- err
	}()
	// Wait until readiness requests graph custody. It must not yet hold the
	// project lock or a bead row; actual claimRun must still complete.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		e = s.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=734031823 AND NOT granted)`).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness did not reach graph lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, claimErr := claimRun(ctx, tx, AgentRun{ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-claimed", ExecutionClaimToken: c.Token, Status: "running", SpawnTime: time.Now().Unix()})
	tx.Rollback()
	var readyErr error
	select {
	case readyErr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness did not return")
	}
	t.Logf("claim error: %v; readiness error: %v", claimErr, readyErr)
	if claimErr != nil || readyErr != nil {
		t.Fatalf("claim=%v readiness=%v", claimErr, readyErr)
	}
}
