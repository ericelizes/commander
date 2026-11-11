package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const MaxQuestionScopes = 4096

type QuestionScope struct {
	ProjectScope         bool   `json:"project_scope,omitempty"`
	ProjectID            int64  `json:"project_id"`
	BeadID               string `json:"bead_id"`
	PlanRevision         int64  `json:"plan_revision"`
	CommanderID          string `json:"commander_id,omitempty"`
	JurisdictionRevision int64  `json:"jurisdiction_revision,omitempty"`
}
type BeadQuestion struct {
	Kind           string           `json:"kind"`
	ProjectScope   bool             `json:"project_scope,omitempty"`
	ID             string           `json:"id"`
	ProjectID      int64            `json:"project_id"`
	BeadID         string           `json:"bead_id"`
	RaisedBy       string           `json:"raised_by"`
	Question       string           `json:"question"`
	Recommendation string           `json:"recommendation"`
	Checkpoint     string           `json:"checkpoint"`
	Blocking       bool             `json:"blocking"`
	State          string           `json:"state"`
	CreatedAt      int64            `json:"created_at"`
	Scopes         []QuestionScope  `json:"scopes"`
	Answers        []QuestionAnswer `json:"answers"`
}
type QuestionAnswer struct {
	QuestionScope
	Actor    string `json:"actor"`
	Answer   string `json:"answer"`
	Approved bool   `json:"approved"`
}
type QuestionRequest struct {
	Planning, ProjectScope                                                  bool
	ProjectID                                                               int64
	BeadID, Actor, Session, ClientKey, Question, Recommendation, Checkpoint string
	RunID, PlanRevision                                                     int64
	CheckpointRuntime, CheckpointThread                                     string
	Blocking                                                                bool
	Related                                                                 []QuestionScope
}

func questionLocks(ctx context.Context, q Querier, scopes []QuestionScope) error {
	ids := map[int64]bool{}
	for _, s := range scopes {
		ids[s.ProjectID] = true
	}
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, id); e != nil {
			return e
		}
	}
	return nil
}

func questionScope(ctx context.Context, q Querier, s QuestionScope, planning bool) (QuestionScope, error) {
	if s.ProjectScope {
		if !planning || s.BeadID != "" {
			return s, errors.New("project coordination requires a planning question")
		}
		var revision int64
		e := q.QueryRowContext(ctx, `SELECT COALESCE(j.revision,1),commander_planning_owner(p.id,NULL),COALESCE(j.revision,1) FROM projects p LEFT JOIN jurisdiction_projects j ON j.project_id=p.id WHERE p.id=? FOR UPDATE OF p`, s.ProjectID).Scan(&revision, &s.CommanderID, &s.JurisdictionRevision)
		if e != nil {
			return s, e
		}
		if s.PlanRevision > 0 && s.PlanRevision != revision {
			return s, ErrJurisdictionConflict
		}
		s.PlanRevision = revision
		return s, nil
	}
	var revision int64
	var status string
	e := q.QueryRowContext(ctx, `SELECT b.plan_revision,b.status,commander_planning_owner(b.project_id,b.id),COALESCE(j.revision,1) FROM bead_tasks b LEFT JOIN jurisdiction_projects j ON j.project_id=b.project_id WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL FOR UPDATE OF b`, s.ProjectID, s.BeadID).Scan(&revision, &status, &s.CommanderID, &s.JurisdictionRevision)
	if e != nil {
		return s, e
	}
	if status == "closed" && !planning {
		return s, errors.New("question scope is closed")
	}
	if s.PlanRevision > 0 && s.PlanRevision != revision {
		return s, ErrJurisdictionConflict
	}
	s.PlanRevision = revision
	return s, nil
}

// AskQuestion is intent-before-notification. It does not create a user card,
// approve a release, change scope or infer completion from a chat message.
func (s *Store) AskQuestion(ctx context.Context, r QuestionRequest) (string, error) {
	return s.askQuestion(ctx, r, nil)
}

