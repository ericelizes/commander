package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgbind"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresQualifiedDependencyCommand(t *testing.T) {
	dsn := os.Getenv("COMMANDER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("requires private commander_trial")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Path != "/commander_trial" {
		t.Fatal("requires commander_trial")
	}
	db, e := sql.Open(pgbind.Driver, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("qualified_cli_%d", time.Now().UnixNano())
	if _, e = db.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	if e = os.WriteFile(filepath.Join(home, "state.postgres"), []byte(u.String()), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(filepath.Join(home, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	dir, foreign := t.TempDir(), t.TempDir()
	for _, p := range []store.Project{{Name: "fixture-source", Type: "code", Path: dir, Status: "active"}, {Name: "fixture-target", Type: "code", Path: foreign, Status: "active"}} {
		if _, e = s.InsertProject(ctx, p); e != nil {
			t.Fatal(e)
		}
	}
	beads.SetDoorServer(true)
	defer beads.SetDoorServer(false)
	c := beads.New()
	source, e := c.Create(ctx, dir, "Synthetic source", beads.CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	target, e := c.Create(ctx, foreign, "Synthetic target", beads.CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(e) {
		t.Fatalf("fixture has retired state file: %v", e)
	}
	out, errs, read := withTempOutputFiles(t)
	if code := runInternalDep([]string{"--project", "fixture-source", "--target-project", "fixture-target", "add", source, target}, out, errs); code != 0 {
		a, b := read()
		t.Fatalf("qualified command %d %s %s", code, a, b)
	}
	ds, e := c.Deps(ctx, dir, source)
	if e != nil || len(ds) != 1 || ds[0].DependsOnID != target {
		t.Fatalf("source edge %v %v", ds, e)
	}
	// Source-local recorded custody makes removal legal without touching target.
	if e = c.SetLabel(ctx, dir, source, (beads.Wait{Kind: beads.WaitReview, Ref: "fixture-reviewer", Since: time.Now()}).Label(), true); e != nil {
		t.Fatal(e)
	}
	if code := runInternalDep([]string{"--project", "fixture-source", "--target-project", "fixture-target", "rm", source, target}, out, errs); code != 0 {
		a, b := read()
		t.Fatalf("qualified removal %d %s %s", code, a, b)
	}
	read()
}
