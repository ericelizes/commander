package beads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/example-user/commander/internal/store"
)

const PlanProposePath = "/api/plan/propose"

type coordinationGrantKey struct{}

func SubmitPlanProposal(ctx context.Context, dir string, ops [][]string, basis PlanBasis, intent *DoorIntent) (DoorResponse, error) {
	if !PostgresConfigured() || !OpsWritable(ops) {
		return DoorResponse{}, errors.New("proposal requires PostgreSQL and a bounded planning batch")
	}
	return viaDoor(ctx, dir, DoorRequest{Ops: ops, Atomic: true, NoWait: true, Plan: &basis, Intent: intent, Propose: true})
}

// ProposePlan writes no tree mutations. Its exact payload and the ordinary
// durable question inbox commit together, so no delivery message is authority.
func ProposePlan(ctx context.Context, dir string, ops [][]string, intent *DoorIntent) (DoorResponse, error) {
	if e := requirePlanningCaller(ctx); e != nil {
		return DoorResponse{}, e
	}
	basis := planBasisOf(ctx)
	if basis == nil || basis.CoordinationID != "" {
		return DoorResponse{}, errors.New("proposal requires an ungranted planning basis")
	}
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	if actor == "" {
		return DoorResponse{}, errors.New("proposal requires a registered Commander identity")
	}
	envelope, _, e := receiptEnvelope(ctx, ops)
	if e != nil {
		return DoorResponse{}, e
	}
	s, e := postgresStore()
	if e != nil {
		return DoorResponse{}, e
	}
	var id string
	e = s.WithTx(ctx, func(q store.Querier) error {
		pid, e := projectID(ctx, q, dir)
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, pid); e != nil {
			return e
		}
		var priorHash string
		e = q.QueryRowContext(ctx, `SELECT id,payload_hash FROM commander_plan_proposals WHERE project_id=? AND actor=? AND request_key=?`, pid, actor, envelope.Key).Scan(&id, &priorHash)
		if e == nil {
			if priorHash != envelope.Hash {
				return errors.New("proposal request_id was reused with different intent")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = pgCheckPlanBasis(ctx, q, pid, ops); e != nil {
			return e
		}
		// Coordinate on the actual read dependencies, including closed records.
		// A top-level create has an explicit project scope; no unrelated live bead
		// or invented task is needed to find the default planning owner.
		ids := make([]string, 0, len(basis.Beads))
		for id := range basis.Beads {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		scopes := []store.QuestionScope{}
		topLevel := false
		for _, op := range ops {
			if len(op) > 0 && op[0] == "create" {
				_, f, err := parsePGFlags(op[1:], "--title --description --acceptance --notes --status --priority --type --labels --add-label --remove-label --parent --assignee --reason --force --silent --no-inherit-labels --claim --ref")
				if err != nil {
					return err
				}
				if f.one("--parent") == "" {
					topLevel = true
				}
			}
		}
		if topLevel {
			scopes = append(scopes, store.QuestionScope{ProjectID: pid, ProjectScope: true, PlanRevision: basis.JurisdictionRevision})
		}
		for _, id := range ids {
			scopes = append(scopes, store.QuestionScope{ProjectID: pid, BeadID: id, PlanRevision: basis.Beads[id]})
		}
		if len(scopes) == 0 || len(scopes) > store.MaxQuestionScopes {
			return errors.New("proposal requires bounded canonical planning scopes")
		}
		question := store.QuestionRequest{ProjectID: pid, BeadID: scopes[0].BeadID, PlanRevision: scopes[0].PlanRevision, ProjectScope: scopes[0].ProjectScope, Actor: actor, ClientKey: "plan:" + envelope.Key, Question: fmt.Sprintf("Review exact planning proposal %s (SHA256 %s). Inspect `cmdr plan get --id QUESTION_ID` using this question's ID before answering. Agreement covers only its saved edits and does not approve a release.", envelope.Key, envelope.Hash), Recommendation: "Approve only if the saved edits preserve your workstream's goals and constraints.", Related: scopes[1:]}
		id, e = s.AskPlanningQuestionInTx(ctx, q, question)
		if e != nil {
			return e
		}
		payload, e := json.Marshal(struct {
			Dir    string      `json:"dir"`
			Basis  *PlanBasis  `json:"basis"`
			Ops    [][]string  `json:"ops"`
			Intent *DoorIntent `json:"intent,omitempty"`
		}{dir, basis, ops, intent})
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO commander_plan_proposals(id,project_id,actor,request_key,payload_hash,payload) VALUES(?,?,?,?,?,?::jsonb)`, id, pid, actor, envelope.Key, envelope.Hash, string(payload))
		return e
	})
	if e != nil {
		return DoorResponse{}, e
	}
	return DoorResponse{Atomic: true, ProposalID: id}, nil
}

func authorizeCoordinatedPlan(ctx context.Context, q store.Querier, pid int64, ops [][]string) (context.Context, error) {
	basis := planBasisOf(ctx)
	if basis == nil || basis.CoordinationID == "" {
		return ctx, nil
	}
	envelope, _, e := receiptEnvelope(ctx, ops)
	if e != nil {
		return ctx, e
	}
	return authorizeCoordinationEnvelope(ctx, q, pid, envelope)
}

// Typed actions use this same owner/version check with their own semantic hash.
// An ordinary plan agreement cannot authorize a different action endpoint.
func authorizeCoordinationEnvelope(ctx context.Context, q store.Querier, pid int64, envelope planEnvelope) (context.Context, error) {
	basis := planBasisOf(ctx)
	if basis == nil || basis.CoordinationID == "" {
		return ctx, nil
	}
	var e error
	var actor, hash, state string
	var project int64
	if e = q.QueryRowContext(ctx, `SELECT p.project_id,p.actor,p.payload_hash,b.state FROM commander_plan_proposals p JOIN bead_questions b ON b.id=p.id WHERE p.id=? FOR UPDATE OF p,b`, basis.CoordinationID).Scan(&project, &actor, &hash, &state); e != nil {
		return ctx, e
	}
	if project != pid || actor != pgCallerValue(ctx, "COMMANDER_ROLE") || hash != envelope.Hash || state != "answered" {
		return ctx, errors.New("coordination does not authorize this exact plan and requester")
	}
	var complete bool
	e = q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM bead_question_scopes sc LEFT JOIN bead_tasks t ON t.project_id=sc.project_id AND t.id=sc.bead_id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE sc.question_id=? AND ((sc.project_scope AND sc.plan_revision<>COALESCE(j.revision,1)) OR (NOT sc.project_scope AND (t.id IS NULL OR t.deleted_at IS NOT NULL OR t.plan_revision<>sc.plan_revision)) OR NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=sc.question_id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=? AND a.actor=commander_planning_owner(sc.project_id,sc.bead_id) AND a.approved)))`, basis.CoordinationID, basis.JurisdictionRevision).Scan(&complete)
	if e != nil {
		return ctx, e
	}
	if !complete {
		return ctx, errors.New("coordination is refused or no longer matches the current owners and plan")
	}
	return context.WithValue(ctx, coordinationGrantKey{}, true), nil
}
