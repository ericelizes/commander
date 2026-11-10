package store

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
)

func postgresTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("COMMANDER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set COMMANDER_POSTGRES_TEST_DSN for disposable Postgres integration tests")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Path != "/commander_trial" {
		t.Fatal("Postgres integration tests require the commander_trial database")
	}
	db, e := sql.Open(pgbind.Driver, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, e = db.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, e := OpenPostgres(u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresStoreLifecycleAndAtomicEvents(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	id, e := s.InsertProject(ctx, Project{Name: "trial", Type: "code", Path: t.TempDir(), Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.GetProject(ctx, id)
	if e != nil || p == nil || p.Name != "trial" {
		t.Fatalf("project lookup: %#v %v", p, e)
	}
	run, e := s.InsertAgentRun(ctx, AgentRun{ProjectID: id, TaskBeadID: "trial-1", AgentName: "operator-trial", SpawnTime: time.Now().Unix()})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetAgentState(ctx, "operator-trial", AgentWorking, ""); e != nil {
		t.Fatal(e)
	}
	if r, e := s.GetAgentRun(ctx, run); e != nil || r == nil || r.Status != "running" {
		t.Fatalf("run lookup: %#v %v", r, e)
	}
	var before, after int
	if e = s.DB().QueryRow(`SELECT COUNT(*) FROM state_events`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	e = s.WithTx(ctx, func(q Querier) error {
		_, e := q.ExecContext(ctx, `UPDATE agent_state SET state='idle' WHERE role=?`, "operator-trial")
		if e != nil {
			return e
		}
		return fmt.Errorf("rollback requested")
	})
	if e == nil {
		t.Fatal("expected rollback")
	}
	if e = s.DB().QueryRow(`SELECT COUNT(*) FROM state_events`).Scan(&after); e != nil || after != before {
		t.Fatalf("rolled-back event leaked: %d -> %d: %v", before, after, e)
	}
	if e = s.SetUsageScanState(ctx, "trial.log", 12, "{}"); e != nil {
		t.Fatal(e)
	}
	if offset, _, e := s.UsageScanState(ctx, "trial.log"); e != nil || offset != 12 {
		t.Fatalf("usage cursor: %d %v", offset, e)
	}
}

func TestPostgresConcurrentRuntimeWriters(t *testing.T) {
	s := postgresTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if e := s.SetAgentState(context.Background(), fmt.Sprintf("operator-%d", i), AgentWorking, fmt.Sprint(j)); e != nil {
					errs <- e
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	states, e := s.ListAgentStates(context.Background())
	if e != nil || len(states) != 32 {
		t.Fatalf("states=%d err=%v", len(states), e)
	}
}

func TestPostgresSelectionNeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	marker := filepath.Join(dir, "state.postgres")
	if e := os.WriteFile(marker, []byte("invalid"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(path); e == nil {
		t.Fatal("invalid configuration fell back")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("created SQLite file: %v", e)
	}
	if e := os.WriteFile(marker, []byte("postgres://127.0.0.1:1/no_database?connect_timeout=1"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(path); e == nil {
		t.Fatal("unreachable Postgres fell back")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("created SQLite file: %v", e)
	}
}
