package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// RedirectFactKind marks the bead comment recording that a running execution
// was told its bead changed.
const RedirectFactKind = "execution-redirect"

// PlanChange is a running execution whose bead changed after it was claimed.
type PlanChange struct {
	ProjectID       int64
	ProjectPath     string
	BeadID          string
	RunID           int64
	Agent           string
	Status          string
	ClaimedRevision int64
	CurrentRevision int64
}

// ChangedExecutionPlans lists running native executions whose bead's plan
// revision moved past the one they claimed, and that were not yet told about
// that revision.
func (s *Store) ChangedExecutionPlans(ctx context.Context) ([]PlanChange, error) {
	if !s.postgres {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT x.project_id,p.path,x.bead_id,x.run_id,r.agent_name,b.status,x.plan_revision,b.plan_revision
 FROM execution_slots x
 JOIN agent_runs r ON r.id=x.run_id
 JOIN bead_tasks b ON b.project_id=x.project_id AND b.id=x.bead_id
 JOIN projects p ON p.id=x.project_id
 WHERE r.status='running' AND r.execution_driver='native' AND b.plan_revision>x.plan_revision
  AND NOT EXISTS(SELECT 1 FROM bead_comments c WHERE c.project_id=x.project_id AND c.bead_id=x.bead_id
   AND c.fact_kind=? AND c.run_id=x.run_id AND (c.fact_value->>'plan_revision')::bigint>=b.plan_revision)
 ORDER BY x.project_id,x.bead_id`, RedirectFactKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanChange
	for rows.Next() {
		var c PlanChange
		if err := rows.Scan(&c.ProjectID, &c.ProjectPath, &c.BeadID, &c.RunID, &c.Agent, &c.Status, &c.ClaimedRevision, &c.CurrentRevision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordExecutionRedirect notes on the bead that its running execution was
// told about c.CurrentRevision, so it is told once per revision.
func (s *Store) RecordExecutionRedirect(ctx context.Context, c PlanChange, text string) error {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	value, _ := json.Marshal(map[string]int64{"plan_revision": c.CurrentRevision, "claimed_revision": c.ClaimedRevision})
	_, err := s.db.ExecContext(ctx, `INSERT INTO bead_comments(project_id,bead_id,id,author,text,created_at,run_id,fact_kind,fact_definition,fact_value) VALUES(?,?,?,?,?,?,?,?,?,?::jsonb)`,
		c.ProjectID, c.BeadID, hex.EncodeToString(id[:]), "cmdrd", text, time.Now().UTC().Format(time.RFC3339Nano), c.RunID, RedirectFactKind, "", string(value))
	return err
}
