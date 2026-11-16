package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// closeSupervisedRun closes execution and transfers the next move in one
// transaction. Lock order is bead, then run, matching claims and state hooks.
func (s *Store) closeSupervisedRun(ctx context.Context, rid int64, status string) error {
	return s.closeSupervisedRunIdentity(ctx, rid, status, "", "", "")
}

// FinishNativeExecution consumes a host-observed exit, never a model's claim
// of completion. An old receipt cannot alter a replacement run or its custody.
func (s *Store) FinishNativeExecution(ctx context.Context, role string, rid int64, session, status string) error {
	return s.FinishNativeExecutionReport(ctx, role, rid, session, status, "")
}

// FinishNativeExecutionReport records a physical exit and its unaccepted model
// report atomically. It never closes work or substitutes for review/approval.
func (s *Store) FinishNativeExecutionReport(ctx context.Context, role string, rid int64, session, status, report string) error {
	if !s.postgres || rid <= 0 || role == "" || session == "" || (status != "exited" && status != "native-error" && status != "usage-limited") {
		return errors.New("invalid native execution exit identity")
	}
	return s.closeSupervisedRunIdentity(ctx, rid, status, role, session, truncateSupervision(report, 6000))
}

func (s *Store) closeSupervisedRunIdentity(ctx context.Context, rid int64, status, expectedRole, expectedSession, nativeReport string) error {
	return s.WithTx(ctx, func(q Querier) error {
		return closeSupervisedRunTx(ctx, q, rid, status, expectedRole, expectedSession, nativeReport)
	})
}

const AgentStopped = "Agent stopped: "

// FinishNativeExecutionReceipt preserves the host's physical exit time and cause,
// including when the daemon replays the receipt after an outage.
func (s *Store) FinishNativeExecutionReceipt(ctx context.Context, role string, rid int64, session, status, report string, observedAt int64, reason string) error {
	if !s.postgres || rid <= 0 || role == "" || session == "" || observedAt <= 0 || (status != "exited" && status != "native-error" && status != "usage-limited") {
		return errors.New("invalid native execution exit receipt")
	}
	return s.WithTx(ctx, func(q Querier) error {
		return closeSupervisedRunReceiptTx(ctx, q, rid, status, role, session, truncateSupervision(report, 6000), observedAt, truncateSupervision(reason, 1000))
	})
}

