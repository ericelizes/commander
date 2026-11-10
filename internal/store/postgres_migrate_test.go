package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPostgresPopulatedMigrationPreservesIdentityAndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	source, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	ctx := context.Background()
	id, e := source.InsertProject(ctx, Project{Name: "preserved", Type: "code", Path: "/migration/source", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if e = source.SetAgentState(ctx, "manager-preserved", AgentWorking, "reviewing"); e != nil {
		t.Fatal(e)
	}
	if _, e = source.DB().Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	if _, e = source.DB().Exec(`INSERT INTO cost_events(id,project_id,agent_name,event_type,occurred_at) VALUES(99,0,'fleet','legacy',1)`); e != nil {
		t.Fatal(e)
	}
	dest := postgresTestStore(t)
	counts, e := dest.ImportSQLite(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	if counts["projects"] != 1 || counts["agent_state"] != 1 {
		t.Fatalf("counts: %#v", counts)
	}
	p, e := dest.GetProject(ctx, id)
	if e != nil || p == nil || p.Name != "preserved" {
		t.Fatalf("identity changed: %#v %v", p, e)
	}
	next, e := dest.InsertProject(ctx, Project{Name: "next", Type: "code", Path: "/migration/next", Status: "active"})
	if e != nil || next <= id {
		t.Fatalf("identity sequence not advanced: %d %v", next, e)
	}
	if _, e = dest.ImportSQLite(ctx, path); e == nil {
		t.Fatal("overwrote a populated destination")
	}
	if p, e = source.GetProject(ctx, id); e != nil || p.Name != "preserved" {
		t.Fatalf("source modified: %#v %v", p, e)
	}
}
