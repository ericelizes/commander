package beads

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/pgtest"
	"github.com/example-user/commander/internal/store"
)

func TestPostgresSQLiteImportSeedsNestedPlanningJurisdictions(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source, e := store.Open(sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	pid, e := source.InsertProject(ctx, store.Project{Name: "imported", Path: t.TempDir(), Type: "code", Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"root", "nested"} {
		if e = source.UpsertWorkstreamManager(ctx, store.WorkstreamManager{ProjectID: pid, WorkstreamBeadID: id, AmRole: "manager-" + id, SessionFile: id + ".session"}); e != nil {
			t.Fatal(e)
		}
	}
	dest, _ := pgtest.Open(t)
	if _, e = dest.ImportSQLite(ctx, sourcePath); e != nil {
		t.Fatal(e)
	}
	data := `{"id":"root","title":"Root","status":"open","labels":["kind:workstream","hold"]}
{"id":"nested","title":"Nested","status":"open","parent_id":"root","labels":["kind:workstream"]}
{"id":"unrelated-id","title":"Leaf","status":"open","parent_id":"nested"}
`
	if _, e = ImportPostgresProject(ctx, dest, pid, strings.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	for bead, owner := range map[string]string{"root": "manager-root", "nested": "manager-nested", "unrelated-id": "manager-nested"} {
		j, e := dest.Jurisdiction(ctx, pid, bead)
		if e != nil || j.CommanderID != owner {
			t.Fatalf("imported %s=%+v %v", bead, j, e)
		}
		var responsible string
		if e = dest.DB().QueryRowContext(ctx, `SELECT responsible_agent_id FROM bead_tasks WHERE project_id=? AND id=?`, pid, bead).Scan(&responsible); e != nil || responsible != owner {
			t.Fatalf("imported responsibility %s=%s %v", bead, responsible, e)
		}
	}
}
