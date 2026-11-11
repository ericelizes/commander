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

func readPublicationProposal(ctx context.Context, q store.Querier, pid int64, envelope planEnvelope) (string, error) {
	var id, hash string
	e := q.QueryRowContext(ctx, `SELECT id,payload_hash FROM commander_plan_proposals WHERE project_id=? AND actor=? AND request_key=?`, pid, pgCallerValue(ctx, "COMMANDER_ROLE"), envelope.Key).Scan(&id, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if hash != envelope.Hash {
		return "", errors.New("proposal request_id was reused with different intent")
	}
	return id, nil
}

func savePublicationProposal(ctx context.Context, s *store.Store, q store.Querier, pid int64, req PublicationRequest, envelope planEnvelope) (string, error) {
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	var registered bool
	if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, actor).Scan(&registered); e != nil {
		return "", e
	}
	if actor == "" || !registered {
		return "", errors.New("publication coordination requires a registered planning Commander")
	}
	ids := make([]string, 0, len(req.Basis.Beads))
	for id := range req.Basis.Beads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 || len(ids) > store.MaxQuestionScopes {
		return "", errors.New("publication coordination requires bounded canonical scopes")
	}
	scopes := make([]store.QuestionScope, 0, len(ids))
	for _, id := range ids {
		scopes = append(scopes, store.QuestionScope{ProjectID: pid, BeadID: id, PlanRevision: req.Basis.Beads[id]})
	}
	id, e := s.AskPlanningQuestionInTx(ctx, q, store.QuestionRequest{ProjectID: pid, BeadID: scopes[0].BeadID, PlanRevision: scopes[0].PlanRevision, Actor: actor, ClientKey: "publication:" + envelope.Key, Question: fmt.Sprintf("Review exact gate publication proposal %s (SHA256 %s). Inspect cmdr plan get --id QUESTION_ID using this question's ID. Agreement authorizes only the saved placement and redundant ancestor-wait repair, never the protected release or approval.", envelope.Key, envelope.Hash), Recommendation: "Agree only if the exact saved action preserves your scope and all required approval gates.", Related: scopes[1:]})
	if e != nil {
		return "", e
	}
	payload, e := json.Marshal(PlanDocument{Dir: req.Dir, Basis: req.Basis, Publication: &PublicationAction{Gate: req.Gate, Parent: req.Parent, Waiters: req.Waiters, Reason: req.Reason}})
	if e != nil {
		return "", e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO commander_plan_proposals(id,project_id,actor,request_key,payload_hash,payload) VALUES(?,?,?,?,?,?::jsonb)`, id, pid, actor, envelope.Key, envelope.Hash, string(payload))
	return id, e
}