func closeSupervisedRunTx(ctx context.Context, q Querier, rid int64, status, expectedRole, expectedSession, nativeReport string) error {
	return closeSupervisedRunReceiptTx(ctx, q, rid, status, expectedRole, expectedSession, nativeReport, time.Now().Unix(), status)
}
func closeSupervisedRunReceiptTx(ctx context.Context, q Querier, rid int64, status, expectedRole, expectedSession, nativeReport string, observedAt int64, reason string) error {
	usageLimited := status == "usage-limited"
	if usageLimited {
		status = "native-error"
	}
	var pid int64
	var bead, session, actor string
	var spawn int64
	if e := q.QueryRowContext(ctx, `SELECT project_id,task_bead_id,opencode_session_id,spawn_time,agent_name FROM agent_runs WHERE id=?`, rid).Scan(&pid, &bead, &session, &spawn, &actor); e != nil {
		return e
	}
	if expectedRole != "" && (strings.ReplaceAll(actor, ".", "-") != strings.ReplaceAll(expectedRole, ".", "-") || session != expectedSession) {
		return errors.New("native exit does not match the recorded run/session")
	}
	if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
		return e
	}
	var current sql.NullInt64
	e := q.QueryRowContext(ctx, `SELECT active_run_id FROM bead_tasks WHERE project_id=? AND id=? FOR UPDATE`, pid, bead).Scan(&current)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if expectedRole != "" {
		result, err := q.ExecContext(ctx, `UPDATE agent_runs SET status=? WHERE id=? AND status='running'`, status, rid)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	} else {
		if _, e = q.ExecContext(ctx, `UPDATE agent_runs SET status=? WHERE id=?`, status, rid); e != nil {
			return e
		}
	}
	if _, e = q.ExecContext(ctx, `UPDATE run_observations SET alive=FALSE,checked_at=? WHERE run_id=?`, time.Now().Unix(), rid); e != nil {
		return e
	}
	if _, e = q.ExecContext(ctx, `UPDATE native_presence SET alive=FALSE,checked_at=? WHERE run_id=? AND session_id=?`, time.Now().Unix(), rid, session); e != nil {
		return e
	}
	if !current.Valid || current.Int64 != rid {
		return nil
	}
	if expectedRole != "" {
		history := "Native execution exited; its report has not been accepted."
		if status == "native-error" {
			history = "Native execution ended with an error; its report has not been accepted."
		}
		factKind := ""
		if usageLimited {
			history = "Native execution stopped at its account usage limit; authorized work awaits available capacity."
			factKind = "native-usage-limit"
		}
		body := strings.TrimSpace(nativeReport)
		if body == "" {
			body = "No terminal report was returned. Inspect the chat and artifacts before deciding the next action."
		}
		facts, _ := json.Marshal(map[string]any{"ended_at": observedAt, "reason": reason})
		if _, e = q.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at,run_id,fact_kind,fact_value) VALUES(?,?,?,'cmdrd',?,?,?,?,?::jsonb)`, pid, bead, "native-exit:"+fmt.Sprint(rid), history+"\n"+body, time.Unix(observedAt, 0).UTC().Format(time.RFC3339Nano), rid, factKind, string(facts)); e != nil {
			return e
		}
	}
	// A cutover the Commander asked for settles the exit: a runtime move
	// resumes this run's work on the new runtime, a stop waits for re-planning.
	// Neither is a result to review.
	if handled, e := settleCutoverExit(ctx, q, pid, bead, rid); e != nil || handled {
		return e
	}
	// A blocking question is a checkpoint, not a completed deliverable.
	// Preserve its exact resume source regardless of answer/exit ordering.
	if expectedRole != "" {
		var checkpoint bool
		if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_questions q JOIN bead_tasks b ON b.project_id=q.project_id AND b.id=q.bead_id WHERE q.run_id=? AND q.project_id=? AND q.bead_id=? AND q.blocking AND q.state IN('pending','answered') AND q.plan_revision=b.plan_revision)`, rid, pid, bead).Scan(&checkpoint); e != nil {
			return e
		}
		if checkpoint {
			if _, e = q.ExecContext(ctx, `UPDATE bead_tasks SET responsible_agent_id=commander_supervisor(project_id,id) WHERE project_id=? AND id=? AND active_run_id=?`, pid, bead, rid); e != nil {
				return e
			}
			_, e = q.ExecContext(ctx, `SELECT commander_resume_question_run(?,?)`, pid, bead)
			return e
		}
	}
	rows, e := q.QueryContext(ctx, `SELECT text,created_at FROM bead_comments WHERE project_id=? AND bead_id=? AND run_id=? AND replace(author,'.','-')=replace(?::text,'.','-') AND lower(ltrim(text)) LIKE 'final report%' ORDER BY created_at DESC,id DESC LIMIT 32`, pid, bead, rid, actor)
	if e != nil {
		return e
	}
	summary := ""
	for rows.Next() {
		var text, created string
		if e = rows.Scan(&text, &created); e != nil {
			rows.Close()
			return e
		}
		at, err := time.Parse(time.RFC3339Nano, created)
		if err == nil && at.Unix() >= spawn && strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "final report") {
			summary = text
			break
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	// Usage exhaustion is a recoverable capacity event, not a deliverable.
	// Requeue only the exact admitted plan after physical exit. Holds,
	// pending questions and capacity remain enforced at the next claim.
	if expectedRole != "" && usageLimited && summary == "" {
		result, err := q.ExecContext(ctx, `UPDATE bead_tasks b SET status='open',execution_ready=TRUE,resume_run_id=?,resume_plan_revision=b.plan_revision,execution_attempts=0,execution_due_at=0,execution_error=? WHERE b.project_id=? AND b.id=? AND b.active_run_id=? AND b.status IN('open','in_progress') AND b.result_run_id IS NULL AND EXISTS(SELECT 1 FROM execution_slots x WHERE x.run_id=? AND x.plan_revision=b.plan_revision)`, rid, UsageCapacityError+"execution stopped at its account limit; awaiting available account", pid, bead, rid, rid)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n > 0 {
			return nil
		}
	}
	// A terminal turn is not a completed task. Only an attributed Final report
	// hands off a deliverable. Otherwise release custody and escalate with cause;
	// automatic retries of arbitrary partial side effects are unsafe.
	if expectedRole != "" && summary == "" {
		detail := AgentStopped + reason + "; no attributed final report. Commander must inspect the retained work and re-dispatch or repair."
		if strings.TrimSpace(nativeReport) != "" {
			detail += " Last note: " + nativeReport
		}
		_, e = q.ExecContext(ctx, `UPDATE bead_tasks SET active_run_id=NULL,execution_ready=FALSE,resume_run_id=NULL,resume_plan_revision=0,execution_error=?,responsible_agent_id=commander_supervisor(project_id,id) WHERE project_id=? AND id=? AND active_run_id=?`, truncateSupervision(detail, 1000), pid, bead, rid)
		return e
	}

	if summary != "" {
		var accepted bool
		return q.QueryRowContext(ctx, `SELECT commander_report_result(?,?,?,?,?)`, pid, bead, rid, session, summary).Scan(&accepted)
	}
	if _, e = q.ExecContext(ctx, `UPDATE bead_tasks SET responsible_agent_id=commander_supervisor(project_id,id) WHERE project_id=? AND id=? AND active_run_id=? AND responsible_agent_id<>commander_supervisor(project_id,id)`, pid, bead, rid); e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `SELECT commander_resume_question_run(?,?)`, pid, bead)
	return e
}
