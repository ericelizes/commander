package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type ExecutionClaim struct {
	AgentID, Token, BeadID, Title, Description, Acceptance, PlanningCommander string
	Project                                                                   Project
	PlanRevision, Epoch                                                       int64
	ResumeRunID                                                               int64
}

// ConfigureExecutionSlot advertises capacity supplied by a real runtime
// adapter. Agents cannot invent capabilities in a claim request.
func (s *Store) ConfigureExecutionSlot(ctx context.Context, agent string, capabilities []string, enabled bool) error {
	if !s.postgres {
		return errors.New("execution slots require PostgreSQL")
	}
	if agent == "" || len(capabilities) == 0 {
		return errors.New("execution slot requires identity and capabilities")
	}
	for _, c := range capabilities {
		if c == "" || strings.ContainsAny(c, " \t\n") {
			return errors.New("invalid execution capability")
		}
	}
	raw, e := json.Marshal(capabilities)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO execution_slots(agent_id,capabilities,enabled) VALUES(?,?::jsonb,?) ON CONFLICT(agent_id) DO UPDATE SET capabilities=excluded.capabilities,enabled=excluded.enabled WHERE (execution_slots.capabilities,execution_slots.enabled) IS DISTINCT FROM (excluded.capabilities,excluded.enabled)`, agent, string(raw), enabled)
	return e
}

func (s *Store) SetExecutionReady(ctx context.Context, pid int64, bead, actor string, revision, jurisdictionRevision int64, ready bool, capabilities []string) (int64, error) {
	if !s.postgres {
		return 0, errors.New("execution readiness requires PostgreSQL")
	}
	var next int64
	raw, e := json.Marshal(capabilities)
	if e != nil {
		return 0, e
	}
	if capabilities == nil {
		raw = []byte("[]")
	}
	e = s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return e
		}
		var owner, description, acceptance, status string
		var current, jrev int64
		var active bool
		if e := q.QueryRowContext(ctx, `SELECT commander_planning_owner(b.project_id,b.id),b.plan_revision,COALESCE(j.revision,1),b.description,b.acceptance_criteria,b.status,EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=b.active_run_id AND r.status='running') FROM bead_tasks b LEFT JOIN jurisdiction_projects j ON j.project_id=b.project_id WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL FOR UPDATE OF b`, pid, bead).Scan(&owner, &current, &jrev, &description, &acceptance, &status, &active); e != nil {
			return e
		}
		if current != revision || jrev != jurisdictionRevision {
			return ErrJurisdictionConflict
		}
		if actor != "" && actor != owner {
			return errors.New("only the planning owner may change execution readiness")
		}
		if ready && status != "open" {
			return errors.New("only an open bead can become ready for execution")
		}
		if active {
			return errors.New("execution is already claimed; change direction through the current run")
		}
		if ready && (strings.TrimSpace(description) == "" || strings.TrimSpace(acceptance) == "") {
			return errors.New("ready work requires a brief and acceptance criteria; the owning Commander can add both with cmdr plan apply --file PATH using an update operation with --description and --acceptance, then reread execution status and mark the new revision ready")
		}
		return q.QueryRowContext(ctx, `UPDATE bead_tasks SET plan_revision=plan_revision+1,execution_ready=?,execution_capabilities=?::jsonb,execution_attempts=0,execution_due_at=0,execution_error='',result_run_id=CASE WHEN ? THEN NULL ELSE result_run_id END,result_ready_at=CASE WHEN ? THEN 0 ELSE result_ready_at END,result_summary=CASE WHEN ? THEN '' ELSE result_summary END WHERE project_id=? AND id=? RETURNING plan_revision`, ready, string(raw), ready, ready, ready, pid, bead).Scan(&next)
	})
	return next, e
}

// ClaimNextExecution reserves a ready canonical bead and one runtime slot in a
// short transaction. SKIP LOCKED lets independent slots pick different work.
// The lease is only startup custody: a bound run retains its slot until exit.
func (s *Store) ClaimNextExecution(ctx context.Context, agent string, capacity int, now time.Time, workstreamCapacity ...int) (*ExecutionClaim, error) {
	if !s.postgres {
		return nil, errors.New("execution claims require PostgreSQL")
	}
	if capacity < 1 {
		return nil, errors.New("execution capacity must be positive")
	}
	var claim *ExecutionClaim
	active, e := s.ActiveOrchestration(ctx)
	if e != nil {
		return nil, e
	}
	if !active {
		return nil, nil
	}
	perWorkstream := 8
	if len(workstreamCapacity) > 0 {
		perWorkstream = workstreamCapacity[0]
	}
	e = s.WithTx(ctx, func(q Querier) error {
		// Recheck under the same mode fence as run binding. A pre-transaction
		// observation is not authority to reserve after execution is disabled.
		var mode string
		if e := q.QueryRowContext(ctx, `SELECT mode FROM orchestration_control WHERE id=1 FOR SHARE`).Scan(&mode); e != nil {
			return e
		}
		if mode != "active" {
			return nil
		}
		var caps []byte
		var enabled bool
		var lease int64
		var run sql.NullInt64
		var token string
		e := q.QueryRowContext(ctx, `SELECT capabilities,enabled,lease_until,run_id,claim_token FROM execution_slots WHERE agent_id=? FOR UPDATE SKIP LOCKED`, agent).Scan(&caps, &enabled, &lease, &run, &token)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if !enabled {
			return nil
		}
		if run.Valid {
			var status string
			if e = q.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, run.Int64).Scan(&status); e != nil {
				return e
			}
			if status == "running" {
				return nil
			}
		} else if lease > now.Unix() {
			return nil
		}
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031824)`); e != nil {
			return e
		}
		var used int
		if e = q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM agent_runs WHERE status='running')+(SELECT COUNT(*) FROM execution_slots WHERE run_id IS NULL AND lease_until>?)`, now.Unix()).Scan(&used); e != nil {
			return e
		}
		if used >= capacity {
			return nil
		}
		// Completed or expired slot custody must not block its old bead via the
		// unique reservation index while this slot chooses another bead.
		if _, e = q.ExecContext(ctx, `UPDATE execution_slots SET project_id=NULL,bead_id=NULL,run_id=NULL,resume_run_id=NULL,claim_token='',lease_until=0 WHERE agent_id=?`, agent); e != nil {
			return e
		}
		c := ExecutionClaim{AgentID: agent, Token: supervisionToken()}
		e = q.QueryRowContext(ctx, `SELECT b.project_id,b.id,b.title,b.description,b.acceptance_criteria,b.plan_revision,b.responsibility_epoch,commander_planning_owner(b.project_id,b.id),p.name,p.path,p.type,p.repo_path,p.status
   ,COALESCE(CASE WHEN b.resume_plan_revision=b.plan_revision THEN b.resume_run_id END,0) FROM bead_tasks b JOIN projects p ON p.id=b.project_id
   WHERE p.status='active' AND b.execution_ready AND b.execution_due_at<=? AND b.execution_attempts<5 AND b.status='open' AND b.deleted_at IS NULL AND b.result_run_id IS NULL
   AND jsonb_exists(?::jsonb,p.type) AND b.execution_capabilities <@ ?::jsonb AND commander_execution_allowed(b.project_id,b.id)
   AND commander_execution_capacity_allowed(b.project_id,b.id,?,?,?)
   AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=b.active_run_id AND r.status='running')
   AND NOT EXISTS(SELECT 1 FROM bead_tasks child WHERE child.project_id=b.project_id AND child.parent_id=b.id AND child.status NOT IN('closed','deferred') AND child.deleted_at IS NULL)
   AND NOT EXISTS(SELECT 1 FROM execution_slots x WHERE x.project_id=b.project_id AND x.bead_id=b.id)
   ORDER BY commander_execution_priority(b.project_id,b.id),b.priority,b.created_at,b.project_id,b.id LIMIT 1 FOR UPDATE OF b SKIP LOCKED`, now.Unix(), string(caps), string(caps), perWorkstream, now.Unix(), agent).Scan(&c.Project.ID, &c.BeadID, &c.Title, &c.Description, &c.Acceptance, &c.PlanRevision, &c.Epoch, &c.PlanningCommander, &c.Project.Name, &c.Project.Path, &c.Project.Type, &c.Project.RepoPath, &c.Project.Status, &c.ResumeRunID)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE execution_slots SET project_id=?,bead_id=?,plan_revision=?,responsibility_epoch=?,claim_token=?,lease_until=?,run_id=NULL,resume_run_id=NULLIF(?,0),workstream_capacity=? WHERE agent_id=?`, c.Project.ID, c.BeadID, c.PlanRevision, c.Epoch, c.Token, now.Add(5*time.Minute).Unix(), c.ResumeRunID, perWorkstream, agent)
		if e == nil {
			claim = &c
		}
		return e
	})
	return claim, e
}

