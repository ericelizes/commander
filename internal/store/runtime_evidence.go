package store

import (
	"context"
	"encoding/json"
	"time"
)

type NativeRuntimeObservation struct {
	Role      string `json:"role"`
	RunID     int64  `json:"run_id"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
}

// RecordRuntimeEvidence records exact positive native custody. Absence from an
// engine is not death: a detached host survives daemon replacement. Native
// evidence expires unless refreshed; physical exit clears its exact identity.
func (s *Store) RecordRuntimeEvidence(ctx context.Context, legacy map[int64]bool, checked time.Time, native []NativeRuntimeObservation) error {
	return s.WithTx(ctx, func(q Querier) error {
		if legacy == nil {
			if _, e := q.ExecContext(ctx, `UPDATE run_observations o SET checked_at=0 FROM agent_runs r WHERE r.id=o.run_id AND r.execution_driver<>'native' AND o.checked_at<>0`); e != nil {
				return e
			}
		} else {
			type row struct {
				ID    int64 `json:"id"`
				Alive bool  `json:"alive"`
			}
			rows := []row{}
			for id, alive := range legacy {
				rows = append(rows, row{id, alive})
			}
			raw, e := json.Marshal(rows)
			if e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, `INSERT INTO run_observations(run_id,alive,checked_at) SELECT r.id,x.alive,? FROM jsonb_to_recordset(?::jsonb) x(id bigint,alive boolean) JOIN agent_runs r ON r.id=x.id WHERE r.status='running' AND r.execution_driver<>'native' AND (SELECT COUNT(*) FROM agent_runs other WHERE replace(other.agent_name,'.','-')=replace(r.agent_name,'.','-') AND other.status='running')=1 ON CONFLICT(run_id) DO UPDATE SET alive=excluded.alive,checked_at=excluded.checked_at WHERE run_observations.checked_at<=excluded.checked_at AND (run_observations.alive<>excluded.alive OR run_observations.checked_at<?)`, checked.Unix(), string(raw), checked.Add(-30*time.Second).Unix()); e != nil {
				return e
			}
		}
		if native == nil {
			native = []NativeRuntimeObservation{}
		}
		raw, e := json.Marshal(native)
		if e != nil {
			return e
		}
		now := time.Now().Unix()
		// The current bead/run/session join rejects late old-host observations and
		// prevents a planner with the same role from standing in for an executor.
		valid := `FROM jsonb_to_recordset(?::jsonb) x(role text,run_id bigint,session_id text,pid bigint) JOIN agent_runs r ON r.id=x.run_id JOIN bead_tasks b ON b.active_run_id=r.id AND b.project_id=r.project_id AND b.id=r.task_bead_id WHERE x.pid>0 AND r.status='running' AND r.opencode_session_id=x.session_id AND replace(r.agent_name,'.','-')=replace(x.role,'.','-') AND b.deleted_at IS NULL`
		if _, e = q.ExecContext(ctx, `INSERT INTO run_observations(run_id,alive,checked_at) SELECT r.id,TRUE,? `+valid+` ON CONFLICT(run_id) DO UPDATE SET alive=TRUE,checked_at=excluded.checked_at WHERE NOT run_observations.alive OR run_observations.checked_at<?`, now, string(raw), now-30); e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO native_presence(role,alive,checked_at,run_id,session_id) SELECT r.agent_name,TRUE,?,r.id,x.session_id `+valid+` ON CONFLICT(role) DO UPDATE SET alive=TRUE,checked_at=excluded.checked_at,run_id=excluded.run_id,session_id=excluded.session_id WHERE NOT native_presence.alive OR native_presence.run_id IS DISTINCT FROM excluded.run_id OR native_presence.session_id<>excluded.session_id OR native_presence.checked_at<?`, now, string(raw), now-30); e != nil {
			return e
		}
		// A logical conversation is kept separately and cannot replace a live
		// execution identity in the same role's presence record.
		_, e = q.ExecContext(ctx, `INSERT INTO native_presence(role,alive,checked_at,run_id,session_id) SELECT x.role,TRUE,?,NULL,x.session_id FROM jsonb_to_recordset(?::jsonb) x(role text,run_id bigint,session_id text,pid bigint) WHERE x.run_id=0 AND x.pid>0 AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.status='running' AND replace(r.agent_name,'.','-')=replace(x.role,'.','-')) ON CONFLICT(role) DO UPDATE SET alive=TRUE,checked_at=excluded.checked_at,run_id=NULL,session_id=excluded.session_id WHERE native_presence.run_id IS NULL AND (NOT native_presence.alive OR native_presence.session_id<>excluded.session_id OR native_presence.checked_at<?)`, now, string(raw), now-30)
		return e
	})
}

type ObservedExecution struct {
	RunID, ProjectID                       int64
	Role, SessionID, BeadID, State, Detail string
	Known, Alive                           bool
	Since                                  int64
	Path                                   []string
}

// ObservedExecutions is the authoritative execution set for counts and
// spinners. Chat activity and a newly inserted claim alone are not evidence.
func (s *Store) ObservedExecutions(ctx context.Context) ([]ObservedExecution, error) {
	rows, e := s.db.QueryContext(ctx, `WITH RECURSIVE path(run_id,id,parent_id,depth,seen) AS (
 SELECT r.id,b.id,b.parent_id,0,ARRAY[b.id] FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE r.status='running' AND b.deleted_at IS NULL
 UNION ALL SELECT path.run_id,b.id,b.parent_id,path.depth+1,path.seen||b.id FROM path JOIN agent_runs r ON r.id=path.run_id JOIN bead_tasks b ON b.project_id=r.project_id AND b.id=path.parent_id WHERE NOT b.id=ANY(path.seen))
 SELECT r.id,r.project_id,r.agent_name,r.opencode_session_id,r.task_bead_id,COALESCE(o.checked_at,0)>=?,COALESCE(o.alive,FALSE),COALESCE(a.state,'unknown'),COALESCE(a.detail,''),COALESCE(a.since,0),COALESCE((SELECT jsonb_agg(p.id ORDER BY p.depth) FROM path p WHERE p.run_id=r.id),'[]'::jsonb)
 FROM agent_runs r JOIN bead_tasks b ON b.active_run_id=r.id AND b.project_id=r.project_id AND b.id=r.task_bead_id LEFT JOIN run_observations o ON o.run_id=r.id LEFT JOIN LATERAL(SELECT state,detail,since FROM agent_state a WHERE replace(a.role,'.','-')=replace(r.agent_name,'.','-') ORDER BY updated DESC LIMIT 1)a ON TRUE WHERE r.status='running' AND b.deleted_at IS NULL ORDER BY r.id`, time.Now().Add(-45*time.Second).Unix())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ObservedExecution{}
	for rows.Next() {
		var x ObservedExecution
		var path []byte
		if e = rows.Scan(&x.RunID, &x.ProjectID, &x.Role, &x.SessionID, &x.BeadID, &x.Known, &x.Alive, &x.State, &x.Detail, &x.Since, &path); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(path, &x.Path); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
