package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestPostgresJurisdictionUpgradePreservesExistingOwnership(t *testing.T) {
	s := postgresTestStoreWithSetup(t, func(db *sql.DB) {
		for _, statement := range []string{
			`CREATE TABLE commander_schema(id BIGINT PRIMARY KEY,version BIGINT NOT NULL)`,
			postgresSchema,
			`INSERT INTO commander_schema VALUES(1,1)`,
			`INSERT INTO projects(id,name,path,type,status,activated_at) VALUES(42,'existing','/existing','code','active',1)`,
			`INSERT INTO bead_tasks(project_id,id,title,labels,created_at,updated_at) VALUES(42,'root','Existing goal','["kind:objective","hold"]','now','now')`,
			`INSERT INTO bead_tasks(project_id,id,title,parent_id,created_at,updated_at) VALUES(42,'child','Existing work','root','now','now')`,
			`INSERT INTO workstream_managers(project_id,workstream_bead_id,session_file,am_role,created_at) VALUES(42,'root','retained-session','manager-existing',1)`,
		} {
			if _, e := db.Exec(statement); e != nil {
				t.Fatal(e)
			}
		}
	})
	ctx := context.Background()
	j, e := s.Jurisdiction(ctx, 42, "child")
	if e != nil || j.CommanderID != "manager-existing" {
		t.Fatalf("upgrade lost registered owner: %+v %v", j, e)
	}
	var held bool
	if e = s.DB().QueryRow(`SELECT jsonb_exists(labels,'hold') FROM bead_tasks WHERE project_id=42 AND id='root'`).Scan(&held); e != nil || !held {
		t.Fatal("upgrade lost hold", e)
	}
	if e = s.RegisterCommander(ctx, "commander-urgent", "Urgent"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TransferJurisdiction(ctx, JurisdictionTransfer{ProjectID: 42, BeadID: "root", ExpectedOwner: "manager-existing", ExpectedRevision: 1, NextCommander: "commander-urgent", Reason: "explicit handover"}); e != nil {
		t.Fatal(e)
	}
	// A stale legacy registration cannot take jurisdiction back after cutover.
	if e = s.UpsertWorkstreamManager(ctx, WorkstreamManager{ProjectID: 42, WorkstreamBeadID: "root", AmRole: "manager-old", SessionFile: "old"}); e != nil {
		t.Fatal(e)
	}
	j, e = s.Jurisdiction(ctx, 42, "child")
	if e != nil || j.CommanderID != "commander-urgent" {
		t.Fatalf("registration stole planning ownership: %+v %v", j, e)
	}
}
