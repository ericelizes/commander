package beads

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgbind"
	"github.com/example-user/commander/internal/store"
)

func postgresBeads(t *testing.T) (Client, string, *store.Store) {
	t.Helper()
	dsn := os.Getenv("COMMANDER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable Postgres")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Path != "/commander_trial" {
		t.Fatal("tests require commander_trial")
	}
	db, e := sql.Open(pgbind.Driver, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("beads_%d", time.Now().UnixNano())
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
	t.Cleanup(func() { s.Close() })
	dir := t.TempDir()
	if _, e = s.InsertProject(context.Background(), store.Project{Name: "beads", Type: "code", Path: dir, Status: "active"}); e != nil {
		t.Fatal(e)
	}
	prior := inDoorServer
	SetDoorServer(true)
	t.Cleanup(func() { SetDoorServer(prior) })
	return New(), dir, s
}

func TestPostgresBeadLifecycleAndAtomicRollback(t *testing.T) {
	c, dir, s := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	root, e := c.Create(ctx, dir, "Workstream", CreateOptions{Labels: []string{"kind:workstream"}, Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	child, e := c.Create(ctx, dir, "Task", CreateOptions{Parent: root, Labels: []string{"kind:step"}, Description: "body", AcceptanceCriteria: "proof", Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Show(ctx, dir, child)
	if e != nil || b.ParentID != root || b.Description != "body" || b.AcceptanceCriteria != "proof" {
		t.Fatalf("show: %#v %v", b, e)
	}
	if e = c.SetState(ctx, dir, child, "phase", "review", ""); e != nil {
		t.Fatal(e)
	}
	if e = c.AddComment(ctx, dir, child, "---\nEvidence"); e != nil {
		t.Fatal(e)
	}
	details, e := c.Details(ctx, dir, []string{root, child})
	if e != nil || len(details[child].Comments) != 1 || details[child].Parent != root {
		t.Fatalf("details: %#v %v", details, e)
	}
	listed, e := c.List(ctx, dir, ListOptions{Labels: []string{"kind:step"}, Status: "all"})
	if e != nil || len(listed) != 1 || listed[0].ID != child {
		t.Fatalf("list: %#v %v", listed, e)
	}
	resp := postgresApplyOps(ctx, dir, [][]string{{"update", child, "--title", "Should roll back"}, {"update", "missing", "--status", "closed"}})
	if resp.Exit == 0 || resp.Applied != 0 {
		t.Fatalf("bad rollback receipt: %#v", resp)
	}
	b, e = c.Show(ctx, dir, child)
	if e != nil || b.Title != "Task" {
		t.Fatalf("partial write: %#v %v", b, e)
	}
	if e = c.SetParent(ctx, dir, root, child); e == nil {
		t.Fatal("accepted hierarchy cycle")
	}
	if e = c.CloseWithReason(ctx, dir, child, "Verified"); e != nil {
		t.Fatal(e)
	}
	b, e = c.Show(ctx, dir, child)
	if e != nil || b.Status != "closed" || b.CloseReason != "Verified" {
		t.Fatalf("close: %#v %v", b, e)
	}
	var n int
	if e = s.DB().QueryRow(`SELECT COUNT(*) FROM state_events WHERE entity='bead_tasks'`).Scan(&n); e != nil || n < 4 {
		t.Fatalf("missing events: %d %v", n, e)
	}
}

func TestPostgresConcurrentLabelsAreNotLost(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	ctx := context.Background()
	id, e := c.Create(ctx, dir, "Concurrent", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs <- c.SetLabel(ctx, dir, id, fmt.Sprintf("label:%d", i), true) }(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Error(e)
		}
	}
	b, e := c.Show(ctx, dir, id)
	if e != nil || len(b.Labels) != 20 {
		t.Fatalf("labels lost: %#v %v", b, e)
	}
}

func TestPostgresCachesDoNotReadOldExports(t *testing.T) {
	c, dir, _ := postgresBeads(t)
	ctx := context.Background()
	if e := c.Init(ctx, dir); e != nil {
		t.Fatal(e)
	}
	id, e := c.Create(ctx, dir, "Current", CreateOptions{Silent: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte("{\"id\":\"old\",\"title\":\"stale\",\"status\":\"open\"}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	reader := ExportReads(NewCached(c))
	if _, e = reader.List(ctx, dir, ListOptions{}); e != nil {
		t.Fatal(e)
	}
	if e = c.Rename(ctx, dir, id, "Updated"); e != nil {
		t.Fatal(e)
	}
	rows, e := reader.List(ctx, dir, ListOptions{})
	if e != nil || len(rows) != 1 || rows[0].Title != "Updated" {
		t.Fatalf("stale read: %#v %v", rows, e)
	}
}