func (s *Store) RenewExecutionClaim(ctx context.Context, c ExecutionClaim, now time.Time) error {
	result, e := s.db.ExecContext(ctx, `UPDATE execution_slots SET lease_until=? WHERE agent_id=? AND claim_token=? AND run_id IS NULL AND lease_until>?`, now.Add(5*time.Minute).Unix(), c.AgentID, c.Token, now.Unix())
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n == 0 {
		return errors.New("execution startup claim expired or was superseded")
	}
	return e
}

// ReapExecutionStartupClaims releases expired startup leases and terminal
// bindings whose bead was explicitly made ready again, including disabled
// slots. A live run is never retired from a timeout or missing heartbeat.
func (s *Store) ReapExecutionStartupClaims(ctx context.Context, now time.Time) error {
	_, e := s.db.ExecContext(ctx, `WITH expired AS (SELECT agent_id FROM execution_slots x WHERE
 (run_id IS NULL AND claim_token<>'' AND lease_until<=?) OR
 (EXISTS(SELECT 1 FROM agent_runs r WHERE r.id=x.run_id AND r.status<>'running') AND EXISTS(SELECT 1 FROM bead_tasks b WHERE b.project_id=x.project_id AND b.id=x.bead_id AND b.execution_ready))
 ORDER BY agent_id LIMIT 128 FOR UPDATE SKIP LOCKED) UPDATE execution_slots x SET project_id=NULL,bead_id=NULL,claim_token='',lease_until=0,run_id=NULL,resume_run_id=NULL FROM expired WHERE x.agent_id=expired.agent_id`, now.Unix())
	return e
}

