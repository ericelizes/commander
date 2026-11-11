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
	if r.ExecutionDriver == "native" && (r.ExecutionClaimToken == "" || (r.ExecutionRuntime != "claude" && r.ExecutionRuntime != "codex") || r.ExecutionAccount == "" || strings.TrimSpace(r.ExecutionModel) == "" || r.OpencodeSessionID == "") {
		return 0, errors.New("native execution requires a startup claim and complete session/provider binding")
	}
	e := s.WithTx(ctx, func(q Querier) error {
		// Hold the mode through run binding. A concurrent cutover/drain cannot
		// commit between this admission decision and the execution insert.
		var mode string
		if e := q.QueryRowContext(ctx, `SELECT mode FROM orchestration_control WHERE id=1 FOR SHARE`).Scan(&mode); e != nil {
			return e
		}
		if e := checkExecutionAdmission(mode, r.ExecutionClaimToken); e != nil {
			return e
		}

		var claimAgent string
		var claimRevision, claimEpoch int64
		var workstreamCapacity, sourceRun int64
		var claimCapabilities []byte
		if r.ExecutionClaimToken != "" {
			var pid, lease int64
			var bead string
			var run sql.NullInt64
			var enabled bool
			e := q.QueryRowContext(ctx, `SELECT agent_id,project_id,bead_id,plan_revision,responsibility_epoch,lease_until,run_id,enabled,capabilities,workstream_capacity,COALESCE(resume_run_id,0) FROM execution_slots WHERE claim_token=? FOR UPDATE`, r.ExecutionClaimToken).Scan(&claimAgent, &pid, &bead, &claimRevision, &claimEpoch, &lease, &run, &enabled, &claimCapabilities, &workstreamCapacity, &sourceRun)
			if e != nil {
				return fmt.Errorf("execution startup claim unavailable: %w", e)
			}
			if pid != r.ProjectID || bead != r.TaskBeadID || lease <= time.Now().Unix() || run.Valid || !enabled {
				return errors.New("execution startup claim expired, disabled, bound or belongs to another bead")
			}
		}
		var prior sql.NullInt64
		// Plan/hold/jurisdiction edits use this lock before touching ancestry.
		// Validate launch authority against one committed tree version.
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, r.ProjectID); e != nil {
			return e
		}
		var status string
		if e := q.QueryRowContext(ctx, `SELECT active_run_id,status FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL FOR UPDATE`, r.ProjectID, r.TaskBeadID).Scan(&prior, &status); e != nil {
			return fmt.Errorf("claim target unavailable: %w", e)
		}
		if status == "closed" || status == "deferred" {
			return errors.New("cannot dispatch a closed or deferred bead")
		}
		if r.ExecutionClaimToken != "" {
			var revision, epoch, currentSource int64
			var ready, allowed bool
			if e := q.QueryRowContext(ctx, `SELECT b.plan_revision,b.responsibility_epoch,b.execution_ready,COALESCE(b.resume_run_id,0),
			 commander_execution_allowed(b.project_id,b.id) AND b.result_run_id IS NULL AND p.status='active' AND jsonb_exists(?::jsonb,p.type) AND b.execution_capabilities <@ ?::jsonb
			 AND (SELECT mode='active' FROM orchestration_control WHERE id=1)
			 AND commander_execution_capacity_allowed(b.project_id,b.id,?,?,?)
			 AND NOT EXISTS(SELECT 1 FROM bead_tasks child WHERE child.project_id=b.project_id AND child.parent_id=b.id AND child.status NOT IN('closed','deferred') AND child.deleted_at IS NULL)
			 FROM bead_tasks b JOIN projects p ON p.id=b.project_id WHERE b.project_id=? AND b.id=? FOR UPDATE OF p`, string(claimCapabilities), string(claimCapabilities), workstreamCapacity, time.Now().Unix(), claimAgent, r.ProjectID, r.TaskBeadID).Scan(&revision, &epoch, &ready, &currentSource, &allowed); e != nil {
				return e
			}
			if revision != claimRevision || epoch != claimEpoch || currentSource != sourceRun || r.ResumeSourceRunID != sourceRun || !ready || !allowed {
				return errors.New("execution plan or authority changed before launch")
			}
		} else {
			var reserved bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_slots WHERE project_id=? AND bead_id=? AND run_id IS NULL AND lease_until>?)`, r.ProjectID, r.TaskBeadID, time.Now().Unix()).Scan(&reserved); e != nil {
				return e
			}
			if reserved {
				return errors.New("bead has reserved execution capacity; launch requires its claim token")
			}
		}
		if sourceRun > 0 {
			var sourceSession, sourceAgent string
			var sourceProject int64
			var sourceBead string
			var sourceDriver, sourceRuntime, sourceAccount string
			if e := q.QueryRowContext(ctx, `SELECT execution_driver,execution_runtime,execution_account FROM agent_runs WHERE id=?`, sourceRun).Scan(&sourceDriver, &sourceRuntime, &sourceAccount); e != nil {
				return e
			}
			if r.ExecutionDriver == "native" && sourceDriver == "native" && (r.ExecutionRuntime != sourceRuntime || r.ExecutionAccount != sourceAccount) {
				return errors.New("native checkpoint provider/account binding changed without a validated handoff")
			}

			if e := q.QueryRowContext(ctx, `SELECT project_id,task_bead_id,agent_name,opencode_session_id FROM agent_runs WHERE id=?`, sourceRun).Scan(&sourceProject, &sourceBead, &sourceAgent, &sourceSession); e != nil {
				return e
			}
			if sourceProject != r.ProjectID || sourceBead != r.TaskBeadID || sourceSession != r.OpencodeSessionID || strings.ReplaceAll(sourceAgent, ".", "-") != strings.ReplaceAll(r.AgentName, ".", "-") {
				return errors.New("resumed run does not match its checkpoint identity")
			}
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
		if e := q.QueryRowContext(ctx, `INSERT INTO agent_runs(project_id,task_bead_id,agent_name,opencode_session_id,container_id,tmux_pane_id,spawn_time,tokens_used,usd_used,consecutive_failures,status,worktree_path,execution_driver,execution_runtime,execution_account,execution_model) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, r.ProjectID, r.TaskBeadID, r.AgentName, r.OpencodeSessionID, r.ContainerID, r.TmuxPaneID, r.SpawnTime, r.TokensUsed, r.UsdUsed, r.ConsecutiveFailures, r.Status, r.WorktreePath, r.ExecutionDriver, r.ExecutionRuntime, r.ExecutionAccount, r.ExecutionModel).Scan(&id); e != nil {
			return e
		}
		if _, e := q.ExecContext(ctx, `UPDATE agent_state SET state='unknown',detail='',since=?,updated=? WHERE replace(role,'.','-')=replace(?::text,'.','-')`, time.Now().Unix(), time.Now().Unix(), r.AgentName); e != nil {
			return e
		}
		_, e := q.ExecContext(ctx, `UPDATE bead_tasks SET active_run_id=?,assigned_agent=?,execution_ready=FALSE,resume_run_id=NULL,resume_plan_revision=0,status_kind='',status_text='',status_role='',status_session='',status_seq=0,status_observed_at=0 WHERE project_id=? AND id=?`, id, r.AgentName, r.ProjectID, r.TaskBeadID)
		if e != nil {
			return e
		}
		if claimAgent != "" {
			_, e = q.ExecContext(ctx, `UPDATE execution_slots SET run_id=?,lease_until=0,plan_revision=(SELECT plan_revision FROM bead_tasks WHERE project_id=? AND id=?) WHERE agent_id=? AND claim_token=?`, id, r.ProjectID, r.TaskBeadID, claimAgent, r.ExecutionClaimToken)
		}
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
	if !s.postgres {
		return s.SetAgentState(ctx, role, state, detail)
	}
	// A run is fenced by immutable identity, independent of its display role.
	// Logical planning chats without any execution history retain their existing
	// hook path until they acquire an execution claim of their own.
	if runID == 0 && !strings.HasPrefix(role, "operator-") {
		var execution bool
		if e := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE replace(agent_name,'.','-')=replace(?::text,'.','-'))`, role).Scan(&execution); e != nil {
			return e
		}
		if !execution {
			return s.SetAgentState(ctx, role, state, detail)
		}
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
