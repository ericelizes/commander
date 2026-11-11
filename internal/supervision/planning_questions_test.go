package supervision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

func projectDecision(t *testing.T, s *store.Store, pid int64) string {
	t.Helper()
	var id string
	var revision int64
	if e := s.DB().QueryRow(`SELECT revision FROM jurisdiction_projects WHERE project_id=?`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	err := s.WithTx(context.Background(), func(q store.Querier) error {
		var e error
		id, e = s.AskPlanningQuestionInTx(context.Background(), q, store.QuestionRequest{Planning: true, ProjectScope: true, ProjectID: pid, Actor: "commander", ClientKey: "project-decision", PlanRevision: revision, Question: "Approve this exact top-level plan?"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPlanningProjectQuestionSharesDispatchWithoutInventingWork(t *testing.T) {
	s, pid := workerFixture(t)
	ctx := context.Background()
	if _, e := s.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander-root", ExpectedRevision: 2, NextCommander: "commander", Reason: "combine decision and execution dispatch"}); e != nil {
		t.Fatal(e)
	}
	id := projectDecision(t, s, pid)
	// Planning and execution obligations share the same recipient and dispatch fence.
	var before int
	var progress int64
	if e := s.DB().QueryRow(`SELECT count(*),sum(progress_seq) FROM bead_tasks WHERE project_id=?`, pid).Scan(&before, &progress); e != nil {
		t.Fatal(e)
	}
	calls := 0
	now := time.Now()
	w := &Worker{Store: s, Mode: "shadow", Now: func() time.Time { return now }, Deliver: func(context.Context, string, string) error { t.Fatal("shadow delivered"); return nil }}
	if e := w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().Exec(`UPDATE bead_supervision SET due_at=0`); e != nil {
		t.Fatal(e)
	}
	w.Mode = "active"
	w.Deliver = func(_ context.Context, role, prompt string) error {
		calls++
		if role != "commander" || !strings.Contains(prompt, id) || !strings.Contains(prompt, "--request") {
			t.Fatal("combined notice lost an obligation", role, prompt)
		}
		return nil
	}
	if e := w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("obligations did not share a single dispatch", calls)
	}
	question, e := s.Question(ctx, id)
	if e != nil || question.State != "pending" || !question.ProjectScope {
		t.Fatalf("delivery answered or lost project scope: %+v %v", question, e)
	}
	if done, e := s.AnswerQuestion(ctx, id, "commander", "Agreed to exact plan", true); e != nil || !done {
		t.Fatal(done, e)
	}
	var after int
	var finalProgress int64
	if e := s.DB().QueryRow(`SELECT count(*),sum(progress_seq) FROM bead_tasks WHERE project_id=?`, pid).Scan(&after, &finalProgress); e != nil {
		t.Fatal(e)
	}
	if after != before || finalProgress != progress {
		t.Fatalf("planning communication invented task/progress: %d/%d %d/%d", before, after, progress, finalProgress)
	}
}

func TestPlanningQuestionDeliveryRestartsRetriesAndFencesAnswers(t *testing.T) {
	s, pid := workerFixture(t)
	ctx := context.Background()
	id := projectDecision(t, s, pid)
	now := time.Now()
	// Silence unrelated execution supervision to isolate delivery retry behavior.
	if _, e := s.DB().Exec(`UPDATE bead_supervision SET due_at=?`, now.Add(time.Hour).Unix()); e != nil {
		t.Fatal(e)
	}
	jobs, e := s.LeasePlanningQuestions(ctx, now, true, 128)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	if again, e := s.LeasePlanningQuestions(ctx, now.Add(time.Second), true, 128); e != nil || len(again) != 0 {
		t.Fatal("lease duplicated", again, e)
	}
	jobs, e = s.LeasePlanningQuestions(ctx, now.Add(31*time.Second), true, 128)
	if e != nil || len(jobs) != 1 {
		t.Fatal("expired lease lost", jobs, e)
	}
	now = now.Add(time.Minute)
	calls := 0
	w := &Worker{Store: s, Mode: "active", Now: func() time.Time { return now }, Deliver: func(context.Context, string, string) error { calls++; return errors.New("transport unavailable") }}
	now = now.Add(time.Minute)
	if e = w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	var attempts int
	if e = s.DB().QueryRow(`SELECT delivery_attempts FROM bead_question_scopes WHERE question_id=?`, id).Scan(&attempts); e != nil || attempts != 1 {
		t.Fatal(attempts, e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander", "Accepted", true); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.PlanningQuestionDeliveryCurrent(ctx, jobs[0]); e != nil || ok {
		t.Fatal("answered question retained delivery authority", ok, e)
	}
	now = now.Add(10 * time.Minute)
	if e = w.Pass(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("answered decision redelivered", calls)
	}
}
