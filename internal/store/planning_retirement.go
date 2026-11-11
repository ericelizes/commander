package store

import (
	"context"
	"errors"
)

// PlanningRetirementCandidates come only from explicit consolidation receipts.
// Prefixes and the absence of a running process are never retirement authority.
func (s *Store) PlanningRetirementCandidates(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT c.role FROM jurisdiction_consolidations j CROSS JOIN LATERAL jsonb_array_elements_text(j.response->'retired_planning_candidates') AS c(role) WHERE NOT EXISTS(SELECT 1 FROM planning_runtime_retirements r WHERE r.agent_id=c.role) ORDER BY c.role LIMIT 128`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var role string
		if err = rows.Scan(&role); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

// RetirePlanningRuntime holds ownership and execution writes out while the
// trusted local runtime attests and stops one obsolete logical host. The rare
// migration step is bounded by ctx; it never changes a bead, decision or run.
func (s *Store) RetirePlanningRuntime(ctx context.Context, role string, stop func(context.Context) (string, error)) (bool, error) {
	if !s.postgres || stop == nil {
		return false, errors.New("planning retirement requires PostgreSQL and a runtime attestor")
	}
	retired := false
	err := s.WithTx(ctx, func(q Querier) error {
		var mode string
		if err := q.QueryRowContext(ctx, `SELECT mode FROM orchestration_control WHERE id=1 FOR SHARE`).Scan(&mode); err != nil {
			return err
		}
		if mode != "active" {
			return nil
		}
		if _, err := q.ExecContext(ctx, `LOCK TABLE agent_runs,commander_jurisdictions,jurisdiction_projects,projects IN SHARE MODE`); err != nil {
			return err
		}
		var eligible bool
		if err := q.QueryRowContext(ctx, `SELECT
   EXISTS(SELECT 1 FROM jurisdiction_consolidations WHERE jsonb_exists(response->'retired_planning_candidates',?))
   AND NOT EXISTS(SELECT 1 FROM planning_runtime_retirements WHERE agent_id=?)
   AND NOT commander_identity_owns(?)
   AND NOT EXISTS(SELECT 1 FROM agent_runs WHERE status='running' AND replace(agent_name,'.','-')=replace(? ,'.','-'))`, role, role, role, role).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return nil
		}
		session, err := stop(ctx)
		if err != nil {
			return err
		}
		if session == "" {
			return errors.New("runtime retirement did not attest a session or absence")
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO planning_runtime_retirements(agent_id,session_id) VALUES(?,?) ON CONFLICT(agent_id) DO NOTHING`, role, session); err != nil {
			return err
		}
		retired = true
		return nil
	})
	return retired, err
}
