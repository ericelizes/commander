package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type SupervisionFact struct {
	ProjectID                                                       int64
	BeadID, Title, AgentID, SupervisorID, PlanningCommander, Status string
	ProjectName                                                     string
	PendingHumanAnswer                                              bool
	Epoch, ProgressSeq, ProgressAt, CompletionAt, RunID             int64
	Labels                                                          []string
	Deleted, Held, PresenceKnown, Alive, ResultReady, ExecutorBound bool
	AgentState                                                      string
	ExecutionReady, ExecutionReserved, ExecutionCapable             bool
	ExecutionAttempts                                               int64
	ExecutionError                                                  string
	PendingQuestions                                                int
	QuestionWaiting                                                 bool
	ChildCount, OpenBlockers                                        int
	Job                                                             SupervisionJob
}
type SupervisionJob struct {
	ProjectID                                                                           int64
	BeadID                                                                              string
	Epoch, ProgressSeq                                                                  int64
	Reason, State, Target, Token, RequestID                                             string
	OpenedAt, PhaseSince, DueAt, Attempts, DeliveredAt, AcknowledgedAt, EscalationLevel int64
}

func supervisionToken() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

// LeaseSupervision bounds one pass and permits concurrent reconcilers without
// duplicate ownership. Facts are read after the short lease transaction.
func (s *Store) LeaseSupervision(ctx context.Context, now time.Time, limit int) ([]SupervisionJob, error) {
	if !s.postgres {
		return nil, errors.New("durable supervision requires PostgreSQL")
	}
	if limit < 1 || limit > 256 {
		limit = 128
	}
	out := []SupervisionJob{}
	e := s.WithTx(ctx, func(q Querier) error {
		rows, e := q.QueryContext(ctx, `SELECT x.project_id,x.bead_id,x.epoch,x.progress_seq,x.reason,x.state,x.target_agent_id,x.opened_at,x.phase_since,x.due_at,x.attempts,x.delivered_at,x.acknowledged_at,x.escalation_level,x.request_id FROM bead_supervision x JOIN bead_tasks b ON b.project_id=x.project_id AND b.id=x.bead_id JOIN projects p ON p.id=b.project_id WHERE x.due_at<=? AND x.lease_until<=? AND (b.status<>'closed' OR commander_pending_human_answer(b.project_id,b.id)) AND b.deleted_at IS NULL AND p.status='active' AND (jsonb_exists(b.labels,'kind:objective') OR jsonb_exists(b.labels,'kind:task') OR jsonb_exists(b.labels,'kind:workstream') OR jsonb_exists(b.labels,'kind:gate') OR commander_pending_human_answer(b.project_id,b.id) OR EXISTS(SELECT 1 FROM bead_question_scopes sc JOIN bead_questions q ON q.id=sc.question_id WHERE sc.project_id=b.project_id AND sc.bead_id=b.id AND q.kind='execution' AND q.state='pending')) ORDER BY x.due_at,x.project_id,x.bead_id LIMIT ? FOR UPDATE OF x SKIP LOCKED`, now.Unix(), now.Unix(), limit)
		if e != nil {
			return e
		}
		for rows.Next() {
			var j SupervisionJob
			if e = rows.Scan(&j.ProjectID, &j.BeadID, &j.Epoch, &j.ProgressSeq, &j.Reason, &j.State, &j.Target, &j.OpenedAt, &j.PhaseSince, &j.DueAt, &j.Attempts, &j.DeliveredAt, &j.AcknowledgedAt, &j.EscalationLevel, &j.RequestID); e != nil {
				rows.Close()
				return e
			}
			j.Token = supervisionToken()
			out = append(out, j)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, j := range out {
			if _, e = q.ExecContext(ctx, `UPDATE bead_supervision SET lease_token=?,lease_until=? WHERE project_id=? AND bead_id=?`, j.Token, now.Add(30*time.Second).Unix(), j.ProjectID, j.BeadID); e != nil {
				return e
			}
		}
		return nil
	})
	return out, e
}

func (s *Store) SupervisionFact(ctx context.Context, j SupervisionJob) (*SupervisionFact, error) {
	f := SupervisionFact{ProjectID: j.ProjectID, BeadID: j.BeadID, Job: j}
	var labels []byte
	var at int64
	var nativeAlive sql.NullBool
	var observedAlive sql.NullBool
	var result sql.NullInt64
	e := s.db.QueryRowContext(ctx, `SELECT b.title,b.status,b.responsible_agent_id,b.responsibility_epoch,b.progress_seq,b.progress_at,b.completion_at,COALESCE(b.active_run_id,0),b.result_run_id,COALESCE(replace(r.agent_name,'.','-')=replace(b.responsible_agent_id,'.','-'),FALSE),b.labels,b.deleted_at IS NOT NULL,commander_supervisor(b.project_id,b.id),COALESCE(a.state,'unknown'),COALESCE(o.checked_at,n.checked_at,0),o.alive,n.alive,
 (SELECT COUNT(*) FROM bead_tasks c WHERE c.project_id=b.project_id AND c.parent_id=b.id AND c.status NOT IN ('closed','deferred') AND c.deleted_at IS NULL),
 (SELECT COUNT(*) FROM bead_dependencies d JOIN bead_tasks t ON t.project_id=d.project_id AND t.id=d.depends_on_id WHERE d.project_id=b.project_id AND d.issue_id=b.id AND d.type='blocks' AND (NOT commander_dependency_complete(t.status,t.labels) OR commander_pending_human_answer(t.project_id,t.id)) AND t.deleted_at IS NULL),
 b.execution_ready,b.execution_attempts,b.execution_error,EXISTS(SELECT 1 FROM execution_slots x WHERE x.project_id=b.project_id AND x.bead_id=b.id AND x.run_id IS NULL AND x.lease_until>EXTRACT(EPOCH FROM clock_timestamp())::bigint),
 EXISTS(SELECT 1 FROM execution_slots x JOIN projects p ON p.id=b.project_id WHERE x.enabled AND jsonb_exists(x.capabilities,p.type) AND b.execution_capabilities <@ x.capabilities),
 (SELECT COUNT(*) FROM bead_question_scopes sc JOIN bead_questions q ON q.id=sc.question_id LEFT JOIN jurisdiction_projects j ON j.project_id=sc.project_id WHERE sc.project_id=b.project_id AND sc.bead_id=b.id AND q.kind='execution' AND q.state='pending' AND NOT EXISTS(SELECT 1 FROM bead_question_answers a WHERE a.question_id=q.id AND a.project_id=sc.project_id AND a.bead_id=sc.bead_id AND a.jurisdiction_revision=COALESCE(j.revision,1) AND a.actor=commander_planning_owner(sc.project_id,sc.bead_id))),
 EXISTS(SELECT 1 FROM bead_questions q WHERE q.project_id=b.project_id AND q.bead_id=b.id AND q.blocking AND q.kind='execution' AND q.state='pending'),commander_pending_human_answer(b.project_id,b.id),(SELECT name FROM projects WHERE id=b.project_id)
 FROM bead_tasks b LEFT JOIN agent_runs r ON r.id=b.active_run_id
 LEFT JOIN run_observations o ON o.run_id=r.id AND r.status='running' AND replace(r.agent_name,'.','-')=replace(b.responsible_agent_id,'.','-')
 LEFT JOIN native_presence n ON n.role=b.responsible_agent_id
 LEFT JOIN LATERAL(SELECT state FROM agent_state WHERE replace(role,'.','-')=replace(b.responsible_agent_id,'.','-') ORDER BY updated DESC LIMIT 1)a ON TRUE
 WHERE b.project_id=? AND b.id=?`, j.ProjectID, j.BeadID).Scan(&f.Title, &f.Status, &f.AgentID, &f.Epoch, &f.ProgressSeq, &f.ProgressAt, &f.CompletionAt, &f.RunID, &result, &f.ExecutorBound, &labels, &f.Deleted, &f.SupervisorID, &f.AgentState, &at, &observedAlive, &nativeAlive, &f.ChildCount, &f.OpenBlockers, &f.ExecutionReady, &f.ExecutionAttempts, &f.ExecutionError, &f.ExecutionReserved, &f.ExecutionCapable, &f.PendingQuestions, &f.QuestionWaiting, &f.PendingHumanAnswer, &f.ProjectName)
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(labels, &f.Labels); e != nil {
		return nil, e
	}
	f.ResultReady = result.Valid
	f.PresenceKnown = at >= time.Now().Add(-45*time.Second).Unix()
	f.Alive = observedAlive.Valid && observedAlive.Bool || nativeAlive.Valid && nativeAlive.Bool
	// A registered native role with no process observation has never started;
	// its validated runtime adapter can start it. Stale observations remain unknown.
	if !f.ExecutorBound && at == 0 && !nativeAlive.Valid {
		f.PresenceKnown = true
		f.Alive = false
	}
	e = s.db.QueryRowContext(ctx, `WITH RECURSIVE ancestry AS (SELECT id,parent_id,status,labels,ARRAY[id] AS path FROM bead_tasks WHERE project_id=? AND id=? UNION ALL SELECT b.id,b.parent_id,b.status,b.labels,a.path||b.id FROM bead_tasks b JOIN ancestry a ON a.parent_id=b.id WHERE b.project_id=? AND NOT b.id=ANY(a.path)) SELECT EXISTS(SELECT 1 FROM ancestry WHERE jsonb_exists_any(labels,ARRAY['hold','superseded','retired']) OR status='deferred' OR (status='closed' AND id<>?))`, f.ProjectID, f.BeadID, f.ProjectID, f.BeadID).Scan(&f.Held)
	if e != nil {
		return nil, e
	}
	f.PlanningCommander = f.SupervisorID
	if f.SupervisorID == f.AgentID {
		var parent string
		e = s.db.QueryRowContext(ctx, `SELECT commander_supervisor(b.project_id,b.parent_id) FROM workstream_managers m JOIN bead_tasks b ON b.project_id=m.project_id AND b.id=m.workstream_bead_id WHERE m.am_role=?`, f.AgentID).Scan(&parent)
		if e == nil {
			f.SupervisorID = parent
		} else if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
	}
	return &f, nil
}

// FinishSupervision compares the assignment and progress generation under the
// bead lock. A stale worker cannot acknowledge or overwrite a newer obligation.
func (s *Store) FinishSupervision(ctx context.Context, f SupervisionFact, reason, state, target, detail string, due time.Time, attempted bool) error {
	return s.WithTx(ctx, func(q Querier) error {
		var epoch, seq int64
		if e := q.QueryRowContext(ctx, `SELECT responsibility_epoch,progress_seq FROM bead_tasks WHERE project_id=? AND id=? FOR UPDATE`, f.ProjectID, f.BeadID).Scan(&epoch, &seq); e != nil {
			return e
		}
		if epoch != f.Epoch || seq != f.ProgressSeq {
			return nil
		}
		reset := f.Job.Epoch != f.Epoch || f.Job.ProgressSeq != f.ProgressSeq || f.Job.Reason != reason
		attempts, opened, phase, ack, delivered, level := f.Job.Attempts, f.Job.OpenedAt, f.Job.PhaseSince, f.Job.AcknowledgedAt, f.Job.DeliveredAt, f.Job.EscalationLevel
		now := time.Now().Unix()
		request := f.Job.RequestID
		if reset {
			request = supervisionToken()
			attempts = 0
			opened = now
			phase = now
			ack = 0
			delivered = 0
			level = 0
		}
		if attempted {
			attempts++
		}
		if state == "delivered" || state == "escalated" {
			delivered = now
		}
		if state == "escalated" {
			level++
		}
		res, e := q.ExecContext(ctx, `UPDATE bead_supervision SET epoch=?,progress_seq=?,request_id=?,reason=?,state=?,target_agent_id=?,opened_at=?,phase_since=?,due_at=?,attempts=?,delivered_at=?,acknowledged_at=?,escalation_level=?,last_error=?,lease_until=0,lease_token='' WHERE project_id=? AND bead_id=? AND lease_token=?`, epoch, seq, request, reason, state, target, opened, phase, due.Unix(), attempts, delivered, ack, level, truncateSupervision(detail, 1000), f.ProjectID, f.BeadID, f.Job.Token)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		if attempted {
			_, e = q.ExecContext(ctx, `INSERT INTO supervision_attempts(project_id,bead_id,epoch,target_agent_id,reason,kind,occurred_at,detail) VALUES(?,?,?,?,?,?,?,?)`, f.ProjectID, f.BeadID, epoch, target, reason, state, now, truncateSupervision(detail, 1000))
		}
		return e
	})
}
func truncateSupervision(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Store) AcknowledgeSupervision(ctx context.Context, pid int64, bead string, epoch int64, actor string, requests ...string) error {
	if len(requests) != 1 || requests[0] == "" {
		return errors.New("supervision acknowledgement requires the current request id")
	}
	if actor == "" {
		return errors.New("supervision acknowledgement requires an agent identity")
	}
	return s.WithTx(ctx, func(q Querier) error {
		var current, progress int64
		if e := q.QueryRowContext(ctx, `SELECT responsibility_epoch,progress_seq FROM bead_tasks WHERE project_id=? AND id=? FOR UPDATE`, pid, bead).Scan(&current, &progress); e != nil {
			return e
		}
		if current != epoch {
			return errors.New("assignment has changed; read the current bead")
		}
		res, e := q.ExecContext(ctx, `UPDATE bead_supervision SET state='acknowledged',acknowledged_at=CASE WHEN acknowledged_at=0 THEN ? ELSE acknowledged_at END,due_at=CASE WHEN acknowledged_at=0 THEN ? ELSE due_at END,lease_until=0,lease_token='' WHERE project_id=? AND bead_id=? AND epoch=? AND progress_seq=? AND replace(target_agent_id,'.','-')=replace(?::text,'.','-') AND request_id=? AND state IN ('dispatching','delivered','escalated','acknowledged')`, time.Now().Unix(), time.Now().Add(time.Minute).Unix(), pid, bead, epoch, progress, actor, requests[0])
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return fmt.Errorf("no current supervision request for %s on %s", actor, bead)
		}
		return nil
	})
}

