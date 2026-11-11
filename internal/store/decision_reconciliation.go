package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type DecisionReconciliation struct {
	ProjectID            int64     `json:"project_id"`
	BeadID               string    `json:"bead_id"`
	RequestID            string    `json:"request_id"`
	PlanRevision         int64     `json:"plan_revision"`
	JurisdictionRevision int64     `json:"jurisdiction_revision"`
	Generation           string    `json:"generation"`
	AnswerID             string    `json:"answer_id,omitempty"`
	Reason               string    `json:"reason"`
	Ask                  string    `json:"ask"`
	Decision             *Decision `json:"decision"`
}
type DecisionReconciliationResult struct {
	Version  int64 `json:"version"`
	Replayed bool  `json:"replayed,omitempty"`
}

// ReconcileDecision replaces an obsolete human question, never its answer or
// authority. It neither releases a hold nor edits execution facts. All observed
// versions, source identity and ownership are checked under the project lock.
func (s *Store) ReconcileDecision(ctx context.Context, actor string, r DecisionReconciliation) (DecisionReconciliationResult, error) {
	out := DecisionReconciliationResult{}
	if !s.postgres {
		return out, errors.New("decision reconciliation requires PostgreSQL")
	}
	if r.ProjectID <= 0 || r.BeadID == "" || r.PlanRevision < 1 || r.JurisdictionRevision < 1 || r.Generation == "" || strings.TrimSpace(r.RequestID) == "" || len(r.RequestID) > 128 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 8000 || r.Decision == nil {
		return out, errors.New("reconciliation requires exact source, current versions, request key, reason and new decision")
	}
	if e := ValidateAsk(r.Ask); e != nil {
		return out, e
	}
	decisionRaw := r.Decision.JSON()
	if len(decisionRaw) > 32000 {
		return out, errors.New("replacement decision exceeds bounded context")
	}
	decision, e := ParseDecision(decisionRaw)
	if e != nil {
		return out, e
	}
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	e = s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, r.ProjectID); e != nil {
			return e
		}
		var registered bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, actor).Scan(&registered); e != nil {
			return e
		}
		if !registered {
			return errors.New("only a registered planning Commander may reconcile a decision")
		}
		var priorHash string
		var prior []byte
		e := q.QueryRowContext(ctx, `SELECT payload_hash,response FROM human_decision_reconciliations WHERE project_id=? AND bead_id=? AND actor=? AND request_key=?`, r.ProjectID, r.BeadID, actor, r.RequestID).Scan(&priorHash, &prior)
		if e == nil {
			if priorHash != hash {
				return errors.New("reconciliation request_id reused with different intent")
			}
			if e = json.Unmarshal(prior, &out); e != nil {
				return e
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var version, scopeEpoch, authorityEpoch, jurisdiction int64
		var owner, status string
		var human bool
		e = q.QueryRowContext(ctx, `SELECT b.plan_revision,b.human_scope_epoch,b.human_authority_epoch,COALESCE(j.revision,1),commander_planning_owner(b.project_id,b.id),b.status,EXISTS(SELECT 1 FROM jsonb_array_elements_text(b.labels) label WHERE label IN('hold','needs-user','gate','verifier:user','approver:user') OR label ~ '^needs-' OR label ~ '^waiting:[^:]+:user(:|$)' OR label ~ '^w:[^:]+:us(:|$)') FROM bead_tasks b LEFT JOIN jurisdiction_projects j ON j.project_id=b.project_id WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL FOR UPDATE OF b`, r.ProjectID, r.BeadID).Scan(&version, &scopeEpoch, &authorityEpoch, &jurisdiction, &owner, &status, &human)
		if e != nil {
			return e
		}
		if owner != actor || version != r.PlanRevision || jurisdiction != r.JurisdictionRevision {
			return ErrJurisdictionConflict
		}
		if !human || status == "closed" || status == "deferred" {
			return errors.New("replacement question requires open work with an existing genuine human restriction")
		}
		var generation string
		if e = q.QueryRowContext(ctx, `SELECT CAST(COALESCE(MAX(version),0) AS TEXT) FROM pitches WHERE project_id=? AND bead_id=?`, r.ProjectID, r.BeadID).Scan(&generation); e != nil {
			return e
		}
		if generation != r.Generation {
			return errors.New("the decision generation changed; inspect the current question")
		}
		var savedPayload string
		var state int
		e = q.QueryRowContext(ctx, `SELECT payload,state FROM needs_resolutions WHERE project_id=? AND bead_id=? AND generation=? FOR UPDATE`, r.ProjectID, r.BeadID, generation).Scan(&savedPayload, &state)
		hasAnswer := e == nil
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if hasAnswer {
			var saved NeedsResolution
			if e = json.Unmarshal([]byte(savedPayload), &saved); e != nil {
				return e
			}
			answerID := saved.AnswerID
			if answerID == "" {
				answerID = "legacy-" + r.BeadID
			}
			if r.AnswerID != answerID || state != ResolutionJournaled && state != ResolutionBeadWritten {
				return errors.New("only the exact unapplied saved answer can be reconciled")
			}
		} else if r.AnswerID != "" {
			return errors.New("the supplied saved answer does not exist")
		}
		var oldScope, oldAuthority sql.NullInt64
		if e = q.QueryRowContext(ctx, `SELECT (SELECT scope_epoch FROM pitch_authority_bases WHERE project_id=? AND bead_id=? AND CAST(version AS TEXT)=?),(SELECT authority_epoch FROM decision_authorities WHERE project_id=? AND bead_id=? AND generation=?)`, r.ProjectID, r.BeadID, generation, r.ProjectID, r.BeadID, generation).Scan(&oldScope, &oldAuthority); e != nil {
			return e
		}
		if oldScope.Valid && oldScope.Int64 == scopeEpoch && (!hasAnswer || oldAuthority.Valid && oldAuthority.Int64 == authorityEpoch) {
			return errors.New("the recorded question or answer still applies; resume it rather than asking again")
		}
		// Unanswered historical questions may have no scope proof. A historical
		// answered pitch without its journal is not silently treated as unanswered.
		if !hasAnswer {
			var answered bool
			if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pitches WHERE project_id=? AND bead_id=? AND CAST(version AS TEXT)=? AND (answer<>'' OR answered_at>0))`, r.ProjectID, r.BeadID, generation).Scan(&answered); e != nil {
				return e
			}
			if answered {
				return errors.New("historical answer has no journal; preserve it and recover its provenance before replacing it")
			}
		}
		next, e := strconv.ParseInt(generation, 10, 64)
		if e != nil {
			return fmt.Errorf("invalid source generation: %w", e)
		}
		next++
		now := time.Now().Unix()
		// Publish the new generation before retiring the old journal. The ordinary
		// answer-progress trigger must not count administrative retirement as work.
		if _, e = q.ExecContext(ctx, `INSERT INTO pitches(project_id,bead_id,version,title,decision,created_at) VALUES(?,?,?,?,?,?)`, r.ProjectID, r.BeadID, next, r.Ask, decision.JSON(), now); e != nil {
			return e
		}
		if hasAnswer {
			if _, e = q.ExecContext(ctx, `UPDATE needs_resolutions SET state=? WHERE project_id=? AND bead_id=? AND generation=?`, ResolutionSuperseded, r.ProjectID, r.BeadID, generation); e != nil {
				return e
			}
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO needs_you(project_id,bead_id,ask,note,since,decision) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,bead_id) DO UPDATE SET ask=excluded.ask,note=excluded.note,since=excluded.since,decision=excluded.decision`, r.ProjectID, r.BeadID, r.Ask, decision.Context, now, decision.JSON()); e != nil {
			return e
		}
		out.Version = next
		response, _ := json.Marshal(out)
		_, e = q.ExecContext(ctx, `INSERT INTO human_decision_reconciliations(project_id,bead_id,actor,request_key,payload_hash,source_generation,source_answer_id,reason,next_version,response) VALUES(?,?,?,?,?,?,?,?,?,?::jsonb)`, r.ProjectID, r.BeadID, actor, r.RequestID, hash, r.Generation, r.AnswerID, r.Reason, next, string(response))
		return e
	})
	return out, e
}
