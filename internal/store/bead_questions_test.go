package store

import (
	"context"
	"testing"
	"time"
)

func questionFixture(t *testing.T) (*Store, int64, context.Context, int64, QuestionRequest) {
	t.Helper()
	s, pid, ctx := jurisdictionFixture(t)
	if _, e := s.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='Bounded work',acceptance_criteria='Evidence passes' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	rid, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: "operator-question", OpencodeSessionID: "question-session", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	var revision int64
	if e = s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	r := QuestionRequest{ProjectID: pid, BeadID: "leaf", Actor: "operator-question", RunID: rid, Session: "question-session", PlanRevision: revision, ClientKey: "deployment-owner", Question: "Which integration should own this change?", Recommendation: "Keep it in this workstream", Checkpoint: "Implementation prepared; no release attempted", Blocking: true}
	return s, pid, ctx, rid, r
}

func TestPostgresQuestionFencesExecutionAndDeduplicatesRequests(t *testing.T) {
	s, _, ctx, _, r := questionFixture(t)
	bad := r
	bad.Actor = "operator-other"
	if _, e := s.AskQuestion(ctx, bad); e == nil {
		t.Fatal("foreign execution asked a question")
	}
	bad = r
	bad.Session = "old"
	if _, e := s.AskQuestion(ctx, bad); e == nil {
		t.Fatal("foreign session asked a question")
	}
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	same, e := s.AskQuestion(ctx, r)
	if e != nil || same != id {
		t.Fatalf("retry duplicated request: %s %s %v", id, same, e)
	}
	bad = r
	bad.Question = "Different intent"
	if _, e = s.AskQuestion(ctx, bad); e == nil {
		t.Fatal("request id reused with different content")
	}
	inbox, e := s.QuestionInbox(ctx, "commander")
	if e != nil || len(inbox) != 1 || inbox[0].ID != id {
		t.Fatalf("wrong planning inbox: %+v %v", inbox, e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander-urgent", "Unauthorized", true); e == nil {
		t.Fatal("unrelated Commander answered")
	}
}

func TestPostgresQuestionFollowsJurisdictionAndCoordinatesCurrentOwners(t *testing.T) {
	s, pid, ctx, _, r := questionFixture(t)
	if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "ordinary", ExpectedOwner: "commander", ExpectedRevision: 1, NextCommander: "commander-specialist", Reason: "independent scope"}); e != nil {
		t.Fatal(e)
	}
	r.Related = []QuestionScope{{ProjectID: pid, BeadID: "ordinary"}}
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	complete, e := s.AnswerQuestion(ctx, id, "commander", "Primary scope agrees", true)
	if e != nil || complete {
		t.Fatalf("one owner approved cross-scope change alone: %t %v", complete, e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "ordinary", ExpectedOwner: "commander-specialist", ExpectedRevision: 2, NextCommander: "commander-urgent", Reason: "urgent handoff"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander-specialist", "Late answer", true); e == nil {
		t.Fatal("former owner answered after handoff")
	}
	urgent, e := s.QuestionInbox(ctx, "commander-urgent")
	if e != nil || len(urgent) != 1 {
		t.Fatalf("handoff lost question: %+v %v", urgent, e)
	}
	// Partial decisions revalidate after a jurisdiction revision changes;
	// already finalized decisions remain immutable history.
	complete, e = s.AnswerQuestion(ctx, id, "commander-urgent", "Urgent owner agrees", true)
	if e != nil || complete {
		t.Fatalf("stale partial approval retained: %t %v", complete, e)
	}
	complete, e = s.AnswerQuestion(ctx, id, "commander", "Primary scope confirms", true)
	if e != nil || !complete {
		t.Fatalf("coordinated decision did not complete: %t %v", complete, e)
	}
	q, e := s.Question(ctx, id)
	if e != nil || q.State != "answered" || len(q.Answers) != 3 {
		t.Fatalf("decision history lost: %+v %v", q, e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: pid, BeadID: "root", ExpectedOwner: "commander", ExpectedRevision: 3, NextCommander: "commander-specialist", Reason: "return coverage"}); e != nil {
		t.Fatal(e)
	}
	complete, e = s.AnswerQuestion(ctx, id, "commander", "Primary scope confirms", true)
	if e != nil || !complete {
		t.Fatalf("historical answer retry changed finalized decision: %t %v", complete, e)
	}
}

