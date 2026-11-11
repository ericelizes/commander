package store

import (
	"context"
	"database/sql"
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
	if !s.postgres || rid <= 0 || role == "" || session == "" || (status != "exited" && status != "native-error") {
		return errors.New("invalid native execution exit identity")
	}
	return s.closeSupervisedRunIdentity(ctx, rid, status, role, session, truncateSupervision(report, 6000))
}

func (s *Store) closeSupervisedRunIdentity(ctx context.Context, rid int64, status, expectedRole, expectedSession, nativeReport string) error {
	return s.WithTx(ctx, func(q Querier) error {
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
			body := strings.TrimSpace(nativeReport)
			if body == "" {
				body = "No terminal report was returned. Inspect the chat and artifacts before deciding the next action."
			}
			if _, e = q.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at,run_id) VALUES(?,?,?,'cmdrd',?,?,?)`, pid, bead, "native-exit:"+fmt.Sprint(rid), history+"\n"+body, time.Now().UTC().Format(time.RFC3339Nano), rid); e != nil {
				return e
			}
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
		if expectedRole != "" && summary == "" {
			summary = strings.TrimSpace(nativeReport)
			if summary == "" {
				summary = "Execution exited without a recorded final report. Inspect its chat and artifacts before deciding the next action."
			}
			if status == "native-error" {
				summary = "Execution ended with an error. " + summary
			}
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
	})
}
