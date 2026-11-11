package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type LegacyPlanningBoundary struct {
	BeadID      string `json:"bead_id"`
	CommanderID string `json:"commander_id"`
}
type LegacyPlanningInventory struct {
	ProjectID        int64                    `json:"project_id"`
	Revision         int64                    `json:"jurisdiction_revision"`
	DefaultCommander string                   `json:"default_commander"`
	ControlRevision  int64                    `json:"control_revision"`
	Mode             string                   `json:"mode"`
	Snapshot         string                   `json:"snapshot"`
	Boundaries       []LegacyPlanningBoundary `json:"legacy_scopes"`
}
type JurisdictionConsolidation struct {
	ProjectID        int64  `json:"project_id"`
	Revision         int64  `json:"jurisdiction_revision"`
	DefaultCommander string `json:"default_commander"`
	ControlRevision  int64  `json:"control_revision"`
	Snapshot         string `json:"snapshot"`
	RequestID        string `json:"request_id"`
	Reason           string `json:"reason"`
}
type JurisdictionConsolidationResult struct {
	Revision int64                    `json:"jurisdiction_revision"`
	Removed  []LegacyPlanningBoundary `json:"removed"`
	// These identities are planning-runtime cleanup candidates, not permission
	// to stop a process. Cleanup must recheck ownership and execution custody.
	RetiredPlanningCandidates []string `json:"retired_planning_candidates"`
	Replayed                  bool     `json:"replayed,omitempty"`
}
type consolidationBead struct {
	ID, Parent, Owner string
	PlanRevision      int64
	Deleted           sql.NullInt64
}

