package livedata

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/pgbind"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresQualifiedDisplayRevisionAndAmbiguity(t *testing.T) {
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
	schema := fmt.Sprintf("qualified_display_%d", time.Now().UnixNano())
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
	pid, e := s.InsertProject(ctx, store.Project{Name: "display-source", Type: "code", Path: dir, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.InsertProject(ctx, store.Project{Name: "display-target", Type: "code", Path: foreign, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	beads.SetDoorServer(true)
	defer beads.SetDoorServer(false)
	c := beads.New()
	source, e := c.Create(ctx, dir, "Synthetic source", beads.CreateOptions{Silent: true, Labels: []string{"kind:objective"}})
	if e != nil {
		t.Fatal(e)
	}
	target, e := c.Create(ctx, foreign, "Synthetic target", beads.CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = beads.QualifiedDependency(ctx, dir, foreign, "add", source, target); e != nil {
		t.Fatal(e)
	}
	cache := &projectRowsCache{}
	projects := []store.Project{{ID: pid, Name: "display-source", Path: dir}}
	rows, e := objectiveRowsFromBeadsCached(ctx, c, projects, cache)
	if e != nil || len(rows) != 1 || !slices.Contains(rows[0].BlockedBy, target) || len(rows[0].DoneBlockers) != 0 {
		t.Fatalf("foreign blocked display: %+v %v", rows, e)
	}
	if e = c.SetLabel(ctx, dir, source, (beads.Wait{Kind: beads.WaitReview, Ref: "fixture-reviewer", Since: time.Now()}).Label(), true); e != nil {
		t.Fatal(e)
	}
	if e = c.Close(ctx, foreign, target); e != nil {
		t.Fatal(e)
	}
	rows, e = objectiveRowsFromBeadsCached(ctx, c, projects, cache)
	if e != nil || len(rows) != 1 || !slices.Contains(rows[0].DoneBlockers, target) {
		t.Fatalf("foreign completed display: %+v %v", rows, e)
	}
	third, e := s.InsertProject(ctx, store.Project{Name: "display-third", Type: "code", Path: t.TempDir(), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO bead_tasks(project_id,id,title,created_at,updated_at) SELECT ?,id,title,created_at,updated_at FROM bead_tasks WHERE project_id=? AND id=?`, third, other, target); e != nil {
		t.Fatal(e)
	}
	rows, e = objectiveRowsFromBeadsCached(ctx, c, projects, cache)
	if !beads.IsCrossProjectError(e) || len(rows) != 0 {
		t.Fatalf("ambiguous cached rows reused: %+v %v", rows, e)
	}
}
