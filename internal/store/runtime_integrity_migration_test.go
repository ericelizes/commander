package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestSchemaTwoUpgradePreservesLiveCustodyAndHumanDirection(t *testing.T) {
	s := postgresTestStoreWithSetup(t, func(db *sql.DB) {
		for _, statement := range []string{
			`CREATE TABLE commander_schema(id BIGINT PRIMARY KEY,version BIGINT NOT NULL)`, postgresSchema, `INSERT INTO commander_schema VALUES(1,1)`,
			`INSERT INTO projects(id,name,path,type,status,activated_at) VALUES(42,'existing','/existing','code','active',1)`,
			`INSERT INTO bead_tasks(project_id,id,title,status,labels,created_at,updated_at) VALUES(42,'live','Existing work','open','["hold"]','now','now'),(42,'closed','Historical closure','closed','[]','now','now')`,
			`INSERT INTO agent_runs(id,project_id,task_bead_id,agent_name,opencode_session_id,spawn_time,status) VALUES(17,42,'live','operator-existing','retained-native-session',1,'running')`,
			`UPDATE bead_tasks SET active_run_id=17 WHERE project_id=42 AND id='live'`,
			`INSERT INTO pitches(project_id,bead_id,version,title,decision,created_at,answer,answered_at) VALUES(42,'live',1,'Recorded hold','{"question":"Ship?","options":[{"label":"Hold","approves":false}]}',1,'Hold',2)`,
			jurisdictionSchema, beadQuestionsSchema, executionSchema, supervisionSchema, `UPDATE commander_schema SET version=2`,
			`UPDATE bead_tasks SET progress_seq=73,progress_at=123,responsibility_epoch=19 WHERE id='live'`,
			`UPDATE agent_runs SET execution_driver='native',execution_runtime='claude',execution_account='kept-account',execution_model='haiku' WHERE id=17`,
			`INSERT INTO agent_sessions(role,session_id,source_seq,updated_at) VALUES('operator-existing','retained-native-session',9,123)`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	})
	ctx := context.Background()
	var version, progress, epoch, cursor, runID int64
	var session, labels, answer, driver, account string
	if err := s.DB().QueryRow(`SELECT version FROM commander_schema`).Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	if err := s.DB().QueryRow(`SELECT progress_seq,responsibility_epoch,labels::text,active_run_id FROM bead_tasks WHERE id='live'`).Scan(&progress, &epoch, &labels, &runID); err != nil || progress != 73 || epoch != 19 || labels != `["hold"]` || runID != 17 {
		t.Fatal("migration rewrote live work", progress, epoch, labels, runID, err)
	}
	if err := s.DB().QueryRow(`SELECT answer FROM pitches WHERE bead_id='live'`).Scan(&answer); err != nil || answer != "Hold" {
		t.Fatal("lost human direction", answer, err)
	}
	if err := s.DB().QueryRow(`SELECT session_id,source_seq,run_id FROM agent_sessions WHERE role='operator-existing'`).Scan(&session, &cursor, &runID); err != nil || session != "retained-native-session" || cursor != 9 || runID != 0 {
		t.Fatal("old classifier evidence became attributed work", session, cursor, runID, err)
	}
	if err := s.DB().QueryRow(`SELECT execution_driver,execution_account FROM agent_runs WHERE id=17`).Scan(&driver, &account); err != nil || driver != "native" || account != "kept-account" {
		t.Fatal(driver, account, err)
	}
	var state string
	if err := s.DB().QueryRow(`SELECT state FROM bead_supervision WHERE bead_id='closed'`).Scan(&state); err != nil || state != "resolved" {
		t.Fatal("historical work entered recovery", state, err)
	}
	if err := s.migratePostgres(ctx); err != nil {
		t.Fatal("idempotent reopen", err)
	}
}

func TestConsolidationOwnerTraversalMatchesCanonicalDatabase(t *testing.T) {
	s, _, _ := consolidationFixture(t)
	ctx := context.Background()
	inv, beads, err := legacyPlanningInventory(ctx, s.DB(), 42, false)
	if err != nil {
		t.Fatal(err)
	}
	boundaries := map[string]string{}
	rows, err := s.DB().Query(`SELECT bead_id,commander_id FROM commander_jurisdictions WHERE project_id=42`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, owner string
		if err = rows.Scan(&id, &owner); err != nil {
			t.Fatal(err)
		}
		boundaries[id] = owner
	}
	rows.Close()
	owners := resolveConsolidationOwners(beads, inv.DefaultCommander, boundaries)
	for _, bead := range beads {
		var expected string
		if err = s.DB().QueryRow(`SELECT commander_planning_owner(42,?)`, bead.ID).Scan(&expected); err != nil || owners[bead.ID] != expected {
			t.Fatal(bead.ID, owners[bead.ID], expected, err)
		}
	}
	deleted := sql.NullInt64{Int64: 1, Valid: true}
	graph := []consolidationBead{{ID: "deleted", Deleted: deleted}, {ID: "under-deleted", Parent: "deleted"}, {ID: "cycle-a", Parent: "cycle-b"}, {ID: "cycle-b", Parent: "cycle-a"}, {ID: "missing-parent", Parent: "missing"}}
	got := resolveConsolidationOwners(graph, "commander", map[string]string{"deleted": "manager-old", "cycle-b": "commander-focused"})
	for _, id := range []string{"deleted", "under-deleted", "missing-parent"} {
		if got[id] != "commander" {
			t.Fatal("invalid ancestry claimed jurisdiction", id, got)
		}
	}
	for _, id := range []string{"cycle-a", "cycle-b"} {
		if got[id] != "commander-focused" {
			t.Fatal("cycle lost closest override", id, got)
		}
	}
}
