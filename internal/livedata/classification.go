package livedata

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type StatusEvidence struct {
	RunID      int64  `json:"run_id,omitempty"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Role       string `json:"role"`
	SessionID  string `json:"session_id"`
	Seq        int64  `json:"seq"`
	ObservedAt int64  `json:"observed_at"`
}

func applyClassifications(ctx context.Context, st *store.Store, s *Snapshot, d *Display) error {
	if st == nil || !st.IsPostgres() {
		return nil
	}
	rows, e := st.DB().QueryContext(ctx, `SELECT b.project_id,b.id,b.status_kind,b.status_text,b.status_role,b.status_session,b.status_seq,b.status_observed_at,a.run_id FROM bead_tasks b JOIN agent_sessions a ON a.role=b.status_role AND a.session_id=b.status_session AND a.source_seq=b.status_seq WHERE b.deleted_at IS NULL AND b.status<>'closed' AND b.status_text<>'' AND ((a.run_id>0 AND b.active_run_id=a.run_id) OR (a.run_id=0 AND commander_planning_owner(b.project_id,b.id)=b.status_role))`)
	if e != nil {
		return e
	}
	defer rows.Close()
	type key struct {
		project int64
		bead    string
	}
	evidence := map[key]StatusEvidence{}
	for rows.Next() {
		var k key
		var v StatusEvidence
		if e = rows.Scan(&k.project, &k.bead, &v.Kind, &v.Text, &v.Role, &v.SessionID, &v.Seq, &v.ObservedAt, &v.RunID); e != nil {
			return e
		}
		evidence[k] = v
	}
	if e = rows.Err(); e != nil {
		return e
	}
	for i := range s.Workstreams {
		ws := &s.Workstreams[i]
		var walk func([]SnapStep)
		walk = func(steps []SnapStep) {
			for j := range steps {
				if v, ok := evidence[key{ws.ProjectID, steps[j].BeadID}]; ok {
					copy := v
					steps[j].Activity = &copy
				}
				walk(steps[j].Children)
			}
		}
		walk(ws.Steps)
	}
	// Recorded needs-user cards, waits, stopped runs and invariant findings keep
	// their authoritative text. Interpretation enriches only ordinary activity.
	for i := range d.Workstreams {
		r := &d.Workstreams[i]
		pid := projectIDFor(s, r.Project)
		if r.Kind != "working" && r.Kind != "idle" && r.Kind != "quiet" {
			continue
		}
		if v, ok := evidence[key{pid, r.BeadID}]; ok && classificationCompatible(v, r.Kind, s.Executions) {
			r.Text = v.Text
			copy := v
			r.Evidence = &copy
		}
	}
	var tracks func(int64, []BoardTrack)
	tracks = func(pid int64, ts []BoardTrack) {
		for i := range ts {
			if v, ok := evidence[key{pid, ts[i].BeadID}]; ok && classificationCompatible(v, ts[i].Kind, s.Executions) {
				ts[i].Text = v.Text
				copy := v
				ts[i].Evidence = &copy
			}
			tracks(pid, ts[i].Children)
		}
	}
	for i := range d.Workstreams {
		tracks(projectIDFor(s, d.Workstreams[i].Project), d.Workstreams[i].Tracks)
	}
	return nil
}

func classificationCompatible(v StatusEvidence, kind string, runs []SnapExecution) bool {
	if v.Kind == "unknown" || v.Kind == "waiting_for_user" {
		return false
	}
	for _, run := range runs {
		if beads.OwnerLabelName(run.Role) == beads.OwnerLabelName(v.Role) {
			if run.RunID != v.RunID || (v.RunID > 0 && run.SessionID != v.SessionID) {
				continue
			}
			if kind == "working" {
				return run.State == store.AgentWorking && v.Kind == "working"
			}
			if kind == "idle" || kind == "quiet" {
				return run.State != store.AgentWorking && v.Kind != "working"
			}
		}
	}
	return false
}