func (s *Store) FailExecutionClaim(ctx context.Context, c ExecutionClaim, detail string, now time.Time) error {
	return s.WithTx(ctx, func(q Querier) error {
		var pid, revision, source int64
		var bead string
		var run sql.NullInt64
		e := q.QueryRowContext(ctx, `SELECT project_id,bead_id,plan_revision,run_id,COALESCE(resume_run_id,0) FROM execution_slots WHERE agent_id=? AND claim_token=? FOR UPDATE`, c.AgentID, c.Token).Scan(&pid, &bead, &revision, &run, &source)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if run.Valid {
			var status string
			if e = q.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, run.Int64).Scan(&status); e != nil {
				return e
			}
			if status == "running" {
				return errors.New("cannot release a live execution after a launch error")
			}
		}
		if _, e = q.ExecContext(ctx, `UPDATE bead_tasks SET execution_ready=TRUE,resume_run_id=NULLIF(?,0),resume_plan_revision=CASE WHEN ?::bigint>0 THEN plan_revision ELSE 0 END,execution_attempts=execution_attempts+1,execution_error=?,execution_due_at=? WHERE project_id=? AND id=? AND plan_revision=? AND status='open' AND result_run_id IS NULL`, source, source, truncateSupervision(detail, 1000), now.Add(time.Minute).Unix(), pid, bead, revision); e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE execution_slots SET project_id=NULL,bead_id=NULL,claim_token='',lease_until=0,run_id=NULL,resume_run_id=NULL WHERE agent_id=? AND claim_token=?`, c.AgentID, c.Token)
		return e
	})
}