// AskQuestionInTx lets a structured planning proposal and its decision inbox
// commit together. The same identity, scope and idempotency checks apply.
func (s *Store) AskQuestionInTx(ctx context.Context, q Querier, r QuestionRequest) (string, error) {
	return s.askQuestion(ctx, r, q)
}
func (s *Store) AskPlanningQuestionInTx(ctx context.Context, q Querier, r QuestionRequest) (string, error) {
	r.Planning = true
	return s.askQuestion(ctx, r, q)
}
func (s *Store) askQuestion(ctx context.Context, r QuestionRequest, tx Querier) (string, error) {
	if !s.postgres {
		return "", errors.New("durable questions require PostgreSQL")
	}
	if r.ProjectID <= 0 || (r.BeadID == "" && (!r.Planning || !r.ProjectScope)) || r.Actor == "" || r.ClientKey == "" || r.PlanRevision < 1 || strings.TrimSpace(r.Question) == "" {
		return "", errors.New("question requires bead, actor, stable request key and current plan revision")
	}
	if len(r.Question) > 8000 || len(r.Recommendation) > 8000 || len(r.Checkpoint) > 8000 || len(r.Related) >= MaxQuestionScopes {
		return "", errors.New("question exceeds bounded context")
	}
	scopes := append([]QuestionScope{{ProjectID: r.ProjectID, BeadID: r.BeadID, PlanRevision: r.PlanRevision, ProjectScope: r.ProjectScope}}, r.Related...)
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].ProjectID != scopes[j].ProjectID {
			return scopes[i].ProjectID < scopes[j].ProjectID
		}
		return scopes[i].BeadID < scopes[j].BeadID
	})
	for i, v := range scopes {
		if v.ProjectID <= 0 || (v.BeadID == "" && !v.ProjectScope) || (!r.Planning && v.ProjectScope) || i > 0 && v.ProjectID == scopes[i-1].ProjectID && v.BeadID == scopes[i-1].BeadID {
			return "", errors.New("question scopes must be distinct canonical beads")
		}
	}
	payload, _ := json.Marshal(struct {
		Question, Recommendation, Checkpoint, Runtime, Thread string
		Blocking, Planning                                    bool
		Scopes                                                []QuestionScope
	}{r.Question, r.Recommendation, r.Checkpoint, r.CheckpointRuntime, r.CheckpointThread, r.Blocking, r.Planning, scopes})
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	id := supervisionToken()
	apply := func(q Querier) error {
		if e := questionLocks(ctx, q, scopes); e != nil {
			return e
		}
		for i, v := range scopes {
			current, e := questionScope(ctx, q, v, r.Planning)
			if e != nil {
				return e
			}
			scopes[i] = current
		}
		var owner string
		for _, v := range scopes {
			if v.ProjectID == r.ProjectID && v.BeadID == r.BeadID {
				owner = v.CommanderID
			}
		}
		var runID any
		if r.Planning {
			var registered bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, r.Actor).Scan(&registered); e != nil {
				return e
			}
			if !registered || r.RunID != 0 || r.Blocking {
				return errors.New("planning coordination requires a registered Commander and no execution checkpoint")
			}
		}
		if r.RunID > 0 {
			var driver, runtime string
			if e := q.QueryRowContext(ctx, `SELECT execution_driver,execution_runtime FROM agent_runs WHERE id=?`, r.RunID).Scan(&driver, &runtime); e != nil {
				return e
			}
			if driver == "native" && r.CheckpointRuntime != runtime {
				return errors.New("question checkpoint provider differs from the recorded native execution")
			}
			var valid bool
			e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b JOIN agent_runs a ON a.id=b.active_run_id WHERE b.project_id=? AND b.id=? AND a.id=? AND a.status='running' AND a.opencode_session_id=? AND replace(a.agent_name,'.','-')=replace(?::text,'.','-'))`, r.ProjectID, r.BeadID, r.RunID, r.Session, r.Actor).Scan(&valid)
			if e != nil {
				return e
			}
			if !valid {
				return errors.New("question belongs to an inactive or replaced execution")
			}
			runID = r.RunID
		} else if r.Actor != owner {
			var planner bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, r.Actor).Scan(&planner); e != nil {
				return e
			}
			if !planner {
				return errors.New("question requires the current execution or a registered planning Commander")
			}
		}
		if r.Blocking && r.RunID > 0 && strings.TrimSpace(r.Checkpoint) == "" {
			return errors.New("blocking execution question requires a checkpoint")
		}
		var existing, oldHash string
		e := q.QueryRowContext(ctx, `SELECT id,payload_hash FROM bead_questions WHERE project_id=? AND bead_id=? AND raised_by=? AND client_key=? AND plan_revision=? AND request_run_id=?`, r.ProjectID, r.BeadID, r.Actor, r.ClientKey, r.PlanRevision, r.RunID).Scan(&existing, &oldHash)
		if e == nil {
			if oldHash != hash {
				return errors.New("question request key was reused with different content")
			}
			id = existing
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO bead_questions(id,project_id,bead_id,kind,plan_revision,raised_by,client_key,payload_hash,run_id,question,recommendation,checkpoint,blocking,checkpoint_runtime,checkpoint_thread) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, r.ProjectID, r.BeadID, func() string {
			if r.Planning {
				return "planning"
			}
			return "execution"
		}(), r.PlanRevision, r.Actor, r.ClientKey, hash, runID, r.Question, r.Recommendation, r.Checkpoint, r.Blocking, r.CheckpointRuntime, r.CheckpointThread); e != nil {
			return e
		}
		for _, v := range scopes {
			if _, e = q.ExecContext(ctx, `INSERT INTO bead_question_scopes(question_id,project_id,bead_id,plan_revision,project_scope,requested_jurisdiction_revision) VALUES(?,?,?,?,?,?)`, id, v.ProjectID, v.BeadID, v.PlanRevision, v.ProjectScope, v.JurisdictionRevision); e != nil {
				return e
			}
		}
		if r.Planning {
			return nil
		}
		_, e = q.ExecContext(ctx, `UPDATE bead_tasks SET resume_run_id=CASE WHEN ? AND ?::bigint>0 THEN ? ELSE resume_run_id END,resume_plan_revision=CASE WHEN ? AND ?::bigint>0 THEN plan_revision ELSE resume_plan_revision END,progress_seq=progress_seq+1,progress_at=? WHERE project_id=? AND id=?`, r.Blocking, r.RunID, runID, r.Blocking, r.RunID, time.Now().Unix(), r.ProjectID, r.BeadID)
		return e
	}
	var e error
	if tx != nil {
		e = apply(tx)
	} else {
		e = s.WithTx(ctx, apply)
	}
	return id, e
}

func questionScopes(ctx context.Context, q Querier, id string) ([]QuestionScope, error) {
	rows, e := q.QueryContext(ctx, `SELECT project_id,bead_id,plan_revision,project_scope FROM bead_question_scopes WHERE question_id=? ORDER BY project_id,bead_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var scopes []QuestionScope
	for rows.Next() {
		var v QuestionScope
		if e = rows.Scan(&v.ProjectID, &v.BeadID, &v.PlanRevision, &v.ProjectScope); e != nil {
			return nil, e
		}
		scopes = append(scopes, v)
	}
	return scopes, rows.Err()
}

