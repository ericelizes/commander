package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// insertClaimedRun binds the new execution and its bead in one transaction.
// A live previous claim cannot be stolen by another dispatch.
func (s *Store) insertClaimedRun(ctx context.Context, r AgentRun) (int64, error) {
	var id int64
	e := s.WithTx(ctx, func(q Querier) error {
		var prior sql.NullInt64
		var status string
		if e := q.QueryRowContext(ctx, `SELECT active_run_id,status FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, r.ProjectID, r.TaskBeadID).Scan(&prior, &status); e != nil {
			return fmt.Errorf("claim target unavailable: %w", e)
		}
		if status == "closed" || status == "deferred" {
			return errors.New("cannot dispatch a closed or deferred bead")
		}
		if prior.Valid {
			var old string
			if e := q.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, prior.Int64).Scan(&old); e != nil {
				return e
			}
			if old == "running" {
				return errors.New("bead already has a running claim")
			}
		}
		if e := q.QueryRowContext(ctx, `INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,container_id,tmux_pane_id,spawn_time,tokens_used,usd_used,consecutive_failures,status,worktree_path) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, r.ProjectID, r.TaskBeadID, r.AgentName, r.OpencodeSessionID, r.ContainerID, r.TmuxPaneID, r.SpawnTime, r.TokensUsed, r.UsdUsed, r.ConsecutiveFailures, r.Status, r.WorktreePath).Scan(&id); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `UPDATE agent_state SET state='unknown',detail='',since=?,updated=? WHERE replace(role,'.','-')=replace(?::text,'.','-')`, time.Now().Unix(), time.Now().Unix(), r.AgentName); e != nil {
			return e
		}
		_, e := q.ExecContext(ctx, `UPDATE bead_tasks SET active_run_id=?,assigned_agent=?,status_kind='',status_text='',status_role='',status_session='',status_seq=0,status_observed_at=0 WHERE project_id=? AND id=?`, id, r.AgentName, r.ProjectID, r.TaskBeadID)
		return e
	})
	return id, e
}

// SetAgentStateForSession fences late hooks from a replaced Operator. Legacy
// callers without a session remain supported only before Postgres cutover.
func (s *Store) SetAgentStateForSession(ctx context.Context, role, session, state, detail string) error {
	return s.SetAgentStateForRun(ctx, role, 0, session, state, detail)
}

func (s *Store) SetAgentStateForRun(ctx context.Context, role string, runID int64, session, state, detail string) error {
	if !s.postgres || !strings.HasPrefix(role, "operator-") {
		return s.SetAgentState(ctx, role, state, detail)
	}
	if session == "" {
		return errors.New("agent update requires a run session")
	}
	return s.WithTx(ctx, func(q Querier) error {
		var current int64
		var currentSession, status string
		e := q.QueryRowContext(ctx, `SELECT r.id,r.opencode_session_id,r.status FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE replace(r.agent_name,'.','-')=replace(?::text,'.','-') ORDER BY r.spawn_time DESC,r.id DESC LIMIT 1 FOR UPDATE OF b,r`, role).Scan(&current, &currentSession, &status)
		if e != nil {
			return fmt.Errorf("agent claim unavailable: %w", e)
		}
		if currentSession != session || status != "running" || (runID > 0 && runID != current) {
			return errors.New("agent update belongs to an inactive or replaced run")
		}
		if _, e = q.ExecContext(ctx, `DELETE FROM agent_state WHERE role<>? AND replace(role,'.','-')=replace(?::text,'.','-')`, role, role); e != nil {
			return e
		}
		now := time.Now().Unix()
		_, e = q.ExecContext(ctx, `INSERT INTO agent_state(role,state,detail,since,updated) VALUES(?,?,?,?,?) ON CONFLICT(role) DO UPDATE SET since=CASE WHEN agent_state.state=excluded.state THEN agent_state.since ELSE excluded.since END,state=excluded.state,detail=excluded.detail,updated=excluded.updated`, role, state, detail, now, now)
		return e
	})
}

// RestoreRunClaims admits only unambiguous imported running assignments.
func (s *Store) RestoreRunClaims(ctx context.Context) error {
	return s.WithTx(ctx, func(q Querier) error {
		var missing bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r LEFT JOIN bead_tasks b ON b.project_id=r.project_id AND b.id=r.task_bead_id WHERE r.status='running' AND (b.id IS NULL OR b.deleted_at IS NOT NULL))`).Scan(&missing); e != nil {
			return e
		}
		if missing {
			return errors.New("running imported assignment has no bead; reconcile before cutover")
		}
		var duplicate bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE status='running' GROUP BY project_id,task_bead_id HAVING COUNT(*)>1)`).Scan(&duplicate); e != nil {
			return e
		}
		if duplicate {
			return errors.New("multiple imported running claims on a bead; reconcile process evidence before cutover")
		}
		_, e := q.ExecContext(ctx, `UPDATE bead_tasks b SET active_run_id=r.id,assigned_agent=r.agent_name FROM agent_runs r WHERE r.project_id=b.project_id AND r.task_bead_id=b.id AND r.status='running'`)
		return e
	})
}
