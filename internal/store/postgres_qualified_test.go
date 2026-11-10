package store

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/pgbind"
)

func TestPostgresQualifiedUpgradeAndRejectUnpublishedV2(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	// A separate private schema starts at the exact shipped schema-1 shape.
	u, e := url.Parse(s.postgresDSN)
	if e != nil {
		t.Fatal(e)
	}
	schema := u.Query().Get("search_path") + "_upgrade"
	if _, e := s.DB().Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer s.DB().Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	raw, e := sql.Open(pgbind.Driver, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	if _, e = raw.Exec(postgresSchema); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`CREATE TABLE commander_schema(id BIGINT PRIMARY KEY,version BIGINT NOT NULL);INSERT INTO commander_schema VALUES(1,1)`); e != nil {
		t.Fatal(e)
	}
	base := &Store{db: raw, postgres: true}
	p, e := base.InsertProject(ctx, Project{Name: "fixture-upgrade", Type: "code", Path: t.TempDir(), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) VALUES(?,'fixture-1','Source','now','now'),(?,'fixture-2','Target','now','now')`, p, p); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`INSERT INTO bead_dependencies(project_id,issue_id,depends_on_id,type) VALUES(?,'fixture-1','fixture-2','blocks')`, p); e != nil {
		t.Fatal(e)
	}
	upgraded, e := OpenPostgres(u.String())
	if e != nil {
		t.Fatal(e)
	}
	upgraded.Close()
	var target, version int64
	if e = raw.QueryRow(`SELECT target_project_id FROM bead_dependencies`).Scan(&target); e != nil || target != p {
		t.Fatalf("backfill %d %v", target, e)
	}
	if e = raw.QueryRow(`SELECT version FROM commander_schema`).Scan(&version); e != nil || version != 3 {
		t.Fatalf("provisional version %d %v", version, e)
	}
	if _, e = raw.Exec(`UPDATE commander_schema SET version=2`); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenPostgres(u.String()); e == nil {
		opened.Close()
		t.Fatal("silently accepted unrelated unpublished v2")
	} else if !strings.Contains(e.Error(), "unsupported Postgres schema 2") {
		t.Fatal(e)
	}
	if e = raw.QueryRow(`SELECT version FROM commander_schema`).Scan(&version); e != nil || version != 2 {
		t.Fatalf("refusal mutated version %d %v", version, e)
	}
}
