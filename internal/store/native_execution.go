package store

import (
	"context"
	"errors"
)

// NativeExecution is the existing canonical run and bead, not a second claim.
// No startup token is exposed to a chat model or persisted in host metadata.
type NativeExecution struct {
	RunID, ProjectID, ResumeSourceRunID                                                           int64
	BeadID, Agent, SessionID, ProjectPath, ProjectType, Worktree, Model, Driver, Runtime, Account string
	Title, Description, Acceptance, PlanningCommander                                             string
	CanRestart                                                                                    bool
}

func (s *Store) NativeExecution(ctx context.Context, role string) (*NativeExecution, error) {
	if !s.postgres {
		return nil, errors.New("native execution binding requires PostgreSQL")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT r.id,b.project_id,b.id,r.agent_name,r.opencode_session_id,p.path,p.type,COALESCE(r.worktree_path,''),COALESCE(NULLIF(r.execution_model,''),r.model),r.execution_driver,r.execution_runtime,r.execution_account,COALESCE((SELECT resume_run_id FROM execution_slots WHERE run_id=r.id),0),b.title,b.description,b.acceptance_criteria,commander_planning_owner(b.project_id,b.id),p.status='active' AND commander_execution_allowed(b.project_id,b.id,TRUE) AND b.result_run_id IS NULL FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id AND r.project_id=b.project_id AND r.task_bead_id=b.id JOIN projects p ON p.id=b.project_id WHERE replace(r.agent_name,'.','-')=replace(?::text,'.','-') AND r.status='running' AND b.deleted_at IS NULL ORDER BY r.id LIMIT 2`, role)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out *NativeExecution
	for rows.Next() {
		if out != nil {
			return nil, errors.New("native identity has multiple live execution claims; reconcile before starting it")
		}
		out = &NativeExecution{}
		if e = rows.Scan(&out.RunID, &out.ProjectID, &out.BeadID, &out.Agent, &out.SessionID, &out.ProjectPath, &out.ProjectType, &out.Worktree, &out.Model, &out.Driver, &out.Runtime, &out.Account, &out.ResumeSourceRunID, &out.Title, &out.Description, &out.Acceptance, &out.PlanningCommander, &out.CanRestart); e != nil {
			return nil, e
		}
	}
	return out, rows.Err()
}
func (s *Store) PlanningIdentityState(ctx context.Context, role string) (owns, retired bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jurisdiction_projects WHERE default_commander=?) OR EXISTS(SELECT 1 FROM commander_jurisdictions WHERE commander_id=?),EXISTS(SELECT 1 FROM jurisdiction_consolidations WHERE jsonb_exists(response->'retired_planning_candidates',?))`, role, role, role).Scan(&owns, &retired)
	return
}

// NativeExecutionRoles includes startup intents that committed before a daemon
// crash or failed host launch. No role-file or tmux-name discovery is needed.
func (s *Store) NativeExecutionRoles(ctx context.Context, now int64) ([]string, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT r.agent_name FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE r.status='running' AND r.execution_driver='native' AND b.deleted_at IS NULL AND b.execution_attempts<5 AND b.execution_due_at<=? ORDER BY r.id LIMIT 128`, now)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if e = rows.Scan(&role); e != nil {
			return nil, e
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
func (s *Store) RecordNativeStartupAttempt(ctx context.Context, role string, rid int64, session, detail string, now int64) error {
	_, e := s.db.ExecContext(ctx, `UPDATE bead_tasks b SET execution_error=?,execution_attempts=CASE WHEN ?='' THEN 0 ELSE execution_attempts+1 END,execution_due_at=CASE WHEN ?='' THEN 0 ELSE ?+60 END FROM agent_runs r WHERE r.id=b.active_run_id AND r.id=? AND r.agent_name=? AND r.opencode_session_id=? AND r.status='running' AND r.execution_driver='native'`, truncateSupervision(detail, 1000), detail, detail, now, rid, role, session)
	return e
}
