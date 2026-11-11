package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestPostgresUsageRecoveryFencesSessionWorkspaceAndTerminalEvidence(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	claim, e := s.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || claim == nil {
		t.Fatal(claim, e)
	}
	first := AgentRun{ExecutionClaimToken: claim.Token, ProjectID: pid, TaskBeadID: claim.BeadID, AgentName: "operator-usage-source", OpencodeSessionID: "source-session", ExecutionDriver: "native", ExecutionRuntime: "claude", ExecutionAccount: "default", ExecutionModel: "haiku", SpawnTime: now.Unix(), WorktreePath: sql.NullString{String: "/private/retained-workspace", Valid: true}}
	rid, e := s.InsertAgentRun(ctx, first)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE bead_tasks SET status='in_progress' WHERE project_id=? AND id=?`, pid, first.TaskBeadID); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishNativeExecutionReport(ctx, first.AgentName, rid, "wrong-session", "usage-limited", "usage exhausted"); e == nil {
		t.Fatal("foreign receipt released capacity")
	}
	if e = s.FinishNativeExecutionReport(ctx, first.AgentName, rid, first.OpencodeSessionID, "usage-limited", "usage exhausted"); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishNativeExecutionReport(ctx, first.AgentName, rid, first.OpencodeSessionID, "exited", "late summary"); e != nil {
		t.Fatal(e)
	}
	if e = s.ReapExecutionStartupClaims(ctx, now); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || next == nil || next.ResumeRunID != rid {
		t.Fatal(next, e)
	}
	first.ExecutionClaimToken = next.Token
	first.ResumeSourceRunID = rid
	first.ExecutionRuntime = "codex"
	if _, e = s.InsertAgentRun(ctx, first); e == nil {
		t.Fatal("usage handoff reused native source session")
	}
	first.OpencodeSessionID = "new-provider-session"
	bad := first
	bad.WorktreePath.String = "/private/unrelated-workspace"
	if _, e = s.InsertAgentRun(ctx, bad); e == nil {
		t.Fatal("usage handoff abandoned retained workspace")
	}
	if _, e = s.InsertAgentRun(ctx, first); e != nil {
		t.Fatal("validated source handoff refused", e)
	}
	old, e := s.GetAgentRun(ctx, rid)
	if e != nil || old.Status != "native-error" {
		t.Fatal("duplicate receipt changed terminal outcome", old, e)
	}
}

func TestPostgresUsageRecoveryWaitsForSavedAnswerBeforeProviderHandoff(t *testing.T) {
	s, pid, ctx := executionFixture(t)
	now := time.Now()
	c, e := s.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || c == nil {
		t.Fatal(c, e)
	}
	source := AgentRun{ExecutionClaimToken: c.Token, ProjectID: pid, TaskBeadID: c.BeadID, AgentName: "operator-usage-question", OpencodeSessionID: "source-session", ExecutionDriver: "native", ExecutionRuntime: "claude", ExecutionAccount: "default", ExecutionModel: "haiku", SpawnTime: now.Unix()}
	rid, e := s.InsertAgentRun(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE bead_tasks SET execution_ready=FALSE WHERE project_id=? AND id<>?`, pid, c.BeadID); e != nil {
		t.Fatal(e)
	}
	qid, e := s.AskQuestion(ctx, QuestionRequest{ProjectID: pid, BeadID: c.BeadID, PlanRevision: c.PlanRevision, Actor: source.AgentName, RunID: rid, Session: source.OpencodeSessionID, ClientKey: "usage-question", Question: "Which bounded choice?", Checkpoint: "Retained work", CheckpointRuntime: "claude", Blocking: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishNativeExecutionReport(ctx, source.AgentName, rid, source.OpencodeSessionID, "usage-limited", "Usage exhausted while awaiting a choice"); e != nil {
		t.Fatal(e)
	}
	if yes, e := s.UsageRecoverySource(ctx, rid); e != nil || yes {
		t.Fatal("unanswered question became provider recovery", yes, e)
	}
	if c, e := s.ClaimNextExecution(ctx, "vm-0", 1, now); e != nil || c != nil {
		t.Fatal("pending question dispatched", c, e)
	}
	if _, e = s.AnswerQuestion(ctx, qid, "commander", "Continue the retained bounded work", true); e != nil {
		t.Fatal(e)
	}
	if yes, e := s.UsageRecoverySource(ctx, rid); e != nil || !yes {
		t.Fatal("saved answer did not permit usage handoff", yes, e)
	}
	if e = s.ReapExecutionStartupClaims(ctx, now); e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimNextExecution(ctx, "vm-0", 1, now)
	if e != nil || c == nil || c.ResumeRunID != rid {
		t.Fatal(c, e)
	}
	source.ExecutionClaimToken = c.Token
	source.ResumeSourceRunID = rid
	source.ExecutionRuntime = "codex"
	source.OpencodeSessionID = "fresh-provider-session"
	if _, e = s.InsertAgentRun(ctx, source); e != nil {
		t.Fatal("answered usage checkpoint could not use available provider", e)
	}
}