func legacyPlanningInventory(ctx context.Context, q Querier, pid int64, lock bool) (LegacyPlanningInventory, []consolidationBead, error) {
	out := LegacyPlanningInventory{ProjectID: pid, Boundaries: []LegacyPlanningBoundary{}}
	tail := ""
	if lock {
		tail = " FOR UPDATE"
	}
	var e error
	if lock {
		if _, e = q.ExecContext(ctx, `INSERT INTO jurisdiction_projects(project_id) VALUES(?) ON CONFLICT DO NOTHING`, pid); e != nil {
			return out, nil, e
		}
		e = q.QueryRowContext(ctx, `SELECT revision,default_commander FROM jurisdiction_projects WHERE project_id=? FOR UPDATE`, pid).Scan(&out.Revision, &out.DefaultCommander)
	} else {
		e = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=p.id),1),COALESCE((SELECT default_commander FROM jurisdiction_projects WHERE project_id=p.id),'commander') FROM projects p WHERE p.id=?`, pid).Scan(&out.Revision, &out.DefaultCommander)
	}
	if e != nil {
		return out, nil, e
	}
	if e = q.QueryRowContext(ctx, `SELECT revision,mode FROM orchestration_control WHERE id=1`+tail).Scan(&out.ControlRevision, &out.Mode); e != nil {
		return out, nil, e
	}
	rows, e := q.QueryContext(ctx, `SELECT bead_id,commander_id,origin FROM commander_jurisdictions WHERE project_id=? ORDER BY bead_id`+tail, pid)
	if e != nil {
		return out, nil, e
	}
	type boundary struct{ BeadID, Owner, Origin string }
	all := []boundary{}
	for rows.Next() {
		var b boundary
		if e = rows.Scan(&b.BeadID, &b.Owner, &b.Origin); e != nil {
			rows.Close()
			return out, nil, e
		}
		all = append(all, b)
		if b.Origin == "legacy" {
			out.Boundaries = append(out.Boundaries, LegacyPlanningBoundary{b.BeadID, b.Owner})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, nil, e
	}
	// The fingerprint includes actual parentage, all planning versions and every
	// boundary's provenance. Heartbeats and execution progress do not invalidate it.
	beadTail := ""
	if lock {
		beadTail = " FOR UPDATE OF b"
	}
	rows, e = q.QueryContext(ctx, `SELECT b.id,COALESCE(b.parent_id,''),b.plan_revision,b.deleted_at FROM bead_tasks b WHERE b.project_id=? ORDER BY b.id`+beadTail, pid)
	if e != nil {
		return out, nil, e
	}
	beads := []consolidationBead{}
	for rows.Next() {
		var b consolidationBead
		if e = rows.Scan(&b.ID, &b.Parent, &b.PlanRevision, &b.Deleted); e != nil {
			rows.Close()
			return out, nil, e
		}
		beads = append(beads, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, nil, e
	}
	boundaries := map[string]string{}
	for _, b := range all {
		boundaries[b.BeadID] = b.Owner
	}
	owners := resolveConsolidationOwners(beads, out.DefaultCommander, boundaries)
	for i := range beads {
		beads[i].Owner = owners[beads[i].ID]
	}
	raw, _ := json.Marshal(struct {
		Inventory  LegacyPlanningInventory
		Boundaries []boundary
		Beads      []consolidationBead
	}{out, all, beads})
	sum := sha256.Sum256(raw)
	out.Snapshot = hex.EncodeToString(sum[:])
	return out, beads, nil
}
func (s *Store) LegacyPlanningInventory(ctx context.Context, pid int64) (LegacyPlanningInventory, error) {
	if !s.postgres {
		return LegacyPlanningInventory{}, errors.New("planning consolidation requires PostgreSQL")
	}
	var out LegacyPlanningInventory
	e := s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return e
		}
		var e error
		out, _, e = legacyPlanningInventory(ctx, q, pid, false)
		return e
	})
	return out, e
}

// ConsolidateLegacyPlanning removes only imported planning boundaries. Explicit
// user transfers, conversation/session history, work trees, decisions and live
// execution claims are preserved. Runtime cleanup is a separate fenced step.
func (s *Store) ConsolidateLegacyPlanning(ctx context.Context, actor string, r JurisdictionConsolidation) (JurisdictionConsolidationResult, error) {
	out := JurisdictionConsolidationResult{}
	if !s.postgres {
		return out, errors.New("planning consolidation requires PostgreSQL")
	}
	if r.ProjectID <= 0 || r.Revision < 1 || r.ControlRevision < 1 || !commanderIdentity.MatchString(r.DefaultCommander) || len(r.Snapshot) != 64 || strings.TrimSpace(r.RequestID) == "" || len(r.RequestID) > 128 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 8000 {
		return out, errors.New("consolidation requires the exact reviewed inventory, request key and reason")
	}
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	receiptActor := actor
	if actor == "" {
		receiptActor = "user"
	}
	e := s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, r.ProjectID); e != nil {
			return e
		}
		var priorHash string
		var response []byte
		e := q.QueryRowContext(ctx, `SELECT payload_hash,response FROM jurisdiction_consolidations WHERE project_id=? AND actor=? AND request_key=?`, r.ProjectID, receiptActor, r.RequestID).Scan(&priorHash, &response)
		if e == nil {
			if priorHash != hash {
				return errors.New("consolidation request_id reused with different intent")
			}
			if e = json.Unmarshal(response, &out); e != nil {
				return e
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		current, beads, e := legacyPlanningInventory(ctx, q, r.ProjectID, true)
		if e != nil {
			return e
		}
		if actor != "" && actor != current.DefaultCommander {
			return errors.New("only the user or project default Commander may consolidate imported planning ownership")
		}
		if current.Mode != "shadow" {
			return errors.New("legacy planning consolidation requires shadow mode")
		}
		if current.Revision != r.Revision || current.DefaultCommander != r.DefaultCommander || current.ControlRevision != r.ControlRevision || current.Snapshot != r.Snapshot {
			return ErrJurisdictionConflict
		}
		if len(current.Boundaries) == 0 {
			return errors.New("no imported planning boundaries remain")
		}
		if _, e = q.ExecContext(ctx, `DELETE FROM commander_jurisdictions WHERE project_id=? AND origin='legacy'`, r.ProjectID); e != nil {
			return e
		}
		revision := current.Revision + int64(len(current.Boundaries))
		roles := map[string]bool{}
		boundariesJSON, _ := json.Marshal(current.Boundaries)
		if _, e = q.ExecContext(ctx, `INSERT INTO jurisdiction_history(project_id,bead_id,revision,actor,previous_commander,next_commander,inherit,reason) SELECT ?,x.bead_id,?+row_number() OVER(ORDER BY x.bead_id),?,x.commander_id,commander_planning_owner(?,x.bead_id),TRUE,? FROM jsonb_to_recordset(?::jsonb) AS x(bead_id TEXT,commander_id TEXT)`, r.ProjectID, current.Revision, receiptActor, r.ProjectID, r.Reason, string(boundariesJSON)); e != nil {
			return e
		}
		for _, boundary := range current.Boundaries {
			roles[boundary.CommanderID] = true
		}
		if _, e = q.ExecContext(ctx, `UPDATE jurisdiction_projects SET revision=? WHERE project_id=?`, revision, r.ProjectID); e != nil {
			return e
		}
		remaining := map[string]string{}
		rows, e := q.QueryContext(ctx, `SELECT bead_id,commander_id FROM commander_jurisdictions WHERE project_id=?`, r.ProjectID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var bead, owner string
			if e = rows.Scan(&bead, &owner); e != nil {
				rows.Close()
				return e
			}
			remaining[bead] = owner
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		nextOwners := resolveConsolidationOwners(beads, current.DefaultCommander, remaining)
		type oldOwner struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
			Next  string `json:"next"`
		}
		old := make([]oldOwner, 0, len(beads))
		for _, b := range beads {
			old = append(old, oldOwner{b.ID, b.Owner, nextOwners[b.ID]})
		}
		oldJSON, _ := json.Marshal(old)
		// Both owner maps derive from the same locked tree and explicit boundaries.
		// A row update must not recursively rescan ancestry for each historical bead.
		if _, e = q.ExecContext(ctx, `UPDATE bead_tasks b SET responsible_agent_id=CASE WHEN b.responsible_agent_id=n.owner AND NOT EXISTS(SELECT 1 FROM agent_runs a WHERE a.id=b.active_run_id AND a.status='running') THEN n.next ELSE b.responsible_agent_id END,responsibility_epoch=b.responsibility_epoch+1 FROM jsonb_to_recordset(?::jsonb) AS n(id TEXT,owner TEXT,next TEXT) WHERE b.project_id=? AND b.id=n.id AND n.owner<>n.next`, string(oldJSON), r.ProjectID); e != nil {
			return e
		}

		out.Revision = revision
		out.Removed = current.Boundaries
		out.RetiredPlanningCandidates = []string{}
		// No role-name guessing: candidate identities came from the reviewed imported
		// boundaries. A subsequent runtime retirement must still revalidate custody.
		for _, b := range current.Boundaries {
			if roles[b.CommanderID] {
				out.RetiredPlanningCandidates = append(out.RetiredPlanningCandidates, b.CommanderID)
				delete(roles, b.CommanderID)
			}
		}
		if _, e = q.ExecContext(ctx, `UPDATE projects SET state_revision=state_revision+1 WHERE id=?`, r.ProjectID); e != nil {
			return e
		}
		response, e = json.Marshal(out)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO jurisdiction_consolidations(project_id,actor,request_key,payload_hash,request,response) VALUES(?,?,?,?,?::jsonb,?::jsonb)`, r.ProjectID, receiptActor, r.RequestID, hash, string(raw), string(response))
		return e
	})
	if e != nil {
		return JurisdictionConsolidationResult{}, fmt.Errorf("planning consolidation: %w", e)
	}
	return out, nil
}

// resolveConsolidationOwners follows actual parent edges with the same deleted,
// missing-parent and cycle semantics as commander_planning_owner. Memoization
// keeps a large imported project from doing an SQL ancestry scan for every bead.
func resolveConsolidationOwners(beads []consolidationBead, defaultOwner string, boundaries map[string]string) map[string]string {
	byID := make(map[string]consolidationBead, len(beads))
	for _, b := range beads {
		byID[b.ID] = b
	}
	owners := make(map[string]string, len(beads))
	for _, start := range beads {
		if _, ok := owners[start.ID]; ok {
			continue
		}
		path := []string{}
		seen := map[string]bool{}
		owner := defaultOwner
		for id := start.ID; id != "" && !seen[id]; {
			b, ok := byID[id]
			if !ok || b.Deleted.Valid {
				break
			}
			if known, ok := owners[id]; ok {
				owner = known
				break
			}
			if direct, ok := boundaries[id]; ok {
				owner = direct
				break
			}
			seen[id] = true
			path = append(path, id)
			id = b.Parent
		}
		owners[start.ID] = owner
		for _, id := range path {
			owners[id] = owner
		}
	}
	return owners
}
