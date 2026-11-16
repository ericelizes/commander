package store

import (
	"context"
	"encoding/json"
)

// ExecutionStatus exposes current custody and the latest run's observed liveness.
// Unknown process presence is deliberately distinct from an ended execution.
func (s *Store) ExecutionStatus(ctx context.Context, pid int64, bead string) (map[string]any, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT jsonb_build_object(
 'project_id',b.project_id,'bead_id',b.id,'plan_revision',b.plan_revision,
 'planning_commander',commander_planning_owner(b.project_id,b.id),
 'jurisdiction_revision',COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=b.project_id),1),
 'ready',execution_ready,'capabilities',execution_capabilities,'attempts',execution_attempts,
 'retry_at',execution_due_at,'error',execution_error,'active_run_id',active_run_id,
 'run',CASE WHEN r.id IS NULL THEN NULL ELSE jsonb_build_object('id',r.id,'session_id',r.opencode_session_id,'status',r.status,
 'liveness',CASE WHEN r.status<>'running' THEN 'ended'
 WHEN EXISTS(SELECT 1 FROM run_observations o WHERE o.run_id=r.id AND o.alive AND o.checked_at>=EXTRACT(EPOCH FROM clock_timestamp())::bigint-45)
 OR EXISTS(SELECT 1 FROM native_presence n WHERE n.run_id=r.id AND n.session_id=r.opencode_session_id AND n.alive AND n.checked_at>=EXTRACT(EPOCH FROM clock_timestamp())::bigint-45) THEN 'alive' ELSE 'unknown' END,
 'ended_at',c.fact_value->'ended_at','reason',COALESCE(c.fact_value->>'reason',NULLIF(r.status,'running')),
 'last_observed_at',(SELECT MAX(at) FROM (SELECT checked_at AS at FROM run_observations WHERE run_id=r.id UNION ALL SELECT checked_at FROM native_presence WHERE run_id=r.id AND session_id=r.opencode_session_id) observations)) END)
 FROM bead_tasks b LEFT JOIN LATERAL(SELECT * FROM agent_runs ar WHERE ar.project_id=b.project_id AND ar.task_bead_id=b.id ORDER BY (ar.id=b.active_run_id) DESC NULLS LAST,ar.id DESC LIMIT 1)r ON TRUE
 LEFT JOIN bead_comments c ON c.project_id=b.project_id AND c.bead_id=b.id AND c.run_id=r.id AND c.id='native-exit:'||r.id::text
 WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL`, pid, bead).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}
