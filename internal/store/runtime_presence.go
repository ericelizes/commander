package store

import (
	"context"
	"encoding/json"
	"time"
)

type RuntimePresence struct {
	Role   string
	State  string
	Detail string
}

// RecordRuntimePresence persists observations, never inferring a working turn
// from process presence. A failed inspection invalidates its prior evidence.
func (s *Store) RecordRuntimePresence(ctx context.Context, alive map[int64]bool, checked time.Time, native []string) error {
	return s.WithTx(ctx, func(q Querier) error {
		if alive == nil {
			if _, e := q.ExecContext(ctx, `UPDATE run_observations SET checked_at=0 WHERE checked_at<>0`); e != nil {
				return e
			}
		} else {
			type observation struct {
				ID    int64 `json:"id"`
				Alive bool  `json:"alive"`
			}
			rows := []observation{}
			for id, a := range alive {
				rows = append(rows, observation{id, a})
			}
			raw, e := json.Marshal(rows)
			if e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, `INSERT INTO run_observations(run_id,alive,checked_at) SELECT x.id,x.alive,? FROM jsonb_to_recordset(?::jsonb) AS x(id bigint,alive boolean) ON CONFLICT(run_id) DO UPDATE SET alive=excluded.alive,checked_at=excluded.checked_at WHERE run_observations.alive<>excluded.alive OR run_observations.checked_at<?`, checked.Unix(), string(raw), checked.Add(-30*time.Second).Unix()); e != nil {
				return e
			}
		}
		raw, e := json.Marshal(native)
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO native_presence(role,alive,checked_at) SELECT jsonb_array_elements_text(?::jsonb),TRUE,? ON CONFLICT(role) DO UPDATE SET alive=TRUE,checked_at=excluded.checked_at WHERE NOT native_presence.alive OR native_presence.checked_at<?`, string(raw), time.Now().Unix(), time.Now().Add(-30*time.Second).Unix()); e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE native_presence SET alive=FALSE,checked_at=? WHERE alive AND NOT jsonb_exists(?::jsonb,role)`, time.Now().Unix(), string(raw))
		return e
	})
}

// RuntimePresence reads only database facts. Stale process evidence is unknown.
func (s *Store) RuntimePresence(ctx context.Context) ([]string, []RuntimePresence, bool, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT r.agent_name,COALESCE(o.alive,FALSE),COALESCE(o.checked_at,0) FROM agent_runs r LEFT JOIN run_observations o ON o.run_id=r.id WHERE r.status='running'`)
	if e != nil {
		return nil, nil, false, e
	}
	alive := []string{}
	known := true
	floor := time.Now().Add(-45 * time.Second).Unix()
	for rows.Next() {
		var role string
		var yes bool
		var at int64
		if e = rows.Scan(&role, &yes, &at); e != nil {
			rows.Close()
			return nil, nil, false, e
		}
		if at < floor {
			known = false
		} else if yes {
			alive = append(alive, role)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, nil, false, e
	}
	var stale bool
	if e = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_presence WHERE alive AND checked_at<?)`, floor).Scan(&stale); e != nil {
		return nil, nil, false, e
	}
	if stale {
		known = false
	}
	rows, e = s.db.QueryContext(ctx, `SELECT p.role,COALESCE(a.state,'unknown'),COALESCE(a.detail,'') FROM native_presence p LEFT JOIN agent_state a ON a.role=p.role WHERE p.alive AND p.checked_at>=?`, floor)
	if e != nil {
		return nil, nil, false, e
	}
	defer rows.Close()
	native := []RuntimePresence{}
	for rows.Next() {
		var p RuntimePresence
		if e = rows.Scan(&p.Role, &p.State, &p.Detail); e != nil {
			return nil, nil, false, e
		}
		native = append(native, p)
	}
	return alive, native, known, rows.Err()
}
