package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func reconciliationFixture(t *testing.T) (*Store, DecisionReconciliation, string) {
	t.Helper()
	s := postgresTestStore(t)
	ctx := context.Background()
	pid, e := s.InsertProject(ctx, Project{Name: "decision-recovery", Path: t.TempDir(), Type: "knowledge", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,description,labels,created_at,updated_at) VALUES(?,'held','Held work','Original bounded work','["kind:objective","hold","needs-user"]','now','now')`, pid); e != nil {
		t.Fatal(e)
	}
	decision := &Decision{Question: "Resume original work?", Options: []DecisionOption{{Label: "Resume", Approves: true}, {Label: "Keep held"}}}
	if e = s.RecordPitch(ctx, pid, "held", "Resume original work?", decision.JSON(), nil); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveNeedsResolution(ctx, NeedsResolution{ProjectID: pid, BeadID: "held", Generation: "1", AnswerID: "original-answer", Action: "decide", Choice: "Resume", Approved: true, Manager: "commander", InputPlanRevision: 1}, "Resume"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetOrchestrationControl(ctx, 1, "active", "reconciliation fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`UPDATE bead_tasks SET description='Changed release conditions' WHERE project_id=? AND id='held'`, pid); e != nil {
		t.Fatal(e)
	}
	var version int64
	var original string
	if e = s.DB().QueryRow(`SELECT plan_revision,(SELECT payload FROM needs_resolutions WHERE project_id=? AND bead_id='held' AND generation='1') FROM bead_tasks WHERE project_id=? AND id='held'`, pid, pid).Scan(&version, &original); e != nil {
		t.Fatal(e)
	}
	return s, DecisionReconciliation{ProjectID: pid, BeadID: "held", RequestID: "recover-old-answer", PlanRevision: version, JurisdictionRevision: 1, Generation: "1", AnswerID: "original-answer", Reason: "The saved Resume covered the previous conditions; these changed conditions need a new human decision", Ask: "Resume changed work?", Decision: &Decision{Question: "Resume the changed release conditions?", Context: "The original answer remains in history and does not authorize this expanded scope.", Options: decision.Options}}, original
}
func TestPostgresDecisionReconciliationPreservesAnswerHoldAndHistory(t *testing.T) {
	s, req, original := reconciliationFixture(t)
	ctx := context.Background()
	var progress int64
	if e := s.DB().QueryRow(`SELECT progress_seq FROM bead_tasks WHERE project_id=? AND id='held'`, req.ProjectID).Scan(&progress); e != nil {
		t.Fatal(e)
	}
	result, e := s.ReconcileDecision(ctx, "commander", req)
	if e != nil || result.Version != 2 || result.Replayed {
		t.Fatal(result, e)
	}
	var payload, oldAnswer, status string
	var state, authorities int
	var held, allowed, pending bool
	var after int64
	e = s.DB().QueryRow(`SELECT r.payload,r.state,p.answer,b.status,jsonb_exists(b.labels,'hold'),commander_execution_allowed(b.project_id,b.id),commander_pending_human_answer(b.project_id,b.id),b.progress_seq,(SELECT count(*) FROM decision_authorities WHERE project_id=b.project_id AND bead_id=b.id AND generation='2') FROM needs_resolutions r JOIN pitches p ON p.project_id=r.project_id AND p.bead_id=r.bead_id AND CAST(p.version AS TEXT)=r.generation JOIN bead_tasks b ON b.project_id=r.project_id AND b.id=r.bead_id WHERE r.project_id=? AND r.bead_id='held' AND r.generation='1'`, req.ProjectID).Scan(&payload, &state, &oldAnswer, &status, &held, &allowed, &pending, &after, &authorities)
	if e != nil || payload != original || state != ResolutionSuperseded || oldAnswer != "Resume" || status != "open" || !held || allowed || pending || progress != after || authorities != 0 {
		t.Fatal("reconciliation rewrote answer or granted permission", payload, state, oldAnswer, status, held, allowed, pending, progress, after, authorities, e)
	}
	card, e := s.GetNeedsYou(ctx, req.ProjectID, "held")
	if e != nil || card == nil || card.Decision != req.Decision.JSON() {
		t.Fatal("replacement decision missing", card, e)
	}
	replay, e := s.ReconcileDecision(ctx, "commander", req)
	if e != nil || !replay.Replayed || replay.Version != 2 {
		t.Fatal("stale-basis replay did not retain receipt", replay, e)
	}
	changed := req
	changed.Reason = "Different intent under the same request key"
	if _, e = s.ReconcileDecision(ctx, "commander", changed); e == nil {
		t.Fatal("changed reconciliation reused receipt")
	}
	// Only a new explicit answer creates authority for the replacement question.
	if e = s.SaveNeedsResolution(ctx, NeedsResolution{ProjectID: req.ProjectID, BeadID: "held", Generation: "2", AnswerID: "new-answer", Action: "decide", Choice: "Keep held", Approved: false, Manager: "commander", InputPlanRevision: req.PlanRevision}, "Keep held"); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresDecisionReconciliationRefusesWrongSourceOwnerAndCurrentApproval(t *testing.T) {
	for _, change := range []string{"actor", "answer", "generation", "version", "jurisdiction", "unchanged", "applied", "missing-journal"} {
		t.Run(change, func(t *testing.T) {
			s, req, _ := reconciliationFixture(t)
			actor := "commander"
			var e error
			switch change {
			case "actor":
				actor = "executor-unknown"
			case "answer":
				req.AnswerID = "another-answer"
			case "generation":
				req.Generation = "0"
			case "version":
				req.PlanRevision++
			case "jurisdiction":
				req.JurisdictionRevision++
			case "unchanged":
				e = reconciliationFixtureSQL(s, req.ProjectID, `UPDATE pitch_authority_bases SET scope_epoch=(SELECT human_scope_epoch FROM bead_tasks WHERE project_id=? AND id='held') WHERE project_id=? AND bead_id='held'; UPDATE decision_authorities SET authority_epoch=(SELECT human_authority_epoch FROM bead_tasks WHERE project_id=? AND id='held') WHERE project_id=? AND bead_id='held'`)
			case "applied":
				e = s.AdvanceNeedsResolution(context.Background(), NeedsResolution{ProjectID: req.ProjectID, BeadID: "held", Generation: "1"}, ResolutionClosed)
			case "missing-journal":
				e = reconciliationFixtureSQL(s, req.ProjectID, `DELETE FROM decision_authorities WHERE project_id=?; DELETE FROM needs_resolutions WHERE project_id=?`)
				req.AnswerID = ""
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.ReconcileDecision(context.Background(), actor, req); e == nil {
				t.Fatal("unsafe reconciliation accepted", change)
			}
			var count int
			if e = s.DB().QueryRow(`SELECT count(*) FROM pitches WHERE project_id=?`, req.ProjectID).Scan(&count); e != nil || count != 1 {
				t.Fatal("refusal created a new question", count, e)
			}
		})
	}
}
func TestPostgresDecisionReconciliationAtomicLateFailure(t *testing.T) {
	s, req, original := reconciliationFixture(t)
	if _, e := s.DB().Exec(`CREATE FUNCTION fail_reconciliation_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'late receipt failure'; END $$; CREATE TRIGGER fail_reconciliation_fixture BEFORE INSERT ON human_decision_reconciliations FOR EACH ROW EXECUTE FUNCTION fail_reconciliation_fixture()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileDecision(context.Background(), "commander", req); e == nil || !strings.Contains(e.Error(), "late receipt failure") {
		t.Fatal("late failure did not run", e)
	}
	var raw string
	var state, pitches, cards int
	if e := s.DB().QueryRow(`SELECT payload,state,(SELECT count(*) FROM pitches WHERE project_id=?),(SELECT count(*) FROM needs_you WHERE project_id=?) FROM needs_resolutions WHERE project_id=? AND bead_id='held' AND generation='1'`, req.ProjectID, req.ProjectID, req.ProjectID).Scan(&raw, &state, &pitches, &cards); e != nil || raw != original || state != 0 || pitches != 1 || cards != 0 {
		t.Fatal("late failure leaked partial recovery", state, pitches, cards, e)
	}
}
func TestPostgresDecisionReconciliationRebaselinesOnlyUnansweredHistoricalQuestion(t *testing.T) {
	s, req, _ := reconciliationFixture(t)
	if e := reconciliationFixtureSQL(s, req.ProjectID, `DELETE FROM decision_authorities WHERE project_id=?; DELETE FROM needs_resolutions WHERE project_id=?; UPDATE pitches SET answer='',answered_at=0 WHERE project_id=?; DELETE FROM pitch_authority_bases WHERE project_id=?`); e != nil {
		t.Fatal(e)
	}
	req.AnswerID = ""
	result, e := s.ReconcileDecision(context.Background(), "commander", req)
	if e != nil || result.Version != 2 {
		t.Fatal(result, e)
	}
	var raw string
	if e = s.DB().QueryRow(`SELECT decision FROM pitches WHERE project_id=? AND version=2`, req.ProjectID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var d Decision
	if e = json.Unmarshal([]byte(raw), &d); e != nil || d.Question != req.Decision.Question {
		t.Fatal(d, e)
	}
	var count int
	if e = s.DB().QueryRow(`SELECT count(*) FROM decision_authorities WHERE project_id=?`, req.ProjectID).Scan(&count); e != nil || count != 0 {
		t.Fatal("historical rebaseline minted approval", count, e)
	}
}

func reconciliationFixtureSQL(s *Store, pid int64, queries string) error {
	for _, query := range strings.Split(queries, ";") {
		args := []any{}
		for i := 0; i < strings.Count(query, "?"); i++ {
			args = append(args, pid)
		}
		if _, e := s.DB().Exec(query, args...); e != nil {
			return e
		}
	}
	return nil
}

func TestPostgresDecisionReconciliationConcurrentRequestsRaiseOneQuestion(t *testing.T) {
	s, req, _ := reconciliationFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"reconcile-a", "reconcile-b"} {
		r := req
		r.RequestID = key
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.ReconcileDecision(context.Background(), "commander", r)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("competing recoveries raised duplicate decisions", success)
	}
	var count int
	if e := s.DB().QueryRow(`SELECT count(*) FROM pitches WHERE project_id=?`, req.ProjectID).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	entries, e := s.ListNeedsResolutions(context.Background(), req.ProjectID, req.BeadID)
	if e != nil || len(entries) != 1 || entries[0].Applied || entries[0].Mailed || entries[0].State != ResolutionSuperseded {
		t.Fatal("obsolete answer reported as applied", entries, e)
	}
}
