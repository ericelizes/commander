package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	ResolutionJournaled = iota
	ResolutionBeadWritten
	ResolutionClosed
	ResolutionMailed
	ResolutionSuperseded
)

// NeedsResolution is an immutable answer for one card generation. State only
// advances; a superseded entry must never mutate the bead or send stale mail.
type NeedsResolution struct {
	ProjectID                                      int64
	BeadID                                         string
	Generation, AnswerID                           string
	Action, Choice, Text, GateChoice, Ask, Manager string
	State                                          int
	Applied, Mailed                                bool
	// Approved is the approving verdict computed with the card in hand when
	// the answer was saved.
	Approved          bool
	InputPlanRevision int64 // observed decision target; zero for historical journals
}

func (s *Store) ListNeedsResolutions(ctx context.Context, projectID int64, beadID string) ([]NeedsResolution, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT generation, payload, state FROM needs_resolutions WHERE project_id = ? AND bead_id = ? ORDER BY rowid`, projectID, beadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NeedsResolution
	for rows.Next() {
		var generation, payload string
		var state int
		if err := rows.Scan(&generation, &payload, &state); err != nil {
			return nil, err
		}
		var r NeedsResolution
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			return nil, err
		}
		r.Generation, r.State = generation, state
		if r.AnswerID == "" {
			r.AnswerID = "legacy-" + r.BeadID
		}
		r.Applied, r.Mailed = state == ResolutionClosed || state == ResolutionMailed, state == ResolutionMailed
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetNeedsResolution(ctx context.Context, projectID int64, beadID string) (*NeedsResolution, error) {
	entries, err := s.ListNeedsResolutions(ctx, projectID, beadID)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return &entries[len(entries)-1], nil
}

// NeedsGeneration stays available after the queue row is deleted.
func (s *Store) NeedsGeneration(ctx context.Context, projectID int64, beadID string) (string, error) {
	var generation string
	err := s.db.QueryRowContext(ctx, `SELECT CAST(COALESCE(MAX(version), 0) AS TEXT) FROM pitches WHERE project_id = ? AND bead_id = ?`, projectID, beadID).Scan(&generation)
	return generation, err
}

// SaveNeedsResolution atomically saves the journal and the pitch answer before
// any bead writes. Existing entries are immutable, including their receipts.
func (s *Store) SaveNeedsResolution(ctx context.Context, r NeedsResolution, answer string) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(q Querier) error {
		if s.postgres {
			if _, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, r.ProjectID); err != nil {
				return err
			}
		}
		if s.postgres {
			var changed bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND ?::bigint>0 AND b.plan_revision<>? AND NOT EXISTS(SELECT 1 FROM needs_resolutions n WHERE n.project_id=b.project_id AND n.bead_id=b.id AND n.generation=?))`, r.ProjectID, r.BeadID, r.InputPlanRevision, r.InputPlanRevision, r.Generation).Scan(&changed); err != nil {
				return err
			}
			if changed {
				return fmt.Errorf("decision target changed while saving the answer; refresh it before approving")
			}
			var stale bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b JOIN pitch_authority_bases p ON p.project_id=b.project_id AND p.bead_id=b.id WHERE b.project_id=? AND b.id=? AND CAST(p.version AS TEXT)=? AND p.scope_epoch<>b.human_scope_epoch AND NOT EXISTS(SELECT 1 FROM needs_resolutions n WHERE n.project_id=p.project_id AND n.bead_id=p.bead_id AND n.generation=?))`, r.ProjectID, r.BeadID, r.Generation, r.Generation).Scan(&stale); err != nil {
				return err
			}
			if stale {
				return fmt.Errorf("decision scope changed while awaiting an answer; refresh the question before approving it")
			}
			var missing bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND (SELECT mode='active' FROM orchestration_control WHERE id=1) AND NOT EXISTS(SELECT 1 FROM pitch_authority_bases p WHERE p.project_id=b.project_id AND p.bead_id=b.id AND CAST(p.version AS TEXT)=?) AND NOT EXISTS(SELECT 1 FROM needs_resolutions n WHERE n.project_id=b.project_id AND n.bead_id=b.id AND n.generation=?))`, r.ProjectID, r.BeadID, r.Generation, r.Generation).Scan(&missing); err != nil {
				return err
			}
			if missing {
				return fmt.Errorf("decision has no recorded scope; refresh the question before answering")
			}

		}
		res, err := q.ExecContext(ctx, `INSERT INTO needs_resolutions(project_id, bead_id, generation, payload) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, r.ProjectID, r.BeadID, r.Generation, string(payload))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if s.postgres {
			if _, err = q.ExecContext(ctx, `INSERT INTO decision_authorities(project_id,bead_id,generation,authority_epoch) SELECT b.project_id,b.id,?,b.human_authority_epoch FROM bead_tasks b JOIN pitch_authority_bases p ON p.project_id=b.project_id AND p.bead_id=b.id AND CAST(p.version AS TEXT)=? AND p.scope_epoch=b.human_scope_epoch WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL ON CONFLICT DO NOTHING`, r.Generation, r.Generation, r.ProjectID, r.BeadID); err != nil {
				return err
			}
		}
		_, err = q.ExecContext(ctx, `UPDATE pitches SET answer = ?, answered_at = ? WHERE project_id = ? AND bead_id = ? AND CAST(version AS TEXT) = ?`, answer, time.Now().Unix(), r.ProjectID, r.BeadID, r.Generation)
		return err
	})
}

func (s *Store) AdvanceNeedsResolution(ctx context.Context, r NeedsResolution, state int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE needs_resolutions SET state = ? WHERE project_id = ? AND bead_id = ? AND generation = ? AND state < ?`, state, r.ProjectID, r.BeadID, r.Generation, state)
	return err
}
