package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ClassificationJob struct {
	Role, SessionID, BeadID, Evidence     string
	ProjectID, SourceSeq, Attempts, RunID int64
}

// ObserveClassification records bounded new evidence and coalesces one pending
// job per role. The source cursor makes event replay harmless.
func (s *Store) ObserveClassification(ctx context.Context, role, session string, seq int64, text string, enqueue bool) error {
	if !s.postgres || session == "" {
		return nil
	}
	if len(text) > 6000 {
		text = text[len(text)-6000:]
	}
	now := time.Now().Unix()
	return s.WithTx(ctx, func(q Querier) error {
		res, e := q.ExecContext(ctx, `INSERT INTO agent_sessions(role,session_id,source_seq,evidence,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(role) DO UPDATE SET session_id=excluded.session_id,source_seq=excluded.source_seq,evidence=RIGHT(CASE WHEN agent_sessions.session_id=excluded.session_id THEN agent_sessions.evidence ELSE '' END || E'\n' || excluded.evidence,12000),updated_at=excluded.updated_at WHERE excluded.source_seq>agent_sessions.source_seq`, role, session, seq, text, now)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		if !enqueue {
			return nil
		}
		var pid, runID int64
		var bead string
		e = q.QueryRowContext(ctx, `SELECT project_id,workstream_bead_id FROM workstream_managers WHERE am_role=?`, role).Scan(&pid, &bead)
		if errors.Is(e, sql.ErrNoRows) {
			e = q.QueryRowContext(ctx, `SELECT r.project_id,r.task_bead_id,r.id FROM agent_runs r JOIN bead_tasks b ON b.active_run_id=r.id WHERE (r.agent_name=? OR replace(r.agent_name,'.','-')=replace(?::text,'.','-')) AND r.opencode_session_id=? ORDER BY r.spawn_time DESC,r.id DESC LIMIT 1`, role, role, session).Scan(&pid, &bead, &runID)
		}
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO classification_jobs(role,session_id,source_seq,project_id,bead_id,run_id,evidence,state,updated_at) SELECT role,session_id,source_seq,?,?,?,evidence,'pending',? FROM agent_sessions WHERE role=? ON CONFLICT(role) DO UPDATE SET session_id=excluded.session_id,source_seq=excluded.source_seq,project_id=excluded.project_id,bead_id=excluded.bead_id,run_id=excluded.run_id,evidence=excluded.evidence,state='pending',lease_until=0,attempts=0,updated_at=excluded.updated_at`, pid, bead, runID, now, role)
		return e
	})
}

func (s *Store) LeaseClassification(ctx context.Context) (*ClassificationJob, error) {
	var job ClassificationJob
	now := time.Now().Unix()
	e := s.WithTx(ctx, func(q Querier) error {
		e := q.QueryRowContext(ctx, `SELECT role,session_id,source_seq,project_id,bead_id,evidence,attempts,run_id FROM classification_jobs WHERE ((state='pending' AND updated_at<=?) OR (state='leased' AND lease_until<?)) AND attempts<3 ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 1`, now-2, now).Scan(&job.Role, &job.SessionID, &job.SourceSeq, &job.ProjectID, &job.BeadID, &job.Evidence, &job.Attempts, &job.RunID)
		if e != nil {
			return e
		}
		job.Attempts++
		_, e = q.ExecContext(ctx, `UPDATE classification_jobs SET state='leased',lease_until=?,attempts=? WHERE role=?`, now+90, job.Attempts, job.Role)
		return e
	})
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return &job, e
}

// ReserveClassification charges a conservative token reservation BEFORE a
// child starts. Unknown outcomes retain it; restart cannot erase the allowance.
func (s *Store) ReserveClassification(ctx context.Context, key, runtime, account, model string, monthlyTokens int64) (bool, error) {
	allowed := false
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	e := s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031823)`); e != nil {
			return e
		}
		var used int64
		if e := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM classification_usage WHERE occurred_at>=?`, start).Scan(&used); e != nil {
			return e
		}
		if used+32768 > monthlyTokens {
			return nil
		}
		res, e := q.ExecContext(ctx, `INSERT INTO classification_usage(job_key,runtime,account,model,input_tokens,output_tokens,outcome,occurred_at) VALUES(?,?,?,?,32768,0,'reserved',?) ON CONFLICT DO NOTHING`, key, runtime, account, model, now.Unix())
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		allowed = n > 0
		return e
	})
	return allowed, e
}

func (s *Store) SettleClassificationUsage(ctx context.Context, key string, input, output, cached int64, outcome string) error {
	if input < 0 || output < 0 || cached < 0 {
		return errors.New("invalid classification usage")
	}
	// Only a provider response containing usage can reduce a reservation.
	if input+output == 0 {
		_, e := s.db.ExecContext(ctx, `UPDATE classification_usage SET outcome=? WHERE job_key=?`, outcome, key)
		return e
	}
	_, e := s.db.ExecContext(ctx, `UPDATE classification_usage SET input_tokens=?,output_tokens=?,cached_tokens=?,outcome=? WHERE job_key=?`, input, output, cached, outcome, key)
	return e
}

func (s *Store) CompleteClassification(ctx context.Context, j ClassificationJob, kind, text string) error {
	return s.WithTx(ctx, func(q Querier) error {
		var seq int64
		var session string
		e := q.QueryRowContext(ctx, `SELECT source_seq,session_id FROM classification_jobs WHERE role=? FOR UPDATE`, j.Role).Scan(&seq, &session)
		if e != nil {
			return e
		}
		if seq != j.SourceSeq || session != j.SessionID {
			return nil
		}
		if kind != "" && strings.HasPrefix(j.Role, "operator-") {
			var currentID int64
			e = q.QueryRowContext(ctx, `SELECT active_run_id FROM bead_tasks WHERE project_id=? AND id=? FOR UPDATE`, j.ProjectID, j.BeadID).Scan(&currentID)
			if errors.Is(e, sql.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			if currentID != j.RunID {
				_, e = q.ExecContext(ctx, `UPDATE classification_jobs SET state='done',lease_until=0 WHERE role=?`, j.Role)
				return e
			}
		}
		if kind != "" {
			_, e = q.ExecContext(ctx, `UPDATE bead_tasks SET status_kind=?,status_text=?,status_role=?,status_session=?,status_seq=?,status_observed_at=? WHERE project_id=? AND id=? AND deleted_at IS NULL AND EXISTS(SELECT 1 FROM agent_sessions a WHERE a.role=? AND a.session_id=? AND a.source_seq=?)`, kind, text, j.Role, j.SessionID, j.SourceSeq, time.Now().Unix(), j.ProjectID, j.BeadID, j.Role, j.SessionID, j.SourceSeq)
			if e != nil {
				return e
			}
		}
		state := "done"
		if kind == "" {
			state = "pending"
		}
		_, e = q.ExecContext(ctx, `UPDATE classification_jobs SET state=?,lease_until=0,updated_at=? WHERE role=?`, state, time.Now().Unix(), j.Role)
		return e
	})
}

func (j ClassificationJob) Key(provider string) string {
	return fmt.Sprintf("%s:%s:%d:%d:%s", j.Role, j.SessionID, j.SourceSeq, j.Attempts, provider)
}