func TestPostgresQuestionExitAndAnswerOrdersResumeExactlyOneCheckpoint(t *testing.T) {
	for _, exitFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "exit-first", false: "answer-first"}[exitFirst], func(t *testing.T) {
			s, pid, ctx, rid, r := questionFixture(t)
			id, e := s.AskQuestion(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
			closeRun := func() {
				t.Helper()
				if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
					t.Fatal(e)
				}
			}
			answer := func() {
				t.Helper()
				done, err := s.AnswerQuestion(ctx, id, "commander", "Keep the prepared implementation", true)
				if err != nil || !done {
					t.Fatal(err)
				}
			}
			if exitFirst {
				closeRun()
				var ready bool
				if e = s.DB().QueryRowContext(ctx, `SELECT execution_ready FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&ready); e != nil || ready {
					t.Fatal("pending question resumed before answer", e)
				}
				answer()
			} else {
				answer()
				closeRun()
			}
			var ready bool
			var resume int64
			if e = s.DB().QueryRowContext(ctx, `SELECT execution_ready,resume_run_id FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&ready, &resume); e != nil || !ready || resume != rid {
				t.Fatalf("checkpoint not ready: %t %d %v", ready, resume, e)
			}
			if waited, e := s.RunQuestionWait(ctx, rid); e != nil || !waited {
				t.Fatal("intentional checkpoint not recorded", e)
			}
		})
	}
}

func TestPostgresQuestionPlanEditsSupersedeButOperationalActivityDoesNot(t *testing.T) {
	s, pid, ctx, rid, r := questionFixture(t)
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET labels=labels||'["cmdr:brainstorming","owner:operator-question","wait-checked:123"]' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	q, e := s.Question(ctx, id)
	if e != nil || q.State != "pending" {
		t.Fatal("operational labels superseded planning intent", e)
	}
	if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='Different goal' WHERE project_id=? AND id='leaf'`, pid); e != nil {
		t.Fatal(e)
	}
	q, e = s.Question(ctx, id)
	if e != nil || q.State != "superseded" {
		t.Fatal("new plan did not supersede question", e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander", "Old answer", true); e == nil {
		t.Fatal("answer applied to superseded plan")
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	var ready bool
	if e = s.DB().QueryRowContext(ctx, `SELECT execution_ready FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&ready); e != nil || ready {
		t.Fatal("superseded checkpoint automatically resumed", e)
	}
}