// AnswerQuestion answers every affected scope currently owned by actor in one
// transaction. Cross-scope approval completes only after all current owners
// agree; a refusal is sufficient to reject the proposed change. Answers remain
// immutable evidence even after future handoffs.
func (s *Store) AnswerQuestion(ctx context.Context, id, actor, answer string, approved bool) (bool, error) {
	if actor == "" || strings.TrimSpace(answer) == "" || len(answer) > 8000 {
		return false, errors.New("answer requires planning identity and bounded decision text")
	}
	complete := false
	scopes, e := questionScopes(ctx, s.db, id)
	if e != nil {
		return false, e
	}
	if len(scopes) == 0 {
		return false, errors.New("question not found")
	}
	e = s.WithTx(ctx, func(q Querier) error {
		if e := questionLocks(ctx, q, scopes); e != nil {
			return e
		}
		var state, kind string
		var pid int64
		var bead string
		if e := q.QueryRowContext(ctx, `SELECT state,project_id,bead_id,kind FROM bead_questions WHERE id=? FOR UPDATE`, id).Scan(&state, &pid, &bead, &kind); e != nil {
			return e
		}
		if state == "superseded" {
			return ErrJurisdictionConflict
		}
		if state == "answered" {
			var same bool
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_question_answers WHERE question_id=? AND actor=? AND answer=? AND approved=?)`, id, actor, answer, approved).Scan(&same); e != nil {
				return e
			}
			if !same {
				return errors.New("question is already answered")
			}
			complete = true
			return nil
		}
		owned := 0
		for _, v := range scopes {
			current, e := questionScope(ctx, q, v, kind == "planning")
			if e != nil {
				return e
			}
			if current.CommanderID != actor {
				continue
			}
			owned++
			var prior string
			var priorApproved bool
			e = q.QueryRowContext(ctx, `SELECT answer,approved FROM bead_question_answers WHERE question_id=? AND project_id=? AND bead_id=? AND jurisdiction_revision=?`, id, v.ProjectID, v.BeadID, current.JurisdictionRevision).Scan(&prior, &priorApproved)
			if e == nil {
				if prior != answer || priorApproved != approved {
					return errors.New("an answer is already recorded for this scope and revision")
				}
				continue
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if state != "pending" {
				return errors.New("question is already answered")
			}
			if _, e = q.ExecContext(ctx, `INSERT INTO bead_question_answers(question_id,project_id,bead_id,jurisdiction_revision,actor,answer,approved) VALUES(?,?,?,?,?,?,?)`, id, v.ProjectID, v.BeadID, current.JurisdictionRevision, actor, answer, approved); e != nil {
				return e
			}
			if kind != "planning" {
				if _, e = q.ExecContext(ctx, `UPDATE bead_tasks SET progress_seq=progress_seq+1,progress_at=? WHERE project_id=? AND id=?`, time.Now().Unix(), v.ProjectID, v.BeadID); e != nil {
					return e
				}
			}
		}
		if owned == 0 {
			return errors.New("this Commander owns none of the question's scopes")
		}
		var missing bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_question_scopes sc LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE sc.question_id=? AND NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=sc.question_id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=COALESCE(j.revision,1) AND a.actor=commander_planning_owner(sc.project_id,sc.bead_id)))`, id).Scan(&missing); e != nil {
			return e
		}
		complete = !approved || !missing
		if complete && state == "pending" {
			if _, e := q.ExecContext(ctx, `UPDATE bead_questions SET state='answered',answered_at=? WHERE id=?`, time.Now().Unix(), id); e != nil {
				return e
			}
			// If the original process already stopped for this question, queue its
			// checkpoint only after all blocking questions have a recorded answer.
			if kind == "planning" {
				return nil
			}
			_, e := q.ExecContext(ctx, `SELECT commander_resume_question_run(?,?)`, pid, bead)
			return e
		}
		return nil
	})
	return complete, e
}

