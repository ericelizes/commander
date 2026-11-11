package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func recoveryClaim(t *testing.T, role string) (*store.Store, int64, int64) {
	t.Helper()
	s, _ := pgtest.Open(t)
	ctx := context.Background()
	pid, err := s.InsertProject(ctx, store.Project{Name: "recovery", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,acceptance_criteria,created_at,updated_at) VALUES(?,'work','Bound work','Inspect the assigned scope','Record evidence','now','now')`, pid); err != nil {
		t.Fatal(err)
	}
	rid, err := s.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "work", AgentName: role, OpencodeSessionID: "bound-session", SpawnTime: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`UPDATE agent_runs SET execution_driver='native' WHERE id=?`, rid); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetOrchestrationControl(ctx, 1, "active", "private recovery fixture"); err != nil {
		t.Fatal(err)
	}
	return s, pid, rid
}

func TestExhaustedNativeStartupWakesPlanningOwnerWithoutReleasingUnknownCustody(t *testing.T) {
	s, pid, rid := recoveryClaim(t, "operator-recovery")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.RecordNativeStartupAttempt(ctx, "operator-recovery", rid, "bound-session", "runtime adapter refused startup", time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RegisterCommander(ctx, "commander-urgent", "Urgent"); err != nil {
		t.Fatal(err)
	}
	j, err := s.Jurisdiction(ctx, pid, "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "work", Actor: "commander", ExpectedOwner: "commander", NextCommander: "commander-urgent", ExpectedRevision: j.Revision, Reason: "Current owner handles repair"}); err != nil {
		t.Fatal(err)
	}
	observations, err := s.ObservedExecutions(ctx)
	if err != nil || len(observations) != 1 || observations[0].RunID != rid || observations[0].SessionID != "bound-session" || observations[0].StartupAttempts != 5 || observations[0].StartupError != "runtime adapter refused startup" {
		t.Fatal("startup evidence lost current custody", observations, err)
	}
	fact, err := s.SupervisionFact(ctx, store.SupervisionJob{ProjectID: pid, BeadID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if fact.PresenceKnown || !fact.ExecutorBound {
		t.Fatal("fixture did not represent unknown bound custody", fact)
	}
	decision := Decide(*fact, time.Now())
	if decision.Action != "wake" || decision.Target != "commander-urgent" || decision.Reason != "execution_launch_blocked" {
		t.Fatal("exhausted startup became endless checking", decision)
	}
	run, err := s.GetAgentRun(ctx, rid)
	if err != nil || run.Status != "running" {
		t.Fatal("unknown custody was released", run, err)
	}
	fact.Held = true
	if decision = Decide(*fact, time.Now()); decision.Action != "observe" || decision.Reason != "held" {
		t.Fatal("repair wake overrode a hold", decision)
	}
}

func TestCompletedExecutionNamedCommanderReturnsToPlanningReview(t *testing.T) {
	s, pid, rid := recoveryClaim(t, "commander")
	ctx := context.Background()
	if err := s.SetAgentState(ctx, "commander", store.AgentWorking, "last execution tool"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNativeExecutionReport(ctx, "commander", rid, "bound-session", "exited", "Retained unaccepted report"); err != nil {
		t.Fatal(err)
	}
	fact, err := s.SupervisionFact(ctx, store.SupervisionJob{ProjectID: pid, BeadID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if fact.ExecutorBound || !fact.ResultReady || !fact.PresenceKnown {
		t.Fatal("closed execution still impersonates live custody", fact)
	}
	decision := Decide(*fact, time.Now())
	if decision.Action != "wake" || decision.Reason != "result_pending" || decision.Target != "commander" {
		t.Fatal("completed generic identity lost its review obligation", decision)
	}
}

func TestConfirmedMissingExecutionDoesNotLookBusyFromOldActivity(t *testing.T) {
	f := store.SupervisionFact{AgentID: "executor", SupervisorID: "commander", PlanningCommander: "commander", Status: "open", ExecutorBound: true, PresenceKnown: true, Alive: false, AgentState: store.AgentWorking, ProgressAt: time.Now().Unix()}
	d := Decide(f, time.Now())
	if d.Action != "resume" || d.Reason != "work_pending" {
		t.Fatal("dead execution's last activity suppressed recovery", d)
	}
}