func TestPostgresQuestionAnswerNeverOverridesExplicitStop(t *testing.T) {
	s, pid, ctx, rid, r := questionFixture(t)
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, rid, "stopped"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander", "Information is ready", true); e != nil {
		t.Fatal(e)
	}
	var ready bool
	if e = s.DB().QueryRowContext(ctx, `SELECT execution_ready FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&ready); e != nil || ready {
		t.Fatal("answer overrode an explicit execution stop", e)
	}
}

func TestPostgresQuestionRetryIdentityIncludesImmutableExecution(t *testing.T) {
	s, pid, ctx, rid, r := questionFixture(t)
	old, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerQuestion(ctx, old, "commander", "Recorded guidance", true); e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	next, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: r.Actor, OpencodeSessionID: r.Session, SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	r.RunID = next
	id, e := s.AskQuestion(ctx, r)
	if e != nil || id == old {
		t.Fatalf("new run reused old request: %s %s %v", id, old, e)
	}
}

func TestPostgresQuestionCheckpointIsConsumedAndFreshPlanDoesNotReuseIt(t *testing.T) {
	s, pid, ctx, rid, r := questionFixture(t)
	if _, e := s.SetOrchestrationControl(ctx, 1, "active", "checkpoint test"); e != nil {
		t.Fatal(e)
	}
	if e := s.ConfigureExecutionSlot(ctx, "vm-question", []string{"code", "knowledge"}, true); e != nil {
		t.Fatal(e)
	}
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander", "Continue this implementation", true); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimNextExecution(ctx, "vm-question", 8, time.Now())
	if e != nil || c == nil || c.ResumeRunID != rid {
		t.Fatalf("missing checkpoint claim: %+v %v", c, e)
	}
	resumed, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: r.Actor, OpencodeSessionID: r.Session, SpawnTime: time.Now().Unix(), ExecutionClaimToken: c.Token, ResumeSourceRunID: rid})
	if e != nil {
		t.Fatal(e)
	}
	var marker, markerRevision int64
	if e = s.DB().QueryRowContext(ctx, `SELECT COALESCE(resume_run_id,0),resume_plan_revision FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&marker, &markerRevision); e != nil || marker != 0 || markerRevision != 0 {
		t.Fatalf("successful bind retained checkpoint intent: %d %d %v", marker, markerRevision, e)
	}
	if e = s.MarkClosed(ctx, resumed, "exited"); e != nil {
		t.Fatal(e)
	}
	var revision int64
	if e = s.DB().QueryRowContext(ctx, `SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetExecutionReady(ctx, pid, "leaf", "commander", revision, 1, true, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.ReapExecutionStartupClaims(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.ClaimNextExecution(ctx, "vm-question", 8, time.Now())
	if e != nil || fresh == nil || fresh.ResumeRunID != 0 {
		t.Fatalf("fresh plan reused old checkpoint: %+v %v", fresh, e)
	}
}

func TestPostgresQuestionRejectedDecisionRetryRemainsComplete(t *testing.T) {
	s, _, ctx, _, r := questionFixture(t)
	id, e := s.AskQuestion(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		complete, err := s.AnswerQuestion(ctx, id, "commander", "Do not cross that boundary", false)
		if err != nil || !complete {
			t.Fatalf("final rejection retry %d: %t %v", i, complete, err)
		}
	}
	if _, e = s.AnswerQuestion(ctx, id, "commander", "Replace recorded refusal", true); e == nil {
		t.Fatal("final answer was overwritten")
	}
}

func TestPostgresQuestionFailedLaunchRestoresOnlyUnchangedCheckpoint(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry-same-plan", true: "revised-plan"}[edit], func(t *testing.T) {
			s, pid, ctx, rid, r := questionFixture(t)
			if _, e := s.SetOrchestrationControl(ctx, 1, "active", "checkpoint retry"); e != nil {
				t.Fatal(e)
			}
			if e := s.ConfigureExecutionSlot(ctx, "vm-question", []string{"code", "knowledge"}, true); e != nil {
				t.Fatal(e)
			}
			id, e := s.AskQuestion(ctx, r)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.MarkClosed(ctx, rid, "exited"); e != nil {
				t.Fatal(e)
			}
			if _, e = s.AnswerQuestion(ctx, id, "commander", "Continue", true); e != nil {
				t.Fatal(e)
			}
			now := time.Now()
			c, e := s.ClaimNextExecution(ctx, "vm-question", 8, now)
			if e != nil || c == nil {
				t.Fatal(e)
			}
			next, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: pid, TaskBeadID: "leaf", AgentName: r.Actor, OpencodeSessionID: r.Session, SpawnTime: now.Unix(), ExecutionClaimToken: c.Token, ResumeSourceRunID: rid})
			if e != nil {
				t.Fatal(e)
			}
			// Bookkeeping bound the run but the native process failed to start.
			if e = s.MarkClosed(ctx, next, "spawn-failed"); e != nil {
				t.Fatal(e)
			}
			if edit {
				if _, e = s.DB().ExecContext(ctx, `UPDATE bead_tasks SET description='A new direction' WHERE project_id=? AND id='leaf'`, pid); e != nil {
					t.Fatal(e)
				}
			}
			if e = s.FailExecutionClaim(ctx, *c, "native launcher failed", now); e != nil {
				t.Fatal(e)
			}
			var ready bool
			var source int64
			if e = s.DB().QueryRowContext(ctx, `SELECT execution_ready,COALESCE(resume_run_id,0) FROM bead_tasks WHERE project_id=? AND id='leaf'`, pid).Scan(&ready, &source); e != nil {
				t.Fatal(e)
			}
			if edit {
				if ready || source != 0 {
					t.Fatalf("failed old launch restored revised plan: %t %d", ready, source)
				}
			} else {
				if !ready || source != rid {
					t.Fatalf("failed launch lost checkpoint: %t %d", ready, source)
				}
				again, err := s.ClaimNextExecution(ctx, "vm-question", 8, now.Add(2*time.Minute))
				if err != nil || again == nil || again.ResumeRunID != rid {
					t.Fatalf("checkpoint retry unavailable: %+v %v", again, err)
				}
			}
		})
	}
}