func (s *Store) Question(ctx context.Context, id string) (*BeadQuestion, error) {
	var v BeadQuestion
	e := s.db.QueryRowContext(ctx, `SELECT id,project_id,bead_id,raised_by,question,recommendation,checkpoint,blocking,state,created_at,kind FROM bead_questions WHERE id=?`, id).Scan(&v.ID, &v.ProjectID, &v.BeadID, &v.RaisedBy, &v.Question, &v.Recommendation, &v.Checkpoint, &v.Blocking, &v.State, &v.CreatedAt, &v.Kind)
	if e != nil {
		return nil, e
	}
	v.ProjectScope = v.BeadID == ""
	v.Scopes, e = questionScopes(ctx, s.db, id)
	if e != nil {
		return nil, e
	}
	for i, scope := range v.Scopes {
		if scope.ProjectScope {
			if e = s.db.QueryRowContext(ctx, `SELECT commander_planning_owner(?,NULL),COALESCE((SELECT revision FROM jurisdiction_projects WHERE project_id=?),1)`, scope.ProjectID, scope.ProjectID).Scan(&v.Scopes[i].CommanderID, &v.Scopes[i].JurisdictionRevision); e != nil {
				return nil, e
			}
			continue
		}
		j, e := s.Jurisdiction(ctx, scope.ProjectID, scope.BeadID)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		v.Scopes[i].CommanderID = j.CommanderID
		v.Scopes[i].JurisdictionRevision = j.Revision
	}
	rows, e := s.db.QueryContext(ctx, `SELECT project_id,bead_id,jurisdiction_revision,actor,answer,approved FROM bead_question_answers WHERE question_id=? ORDER BY created_at,project_id,bead_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var a QuestionAnswer
		if e = rows.Scan(&a.ProjectID, &a.BeadID, &a.JurisdictionRevision, &a.Actor, &a.Answer, &a.Approved); e != nil {
			return nil, e
		}
		a.ProjectScope = a.BeadID == ""
		v.Answers = append(v.Answers, a)
	}
	return &v, rows.Err()
}

func (s *Store) QuestionInbox(ctx context.Context, actor string) ([]BeadQuestion, error) {
	if actor == "" {
		return nil, errors.New("question inbox requires a planning identity")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT DISTINCT q.id,q.created_at FROM bead_questions q JOIN bead_question_scopes sc ON sc.question_id=q.id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE q.state='pending' AND commander_planning_owner(sc.project_id,sc.bead_id)=? AND NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=q.id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=COALESCE(j.revision,1) AND a.actor=?) ORDER BY q.created_at,q.id LIMIT 128`, actor, actor)
	if e != nil {
		return nil, e
	}
	var ids []string
	for rows.Next() {
		var id string
		var at int64
		if e = rows.Scan(&id, &at); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []BeadQuestion{}
	for _, id := range ids {
		v, e := s.Question(ctx, id)
		if e != nil {
			return nil, fmt.Errorf("question %s: %w", id, e)
		}
		out = append(out, *v)
	}
	return out, nil
}

func (s *Store) RunQuestionWait(ctx context.Context, rid int64) (bool, error) {
	if !s.postgres {
		return false, nil
	}
	var waiting bool
	e := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_questions WHERE run_id=? AND blocking)`, rid).Scan(&waiting)
	return waiting, e
}

func (s *Store) QuestionHistory(ctx context.Context, pid int64, bead string, runID int64) ([]BeadQuestion, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id FROM bead_questions WHERE project_id=? AND bead_id=? AND (?::bigint=0 OR run_id=?) ORDER BY created_at DESC,id DESC LIMIT 16`, pid, bead, runID, runID)
	if e != nil {
		return nil, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []BeadQuestion{}
	for _, id := range ids {
		q, e := s.Question(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, *q)
	}
	return out, nil
}

func (s *Store) QuestionNativeCheckpoint(ctx context.Context, runID int64) (runtime, thread string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT checkpoint_runtime,checkpoint_thread FROM bead_questions WHERE run_id=? AND blocking ORDER BY created_at DESC,id DESC LIMIT 1`, runID).Scan(&runtime, &thread)
	return
}