func (s *Store) ClaimSupervisionDispatch(ctx context.Context, agent string, now time.Time) (string, error) {
	token := supervisionToken()
	var got string
	e := s.db.QueryRowContext(ctx, `INSERT INTO supervision_dispatch(agent_id,next_delivery_at,lease_until,lease_token) VALUES(?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET lease_until=excluded.lease_until,lease_token=excluded.lease_token WHERE supervision_dispatch.next_delivery_at<=? AND supervision_dispatch.lease_until<=? RETURNING lease_token`, agent, now.Unix(), now.Add(45*time.Second).Unix(), token, now.Unix(), now.Unix()).Scan(&got)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return got, e
}
func (s *Store) FinishSupervisionDispatch(ctx context.Context, agent, token string, due time.Time) error {
	_, e := s.db.ExecContext(ctx, `UPDATE supervision_dispatch SET next_delivery_at=?,lease_until=0,lease_token='' WHERE agent_id=? AND lease_token=?`, due.Unix(), agent, token)
	return e
}

// PrepareSupervisionDelivery persists the obligation before crossing the runtime
// boundary. An immediate acknowledgement can therefore win the delivery race.
func (s *Store) PrepareSupervisionDelivery(ctx context.Context, f SupervisionFact, reason, target string) (SupervisionFact, bool, error) {
	prepared := false
	e := s.WithTx(ctx, func(q Querier) error {
		var epoch, seq int64
		if e := q.QueryRowContext(ctx, `SELECT responsibility_epoch,progress_seq FROM bead_tasks WHERE project_id=? AND id=? FOR UPDATE`, f.ProjectID, f.BeadID).Scan(&epoch, &seq); e != nil {
			return e
		}
		if epoch != f.Epoch || seq != f.ProgressSeq {
			return nil
		}
		now := time.Now().Unix()
		reset := f.Job.Epoch != epoch || f.Job.ProgressSeq != seq || f.Job.Reason != reason
		j := f.Job
		if reset {
			j.RequestID = supervisionToken()
			j.Attempts = 0
			j.OpenedAt = now
			j.PhaseSince = now
			j.AcknowledgedAt = 0
			j.DeliveredAt = 0
			j.EscalationLevel = 0
		}
		j.Attempts++
		j.Epoch = epoch
		j.ProgressSeq = seq
		j.Reason = reason
		j.Target = target
		j.State = "dispatching"
		res, e := q.ExecContext(ctx, `UPDATE bead_supervision SET epoch=?,progress_seq=?,request_id=?,reason=?,state='dispatching',target_agent_id=?,attempts=?,opened_at=?,phase_since=?,acknowledged_at=?,delivered_at=?,escalation_level=? WHERE project_id=? AND bead_id=? AND lease_token=?`, epoch, seq, j.RequestID, reason, target, j.Attempts, j.OpenedAt, j.PhaseSince, j.AcknowledgedAt, j.DeliveredAt, j.EscalationLevel, f.ProjectID, f.BeadID, j.Token)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO supervision_attempts(project_id,bead_id,epoch,target_agent_id,reason,kind,occurred_at) VALUES(?,?,?,?,?,'dispatching',?)`, f.ProjectID, f.BeadID, epoch, target, reason, now); e != nil {
			return e
		}
		f.Job = j
		prepared = true
		return nil
	})
	return f, prepared, e
}
