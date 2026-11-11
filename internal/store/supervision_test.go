package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresSupervisionResultTransferAndFencing(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "supervision", Path: "/supervision", Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "task"} {
		_, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,labels,parent_id,created_at,updated_at) VALUES(?,?,?,'["kind:objective"]',NULLIF(?,''),'now','now')`, pid, id, id, map[string]string{"task": "root"}[id])
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = s.RegisterCommander(ctx, "commander-root", "Fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-root", Reason: "fixture"}); e != nil {
		t.Fatal(e)
	}
	var owner string
	if e = s.DB().QueryRow(`SELECT responsible_agent_id FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&owner); e != nil || owner != "commander-root" {
		t.Fatalf("responsibility=%q %v", owner, e)
	}
	r := AgentRun{ProjectID: pid, TaskBeadID: "task", AgentName: "operator-task", OpencodeSessionID: "session", SpawnTime: time.Now().Unix()}
	rid, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	var accepted bool
	if e = s.DB().QueryRow(`SELECT commander_report_result(?,?,?,?,?)`, pid, "task", rid, "wrong", "Final report").Scan(&accepted); e != nil || accepted {
		t.Fatalf("foreign session accepted=%v %v", accepted, e)
	}
	if e = s.DB().QueryRow(`SELECT commander_report_result(?,?,?,?,?)`, pid, "task", rid, "session", "Final report: checks passed").Scan(&accepted); e != nil || !accepted {
		t.Fatal(e)
	}
	if e = s.DB().QueryRow(`SELECT responsible_agent_id FROM bead_tasks WHERE project_id=? AND id='task'`, pid).Scan(&owner); e != nil || owner != "commander-root" {
		t.Fatalf("result lost reviewer responsibility: %q %v", owner, e)
	}
	jobs, e := s.LeaseSupervision(ctx, time.Now(), 128)
	if e != nil || len(jobs) == 0 {
		t.Fatalf("jobs=%d %v", len(jobs), e)
	}
	var j SupervisionJob
	for _, x := range jobs {
		if x.BeadID == "task" {
			j = x
		}
	}
	fact, e := s.SupervisionFact(ctx, j)
	if e != nil || !fact.ResultReady {
		t.Fatalf("result facts=%#v %v", fact, e)
	}
	prepared, ok, e := s.PrepareSupervisionDelivery(ctx, *fact, "result_pending", "commander-root")
	if e != nil || !ok {
		t.Fatal(e)
	}
	fact = &prepared
	j = fact.Job
	if e = s.FinishSupervision(ctx, *fact, "result_pending", "delivered", "commander-root", "", time.Now().Add(time.Minute), false); e != nil {
		t.Fatal(e)
	}
	if e = s.AcknowledgeSupervision(ctx, pid, "task", fact.Epoch, "manager-other", j.RequestID); e == nil {
		t.Fatal("foreign owner acknowledged")
	}
	if e = s.AcknowledgeSupervision(ctx, pid, "task", fact.Epoch, "commander-root", j.RequestID); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	r.OpencodeSessionID = "replacement"
	r.SpawnTime++
	newID, e := s.InsertAgentRun(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AcknowledgeSupervision(ctx, pid, "task", fact.Epoch, "commander-root", j.RequestID); e == nil {
		t.Fatal("old generation acknowledged replacement")
	}
	if e = s.DB().QueryRow(`SELECT commander_report_result(?,?,?,?,?)`, pid, "task", rid, "session", "Late result").Scan(&accepted); e != nil || accepted {
		t.Fatal("late result accepted")
	}
	if newID == rid {
		t.Fatal("run identity reused")
	}
}

func TestPostgresSupervisionRejectsAcknowledgementAfterProgress(t *testing.T) {
	s, pid, ctx := jurisdictionFixture(t)
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
	fact, e := s.SupervisionFact(ctx, job)
	if e != nil {
		t.Fatal(e)
	}
	prepared, ok, e := s.PrepareSupervisionDelivery(ctx, *fact, "work_pending", "commander")
	if e != nil || !ok {
		t.Fatal("prepare", e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='in_progress' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	if e = s.AcknowledgeSupervision(ctx, pid, "leaf", prepared.Epoch, "commander", prepared.Job.RequestID); e == nil {
		t.Fatal("superseded request accepted after real progress")
	}
}

func TestSupervisionOldChildProgressCannotMoveCompletionClockBackwards(t *testing.T) {
	st := postgresTestStore(t)
	ctx := context.Background()
	pid, e := st.InsertProject(ctx, Project{Name: "completion-clock", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `INSERT INTO bead_tasks(project_id,id,parent_id,title,status,created_at,updated_at) VALUES(?,'goal',NULL,'Goal','open','now','now'),(?,'old','goal','Old completed check','closed','now','now')`, pid, pid); e != nil {
		t.Fatal(e)
	}
	recent, old := time.Now().Add(-time.Minute).Unix(), time.Now().Add(-time.Hour).Unix()
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET completion_at=? WHERE project_id=? AND id='old'`, old, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET completion_at=? WHERE project_id=? AND id='goal'`, recent, pid); e != nil {
		t.Fatal(e)
	}
	// Applying a late receipt on an old closed check is real progress, but it is
	// not a new completion and cannot erase a more recent outcome elsewhere.
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET progress_seq=progress_seq+1,progress_at=? WHERE project_id=? AND id='old'`, time.Now().Unix(), pid); e != nil {
		t.Fatal(e)
	}
	var completion int64
	if e = st.DB().QueryRowContext(ctx, `SELECT completion_at FROM bead_tasks WHERE project_id=? AND id='goal'`, pid).Scan(&completion); e != nil || completion != recent {
		t.Fatal("old child progress corrupted the parent's completion clock", recent, completion, e)
	}
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='open' WHERE project_id=? AND id='old'`, pid); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB().ExecContext(ctx, `UPDATE bead_tasks SET status='closed' WHERE project_id=? AND id='old'`, pid); e != nil {
		t.Fatal(e)
	}
	if e = st.DB().QueryRowContext(ctx, `SELECT completion_at FROM bead_tasks WHERE project_id=? AND id='goal'`, pid).Scan(&completion); e != nil || completion <= recent {
		t.Fatal("a real new completion did not advance its parent", recent, completion, e)
	}
}
