package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// closeSupervisedRun closes execution and transfers the next move in one
// transaction. Lock order is bead, then run, matching claims and state hooks.
func (s *Store) closeSupervisedRun(ctx context.Context, rid int64, status string) error {
	return s.closeSupervisedRunIdentity(ctx, rid, status, "", "")
}

// FinishNativeExecution consumes a host-observed exit, never a model's claim
// of completion. An old receipt cannot alter a replacement run or its custody.
func (s *Store) FinishNativeExecution(ctx context.Context, role string, rid int64, session, status string) error {
	if !s.postgres || rid <= 0 || role == "" || session == "" || (status != "exited" && status != "native-error") {
		return errors.New("invalid native execution exit identity")
	}
	return s.closeSupervisedRunIdentity(ctx, rid, status, role, session)
}
func (s *Store) closeSupervisedRunIdentity(ctx context.Context, rid int64, status, expectedRole, expectedSession string) error {
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
