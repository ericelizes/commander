// Package pgtest supplies isolated disposable Postgres schemas to interaction
// tests. It never accepts the configured live database.
package pgtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/example-user/commander/internal/pgbind"
	"github.com/example-user/commander/internal/store"
)

func Open(t *testing.T) (*store.Store, string) {
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
	schema := fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	if _, e = db.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, e := store.OpenPostgres(u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, u.String()
}
