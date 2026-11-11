package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func jurisdictionFixture(t *testing.T) (*Store, int64, context.Context) {
	t.Helper()
	s := postgresTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	pid, e := s.InsertProject(ctx, Project{Name: "jurisdictions", Path: t.TempDir(), Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range []struct{ id, parent string }{{"root", ""}, {"urgent", "root"}, {"leaf", "urgent"}, {"nested", "urgent"}, {"nested-leaf", "nested"}, {"ordinary", "root"}} {
		if _, e = s.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,title,parent_id,labels,created_at,updated_at) VALUES(?,?,?,NULLIF(?,''),'["kind:objective"]','now','now')`, pid, b.id, b.id, b.parent); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"commander-urgent", "commander-specialist"} {
		if e = s.RegisterCommander(ctx, id, id); e != nil {
			t.Fatal(e)
		}
	}
	return s, pid, ctx
}

func TestPostgresJurisdictionInheritanceHandoffAndResultRouting(t *testing.T) {
	s, pid, ctx := jurisdictionFixture(t)
	move := func(bead, owner, next string, revision int64, inherit bool) {
		t.Helper()
		if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: bead, Actor: owner, ExpectedOwner: owner, NextCommander: next, ExpectedRevision: revision, Inherit: inherit, Reason: "urgent investigation"}); e != nil {
			t.Fatal(e)
		}
	}
	check := func(bead, owner string) {
		t.Helper()
		j, e := s.Jurisdiction(ctx, pid, bead)
		if e != nil || j.CommanderID != owner {
			t.Fatalf("%s ownership=%+v %v", bead, j, e)
		}
	}
	move("nested", "commander", "commander-specialist", 1, false)
	rid, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: "executor-1", OpencodeSessionID: "current", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||'"hold"'::jsonb WHERE project_id=? AND id='urgent'`, pid); e != nil {
		t.Fatal(e)
	}
	move("urgent", "commander", "commander-urgent", 2, false)
	check("leaf", "commander-urgent")
	check("nested-leaf", "commander-specialist")
	check("ordinary", "commander")
	var active int64
	var executor string
	if e = s.DB().QueryRowContext(ctx, `SELECT active_run_id,responsible_agent_id FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&active, &executor); e != nil || active != rid || executor != "executor-1" {
		t.Fatalf("handoff disturbed execution: %d %q %v", active, executor, e)
	}
	var held bool
	if e = s.DB().QueryRowContext(ctx, `SELECT jsonb_exists(labels,'hold') FROM bead_tasks WHERE project_id=? AND id='urgent'`, pid).Scan(&held); e != nil || !held {
		t.Fatal("handoff lost hold", e)
	}
	var accepted bool
	if e = s.DB().QueryRowContext(ctx, `SELECT commander_report_result(?,?,?,?,?)`, pid, "leaf", rid, "current", "Final report: done").Scan(&accepted); e != nil || !accepted {
		t.Fatal(e)
	}
	if e = s.DB().QueryRowContext(ctx, `SELECT responsible_agent_id FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&executor); e != nil || executor != "commander-urgent" {
		t.Fatalf("result routed to wrong owner: %s %v", executor, e)
	}
	// The old global owner cannot seize the delegated subtree.
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "leaf", Actor: "commander", ExpectedOwner: "commander-urgent", ExpectedRevision: 3, NextCommander: "commander", Reason: "unauthorized"}); e == nil {
		t.Fatal("non-owner stole jurisdiction")
	}
	move("urgent", "commander-urgent", "", 3, true)
	check("leaf", "commander")
	check("nested-leaf", "commander-specialist")
	var n int
	if e = s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM jurisdiction_history WHERE project_id=?`, pid).Scan(&n); e != nil || n != 3 {
		t.Fatalf("history=%d %v", n, e)
	}
}

func TestPostgresJurisdictionConcurrentTransferAndRollback(t *testing.T) {
	s, pid, ctx := jurisdictionFixture(t)
	transfer := JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "handoff"}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := s.TransferJurisdiction(ctx, transfer); results <- e }()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, ErrJurisdictionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	transfer.ExpectedRevision = 2
	transfer.ExpectedOwner = "commander-urgent"
	transfer.NextCommander = "commander-missing"
	if _, e := s.TransferJurisdiction(ctx, transfer); e == nil {
		t.Fatal("unregistered Commander accepted")
	}
	j, e := s.Jurisdiction(ctx, pid, "leaf")
	if e != nil || j.Revision != 2 || j.CommanderID != "commander-urgent" {
		t.Fatalf("failed transaction changed scope: %+v %v", j, e)
	}
	var n int
	if e = s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM jurisdiction_history WHERE project_id=?`, pid).Scan(&n); e != nil || n != 1 {
		t.Fatalf("failed transaction emitted history: %d %v", n, e)
	}
}

func TestPostgresJurisdictionReparentFencesOldRecovery(t *testing.T) {
	s, pid, ctx := jurisdictionFixture(t)
	if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "urgent"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "nested", ExpectedOwner: "commander-urgent", ExpectedRevision: 2, NextCommander: "commander-specialist", Reason: "independent scope"}); e != nil {
		t.Fatal(e)
	}
	jobs, e := s.LeaseSupervision(ctx, time.Now(), 128)
	if e != nil {
		t.Fatal(e)
	}
	var job SupervisionJob
	for _, j := range jobs {
		if j.BeadID == "leaf" {
			job = j
		}
	}
	before, e := s.SupervisionFact(ctx, job)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET parent_id='ordinary' WHERE project_id=? AND id='urgent'`, pid); e != nil {
		t.Fatal(e)
	}
	// Moving an explicitly owned scope preserves its owner; only the planning
	// revision changes. Removing its override then inherits its new parent.
	j, e := s.Jurisdiction(ctx, pid, "leaf")
	if e != nil || j.CommanderID != "commander-urgent" || j.Revision != 4 {
		t.Fatalf("scope moved incorrectly: %+v %v", j, e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "urgent", ExpectedOwner: "commander-urgent", ExpectedRevision: 4, Inherit: true, Reason: "return"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET parent_id='nested' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	after, e := s.SupervisionFact(ctx, job)
	if e != nil || after.AgentID != "commander-specialist" || after.Epoch <= before.Epoch {
		t.Fatalf("reparent left stale responsibility: %+v %v", after, e)
	}
	if e = s.FinishSupervision(ctx, *before, "work_pending", "delivered", "commander-urgent", "", time.Now().Add(time.Minute), true); e != nil {
		t.Fatal(e)
	}
	var attempts int
	if e = s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM supervision_attempts WHERE project_id=? AND bead_id='leaf'`, pid).Scan(&attempts); e != nil || attempts != 0 {
		t.Fatalf("old owner wrote recovery after move: %d %v", attempts, e)
	}
	j, e = s.Jurisdiction(ctx, pid, "nested-leaf")
	if e != nil || j.CommanderID != "commander-specialist" {
		t.Fatalf("nested boundary lost: %+v %v", j, e)
	}
}
