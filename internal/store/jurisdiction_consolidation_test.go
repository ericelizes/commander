package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func consolidationFixture(t *testing.T) (*Store, JurisdictionConsolidation, string) {
	t.Helper()
	s := postgresTestStoreWithSetup(t, func(db *sql.DB) {
		for _, statement := range []string{
			`CREATE TABLE commander_schema(id BIGINT PRIMARY KEY,version BIGINT NOT NULL)`, postgresSchema, `INSERT INTO commander_schema VALUES(1,1)`,
			`INSERT INTO projects(id,name,path,type,status,activated_at) VALUES(42,'consolidation','/consolidation','code','active',1)`,
			`INSERT INTO bead_tasks(project_id,id,parent_id,title,description,labels,created_at,updated_at) VALUES(42,'home',NULL,'Work','Existing goal','["kind:objective"]','now','now'),(42,'nested','home','Nested scope','','["kind:objective"]','now','now'),(42,'urgent','nested','Urgent scope','','["kind:objective"]','now','now'),(42,'keep','home','Explicit retained scope','','["kind:objective"]','now','now'),(42,'live','nested','Running work','The current assignment','["kind:task"]','now','now'),(42,'held','home','Held action','Original approved scope','["kind:objective","hold","needs-user"]','now','now')`,
			`INSERT INTO workstream_managers(project_id,workstream_bead_id,session_file,am_role,created_at) VALUES(42,'home','keep-root-chat','manager-home',1),(42,'nested','keep-nested-chat','manager-nested',1),(42,'keep','keep-explicit-chat','manager-kept',1)`,
			`INSERT INTO agent_runs(id,project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status,tmux_pane_id,model) VALUES(17,42,'live','executor-current','live-session',1,'running','keep-execution-pane','claude')`,
			`UPDATE bead_tasks SET active_run_id=17 WHERE project_id=42 AND id='live'`,
			`INSERT INTO pitches(project_id,bead_id,version,title,decision,created_at,answer,answered_at) VALUES(42,'held',1,'Original question','{"question":"Resume original scope?","options":[{"label":"Resume","approves":true}]}',1,'Resume',2)`,
			`INSERT INTO needs_resolutions(project_id,bead_id,generation,payload,state) VALUES(42,'held','1','{"ProjectID":42,"BeadID":"held","Generation":"1","AnswerID":"retained-human-answer","Action":"decide","Choice":"Resume","Approved":true,"Manager":"manager-home"}',0)`,
		} {
			if _, e := db.Exec(statement); e != nil {
				t.Fatal(e)
			}
		}
	})
	ctx := context.Background()
	if e := s.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: 42, BeadID: "urgent", ExpectedOwner: "manager-nested", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "new focused conversation"}); e != nil {
		t.Fatal(e)
	}
	// The user can explicitly retain an imported identity. Provenance must change
	// even when its role string and effective owner stay the same.
	if _, e := s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: 42, BeadID: "keep", ExpectedOwner: "manager-kept", ExpectedRevision: 2, NextCommander: "manager-kept", Reason: "explicitly keep this scope"}); e != nil {
		t.Fatal(e)
	}
	qid, e := s.AskQuestion(ctx, QuestionRequest{ProjectID: 42, BeadID: "live", Actor: "executor-current", Session: "live-session", RunID: 17, ClientKey: "current-question", PlanRevision: 1, Question: "Which safe local path should I use?", Blocking: false})
	if e != nil {
		t.Fatal(e)
	}
	inventory, e := s.LegacyPlanningInventory(ctx, 42)
	if e != nil {
		t.Fatal(e)
	}
	if len(inventory.Boundaries) != 2 {
		t.Fatal("import provenance lost or explicit boundary included", inventory)
	}
	return s, JurisdictionConsolidation{ProjectID: 42, Revision: inventory.Revision, DefaultCommander: inventory.DefaultCommander, ControlRevision: inventory.ControlRevision, Snapshot: inventory.Snapshot, RequestID: "consolidate-reviewed-scopes", Reason: "Replace only imported planning ownership with Commander jurisdiction"}, qid
}
func TestPostgresJurisdictionConsolidationPreservesClaimsDecisionsAndOverrides(t *testing.T) {
	s, req, qid := consolidationFixture(t)
	ctx := context.Background()
	type unchanged struct {
		ID, Labels     string
		Run            sql.NullInt64
		Plan, Progress int64
	}
	snapshot := func() []unchanged {
		t.Helper()
		rows, e := s.DB().Query(`SELECT id,labels::text,active_run_id,plan_revision,progress_seq FROM bead_tasks WHERE project_id=42 ORDER BY id`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := []unchanged{}
		for rows.Next() {
			var b unchanged
			if e = rows.Scan(&b.ID, &b.Labels, &b.Run, &b.Plan, &b.Progress); e != nil {
				t.Fatal(e)
			}
			out = append(out, b)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	before := snapshot()
	var journal string
	if e := s.DB().QueryRow(`SELECT payload FROM needs_resolutions WHERE project_id=42 AND bead_id='held'`).Scan(&journal); e != nil {
		t.Fatal(e)
	}
	result, e := s.ConsolidateLegacyPlanning(ctx, "commander", req)
	if e != nil || result.Revision != 5 || len(result.Removed) != 2 || len(result.RetiredPlanningCandidates) != 2 {
		t.Fatal(result, e)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("consolidation changed work/claims/holds/progress", before, after)
	}
	for id, owner := range map[string]string{"home": "commander", "nested": "commander", "live": "commander", "held": "commander", "urgent": "commander-urgent", "keep": "manager-kept"} {
		j, e := s.Jurisdiction(ctx, 42, id)
		if e != nil || j.CommanderID != owner {
			t.Fatal("wrong effective owner", id, j, e)
		}
	}
	var custodian, session, status, pane string
	if e = s.DB().QueryRow(`SELECT b.responsible_agent_id,a.opencode_session_id,a.status,a.tmux_pane_id FROM bead_tasks b JOIN agent_runs a ON a.id=b.active_run_id WHERE b.project_id=42 AND b.id='live'`).Scan(&custodian, &session, &status, &pane); e != nil || custodian != "executor-current" || session != "live-session" || status != "running" || pane != "keep-execution-pane" {
		t.Fatal("live run custody changed", custodian, session, status, pane, e)
	}
	var afterJournal string
	var state int
	if e = s.DB().QueryRow(`SELECT payload,state FROM needs_resolutions WHERE project_id=42 AND bead_id='held'`).Scan(&afterJournal, &state); e != nil || afterJournal != journal || state != 0 {
		t.Fatal("human answer rewritten", afterJournal, state, e)
	}
	q, e := s.Question(ctx, qid)
	if e != nil || q.State != "pending" || q.Scopes[0].CommanderID != "commander" {
		t.Fatal("execution question did not follow current owner", q, e)
	}
	// Existing chat addresses are retained as history, not killed by a DB change.
	var registrations int
	if e = s.DB().QueryRow(`SELECT count(*) FROM workstream_managers WHERE project_id=42`).Scan(&registrations); e != nil || registrations != 3 {
		t.Fatal("legacy conversations erased", registrations, e)
	}
	if _, e = s.SetOrchestrationControl(ctx, 1, "active", "receipt replay test"); e != nil {
		t.Fatal(e)
	}
	replay, e := s.ConsolidateLegacyPlanning(ctx, "commander", req)
	if e != nil || !replay.Replayed || replay.Revision != result.Revision {
		t.Fatal("lost-response replay failed after activation", replay, e)
	}
	req.Reason = "Changed consolidation intent"
	if _, e = s.ConsolidateLegacyPlanning(ctx, "commander", req); e == nil {
		t.Fatal("changed intent reused receipt")
	}
}
func TestPostgresJurisdictionConsolidationRejectsStaleInventoryAndForeignActors(t *testing.T) {
	for _, change := range []string{"actor", "revision", "snapshot", "new-bead", "explicit-transfer", "active", "default", "control"} {
		t.Run(change, func(t *testing.T) {
			s, req, _ := consolidationFixture(t)
			actor := "commander"
			var e error
			switch change {
			case "actor":
				actor = "commander-urgent"
			case "revision":
				req.Revision++
			case "snapshot":
				req.Snapshot = "0000000000000000000000000000000000000000000000000000000000000000"
			case "new-bead":
				_, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,parent_id,title,created_at,updated_at) VALUES(42,'new-work','nested','New work','now','now')`)
			case "explicit-transfer":
				_, e = s.TransferJurisdiction(context.Background(), JurisdictionTransfer{ProjectID: 42, BeadID: "nested", ExpectedOwner: "manager-nested", ExpectedRevision: req.Revision, NextCommander: "manager-nested", Reason: "new explicit choice after inventory"})
			case "active":
				_, e = s.SetOrchestrationControl(context.Background(), 1, "active", "wrong cutover phase")
			case "default":
				req.DefaultCommander = "commander-urgent"
			case "control":
				req.ControlRevision++
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.ConsolidateLegacyPlanning(context.Background(), actor, req); e == nil {
				t.Fatal("unsafe migration accepted", change)
			}
			var receipts int
			if e = s.DB().QueryRow(`SELECT count(*) FROM jurisdiction_consolidations`).Scan(&receipts); e != nil || receipts != 0 {
				t.Fatal("failed migration left receipt", receipts, e)
			}
		})
	}
}
func TestPostgresJurisdictionConsolidationRollsBackLateFailureAndConcurrentRetry(t *testing.T) {
	s, req, _ := consolidationFixture(t)
	if _, e := s.DB().Exec(`CREATE FUNCTION fail_consolidation_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'late receipt failure'; END $$; CREATE TRIGGER fail_consolidation_fixture BEFORE INSERT ON jurisdiction_consolidations FOR EACH ROW EXECUTE FUNCTION fail_consolidation_fixture()`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ConsolidateLegacyPlanning(context.Background(), "commander", req); e == nil {
		t.Fatal("late failure did not refuse")
	}
	inventory, e := s.LegacyPlanningInventory(context.Background(), 42)
	if e != nil || inventory.Snapshot != req.Snapshot || inventory.Revision != req.Revision || len(inventory.Boundaries) != 2 {
		t.Fatal("failed migration leaked ownership/history", inventory, e)
	}
	if _, e = s.DB().Exec(`DROP TRIGGER fail_consolidation_fixture ON jurisdiction_consolidations`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan JurisdictionConsolidationResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.ConsolidateLegacyPlanning(context.Background(), "commander", req)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	replays := 0
	for result := range results {
		if result.Replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatal("concurrent retry repeated migration", replays)
	}
	var histories int
	if e = s.DB().QueryRow(`SELECT count(*) FROM jurisdiction_history WHERE project_id=42`).Scan(&histories); e != nil || histories != 4 {
		t.Fatal("history duplicated or lost", histories, e)
	}
	raw, e := json.Marshal(inventory)
	if e != nil || len(raw) == 0 {
		t.Fatal(e)
	}
}

func TestPostgresJurisdictionConsolidationDoesNotFenceOnHeartbeatOrExecutionProgress(t *testing.T) {
	s, req, _ := consolidationFixture(t)
	if _, e := s.DB().Exec(`UPDATE agent_runs SET active_at=active_at+1,tokens_used=tokens_used+100 WHERE id=17`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().Exec(`UPDATE bead_tasks SET progress_seq=progress_seq+1 WHERE project_id=42 AND id='live'`); e != nil {
		t.Fatal(e)
	}
	inventory, e := s.LegacyPlanningInventory(context.Background(), 42)
	if e != nil || inventory.Snapshot != req.Snapshot {
		t.Fatal("runtime progress invalidated planning snapshot", inventory, e)
	}
	if _, e = s.ConsolidateLegacyPlanning(context.Background(), "commander", req); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresJurisdictionConsolidationPreservesExecutionUnderRetiredPlannerIdentity(t *testing.T) {
	s, req, _ := consolidationFixture(t)
	if _, e := s.DB().Exec(`UPDATE agent_runs SET agent_name='manager-nested' WHERE id=17`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().Exec(`UPDATE bead_tasks SET responsible_agent_id='manager-nested' WHERE project_id=42 AND id='live'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ConsolidateLegacyPlanning(context.Background(), "commander", req); e != nil {
		t.Fatal(e)
	}
	var responsible, agent, status string
	var runID int64
	if e := s.DB().QueryRow(`SELECT b.responsible_agent_id,b.active_run_id,a.agent_name,a.status FROM bead_tasks b JOIN agent_runs a ON a.id=b.active_run_id WHERE b.project_id=42 AND b.id='live'`).Scan(&responsible, &runID, &agent, &status); e != nil || responsible != "manager-nested" || agent != "manager-nested" || runID != 17 || status != "running" {
		t.Fatal("planning consolidation took execution custody by role name", responsible, runID, agent, status, e)
	}
}

func TestPostgresJurisdictionConsolidationInventoryOfNewProjectIsReadOnly(t *testing.T) {
	s := postgresTestStore(t)
	pid, e := s.InsertProject(context.Background(), Project{Name: "new-project", Type: "knowledge", Path: t.TempDir(), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	inventory, e := s.LegacyPlanningInventory(context.Background(), pid)
	if e != nil || inventory.DefaultCommander != "commander" || inventory.Revision != 1 || len(inventory.Boundaries) != 0 {
		t.Fatal(inventory, e)
	}
	var count int
	if e = s.DB().QueryRow(`SELECT count(*) FROM jurisdiction_projects WHERE project_id=?`, pid).Scan(&count); e != nil || count != 0 {
		t.Fatal("read inventory created ownership rows", count, e)
	}
}
