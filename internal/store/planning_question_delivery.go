package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Delivery metadata belongs to the existing decision scope, not an invented
// execution bead. A delivered notice is never an answer or work progress.
type PlanningQuestionDelivery struct {
	QuestionID, BeadID, Owner, Token string
	ProjectID, Revision, Attempts    int64
}

func (s *Store) LeasePlanningQuestions(ctx context.Context, now time.Time, active bool, limit int) ([]PlanningQuestionDelivery, error) {
	if limit <= 0 || limit > 128 {
		limit = 128
	}
	out := []PlanningQuestionDelivery{}
	e := s.WithTx(ctx, func(q Querier) error {
		rows, e := q.QueryContext(ctx, `SELECT sc.question_id,sc.project_id,sc.bead_id,commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,'')),COALESCE(j.revision,1),CASE WHEN sc.delivery_owner=commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,'')) AND sc.delivery_revision=COALESCE(j.revision,1) THEN sc.delivery_attempts ELSE 0 END
   FROM bead_question_scopes sc JOIN bead_questions b ON b.id=sc.question_id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id
   WHERE b.kind='planning' AND b.state='pending' AND sc.delivery_lease_until<=?
   AND (sc.delivery_due_at<=? OR sc.delivery_owner<>commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,'')) OR sc.delivery_revision<>COALESCE(j.revision,1) OR (? AND sc.delivery_error='Observation only'))
   AND NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=sc.question_id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=COALESCE(j.revision,1) AND a.actor=commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,'')))
   ORDER BY sc.delivery_due_at,b.created_at,sc.question_id,sc.project_id,sc.bead_id LIMIT ? FOR UPDATE OF sc SKIP LOCKED`, now.Unix(), now.Unix(), active, limit)
		if e != nil {
			return e
		}
		for rows.Next() {
			var d PlanningQuestionDelivery
			if e = rows.Scan(&d.QuestionID, &d.ProjectID, &d.BeadID, &d.Owner, &d.Revision, &d.Attempts); e != nil {
				rows.Close()
				return e
			}
			d.Token = supervisionToken()
			out = append(out, d)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, d := range out {
			if _, e = q.ExecContext(ctx, `UPDATE bead_question_scopes SET delivery_owner=?,delivery_revision=?,delivery_attempts=?,delivery_token=?,delivery_lease_until=? WHERE question_id=? AND project_id=? AND bead_id=?`, d.Owner, d.Revision, d.Attempts, d.Token, now.Add(30*time.Second).Unix(), d.QuestionID, d.ProjectID, d.BeadID); e != nil {
				return e
			}
		}
		return nil
	})
	return out, e
}

func (s *Store) PlanningQuestionDeliveryCurrent(ctx context.Context, d PlanningQuestionDelivery) (bool, error) {
	var current bool
	e := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_question_scopes sc JOIN bead_questions q ON q.id=sc.question_id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE sc.question_id=? AND sc.project_id=? AND sc.bead_id=? AND sc.delivery_token=? AND q.kind='planning' AND q.state='pending' AND commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,''))=? AND COALESCE(j.revision,1)=? AND NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=sc.question_id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=? AND a.actor=?))`, d.QuestionID, d.ProjectID, d.BeadID, d.Token, d.Owner, d.Revision, d.Revision, d.Owner).Scan(&current)
	return current, e
}

func (s *Store) FinishPlanningQuestionDelivery(ctx context.Context, d PlanningQuestionDelivery, due time.Time, attempted bool, detail string) error {
	return s.WithTx(ctx, func(q Querier) error {
		// A handoff/answer after preparation must not suppress the new obligation.
		var token string
		e := q.QueryRowContext(ctx, `SELECT sc.delivery_token FROM bead_question_scopes sc JOIN bead_questions b ON b.id=sc.question_id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE sc.question_id=? AND sc.project_id=? AND sc.bead_id=? AND sc.delivery_token=? AND b.kind='planning' AND b.state='pending' AND COALESCE(j.revision,1)=? AND commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,''))=? FOR UPDATE OF sc`, d.QuestionID, d.ProjectID, d.BeadID, d.Token, d.Revision, d.Owner).Scan(&token)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		attempts := d.Attempts
		if attempted {
			attempts++
		}
		// One notice covers every scope of this question owned by this recipient,
		// including rows beyond this bounded lease batch. Per-agent dispatch fencing
		// prevents another worker from concurrently delivering those same notices.
		_, e = q.ExecContext(ctx, `UPDATE bead_question_scopes sc SET delivery_owner=?,delivery_revision=?,delivery_attempts=?,delivery_due_at=?,delivery_lease_until=0,delivery_token='',delivery_error=? WHERE sc.question_id=? AND commander_planning_owner(sc.project_id,NULLIF(sc.bead_id,''))=? AND COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=sc.project_id),1)=?`, d.Owner, d.Revision, attempts, due.Unix(), detail, d.QuestionID, d.Owner, d.Revision)
		return e
	})
}
