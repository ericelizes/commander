package store

import (
	"context"
	"database/sql"
	"errors"
)

// QuestionCardAsk is the ask on the card an agent's question to the user
// raises (cmdr question ask --to user).
const QuestionCardAsk = "Answer the agent's question"

// OpenQuestion is an agent's question to the user, as its card shows it.
type OpenQuestion struct {
	ID, Project, Bead, Title, Question, Recommendation string
}

// QuestionForCard is a question with what its card shows: the step's project
// and title beside the question and the agent's recommendation.
func (s *Store) QuestionForCard(ctx context.Context, id string) (OpenQuestion, error) {
	var o OpenQuestion
	err := s.db.QueryRowContext(ctx, `SELECT q.id,p.name,q.bead_id,b.title,q.question,q.recommendation
FROM bead_questions q JOIN projects p ON p.id=q.project_id
JOIN bead_tasks b ON b.project_id=q.project_id AND b.id=q.bead_id
WHERE q.id=?`, id).Scan(&o.ID, &o.Project, &o.Bead, &o.Title, &o.Question, &o.Recommendation)
	return o, err
}

// StaleQuestionCards lists the steps whose agent-question card no longer has a
// pending question behind it (answered or superseded), so the card comes off
// the user's list. A card whose answer the user already gave is left to that
// answer's own path.
func (s *Store) StaleQuestionCards(ctx context.Context) ([]OpenQuestion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.name,n.bead_id FROM needs_you n JOIN projects p ON p.id=n.project_id
WHERE n.ask=?
 AND NOT EXISTS(SELECT 1 FROM bead_questions q WHERE q.project_id=n.project_id AND q.bead_id=n.bead_id AND q.state='pending' AND q.blocking)
 AND NOT commander_pending_human_answer(n.project_id,n.bead_id)
ORDER BY n.since`, QuestionCardAsk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenQuestion
	for rows.Next() {
		var o OpenQuestion
		if err := rows.Scan(&o.Project, &o.Bead); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PendingQuestionOn is the pending blocking question on a step and the
// planning owner that records its answer ("" when there is none).
func (s *Store) PendingQuestionOn(ctx context.Context, project, bead string) (id, owner string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT q.id,commander_planning_owner(q.project_id,q.bead_id) FROM bead_questions q JOIN projects p ON p.id=q.project_id
WHERE p.name=? AND q.bead_id=? AND q.state='pending' AND q.blocking ORDER BY q.created_at LIMIT 1`, project, bead).Scan(&id, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return id, owner, err
}
