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
	return s.observeClassification(ctx, role, nil, session, seq, text, enqueue)
}

// ObserveClassificationForRun consumes a chat event with its captured origin.
// Session reuse and role spelling never confer a replacement run's authority.
func (s *Store) ObserveClassificationForRun(ctx context.Context, role string, runID int64, session string, seq int64, text string, enqueue bool) error {
	return s.observeClassification(ctx, role, &runID, session, seq, text, enqueue)
}

// clipEvidence keeps the last 6000 bytes of text as valid UTF-8 without NUL
// bytes: cutting inside a multi-byte character, or a NUL from tool output,
// makes PostgreSQL refuse the whole write.
func clipEvidence(text string) string {
	if len(text) > 6000 {
		text = text[len(text)-6000:]
	}
	return strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", ""), "")
}

func (s *Store) observeClassification(ctx context.Context, role string, expectedRun *int64, session string, seq int64, text string, enqueue bool) error {
	if !s.postgres || session == "" {
		return nil
	}
	text = clipEvidence(text)
	now := time.Now().Unix()
	return s.WithTx(ctx, func(q Querier) error {
		var pid, runID int64
		var bead string
		e := q.QueryRowContext(ctx, `SELECT r.project_id,r.task_bead_id,r.id FROM agent_runs r JOIN bead_tasks b ON b.active_run_id=r.id AND b.project_id=r.project_id AND b.id=r.task_bead_id WHERE replace(r.agent_name,'.','-')=replace(?::text,'.','-') AND r.opencode_session_id=? AND b.deleted_at IS NULL ORDER BY r.id DESC LIMIT 1 FOR UPDATE OF b`, role, session).Scan(&pid, &bead, &runID)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if expectedRun != nil {
			if *expectedRun > 0 && (errors.Is(e, sql.ErrNoRows) || runID != *expectedRun) {
				return nil
			}
			if *expectedRun == 0 {
				var execution bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN bead_tasks b ON b.active_run_id=r.id WHERE replace(r.agent_name,'.','-')=replace(?::text,'.','-'))`, role).Scan(&execution); err != nil {
					return err
				}
				if execution {
					return nil
				}
			}
		}
		if runID == 0 {
			e = q.QueryRowContext(ctx, `SELECT project_id,workstream_bead_id FROM workstream_managers WHERE am_role=?`, role).Scan(&pid, &bead)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		res, e := q.ExecContext(ctx, `INSERT INTO agent_sessions(role,session_id,source_seq,evidence,updated_at,run_id) VALUES(?,?,?,?,?,?) ON CONFLICT(role) DO UPDATE SET session_id=excluded.session_id,source_seq=excluded.source_seq,evidence=RIGHT(CASE WHEN agent_sessions.session_id=excluded.session_id AND agent_sessions.run_id=excluded.run_id THEN agent_sessions.evidence || E'\n' ELSE '' END || excluded.evidence,12000),updated_at=excluded.updated_at,run_id=excluded.run_id WHERE excluded.source_seq>agent_sessions.source_seq`, role, session, seq, text, now, runID)
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
		if bead == "" {
			return nil
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
		if j.RunID == 0 {
			var owner bool
			if e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(project_id,id)=? FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, j.Role, j.ProjectID, j.BeadID).Scan(&owner); errors.Is(e, sql.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			} else if !owner {
				return nil
			}
		}
		if j.RunID > 0 {
			var valid bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=? AND r.project_id=? AND r.task_bead_id=? AND r.opencode_session_id=? AND replace(r.agent_name,'.','-')=replace(?::text,'.','-')) FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND b.active_run_id=? AND b.deleted_at IS NULL FOR UPDATE OF b`, j.RunID, j.ProjectID, j.BeadID, j.SessionID, j.Role, j.ProjectID, j.BeadID, j.RunID).Scan(&valid); errors.Is(e, sql.ErrNoRows) {
				return nil
			} else if e != nil {
				return e
			} else if !valid {
				return nil
			}
		}
		var seq, runID int64
		var session string
		e := q.QueryRowContext(ctx, `SELECT source_seq,session_id,run_id FROM classification_jobs WHERE role=? FOR UPDATE`, j.Role).Scan(&seq, &session, &runID)
		if e != nil {
			return e
		}
		if seq != j.SourceSeq || session != j.SessionID || runID != j.RunID {
			return nil
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
