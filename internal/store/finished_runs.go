package store

import "context"

// ReportedRun is a running execution whose agent filed its "Final report:" and
// wrote nothing on the bead since.
type ReportedRun struct {
	ProjectID  int64
	BeadID     string
	RunID      int64
	Agent      string
	ReportedAt int64
}

// ReportedRuns lists running runs bound to their bead whose newest bead
// comment by that run is a Final report. A run that kept writing after its
// report is not listed until its newest comment is a report again.
func (s *Store) ReportedRuns(ctx context.Context) ([]ReportedRun, error) {
	if !s.postgres {
		return nil, nil
	}
	rows, e := s.db.QueryContext(ctx, `SELECT b.project_id,b.id,r.id,r.agent_name,c.created_at
 FROM bead_tasks b
 JOIN agent_runs r ON r.id=b.active_run_id AND r.status='running'
 JOIN LATERAL(SELECT t.text,t.created_at FROM bead_comments t WHERE t.project_id=b.project_id AND t.bead_id=b.id AND t.run_id=r.id AND COALESCE(t.fact_kind,'')=''
   ORDER BY t.created_at DESC,t.id DESC LIMIT 1) c ON TRUE
 WHERE b.deleted_at IS NULL AND lower(c.text) LIKE 'final report%'`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReportedRun
	for rows.Next() {
		var x ReportedRun
		var at string
		if e := rows.Scan(&x.ProjectID, &x.BeadID, &x.RunID, &x.Agent, &at); e != nil {
			return nil, e
		}
		x.ReportedAt = unixOf(at)
		out = append(out, x)
	}
	return out, rows.Err()
}
