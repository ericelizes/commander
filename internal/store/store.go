// Package store is commander's persistent state layer. It owns the SQLite
// database that records projects, objectives, the join table that links
// objectives to bd Tasks across projects, cost events, active operator runs,
// and the single-row TUI state.
//
// modernc.org/sqlite is used (pure Go, no CGo) so cross-compile parity holds.
// Driver name is "sqlite" (not "sqlite3").
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/store/migrations"

	_ "modernc.org/sqlite"
)

// Store wraps a *sql.DB. Entity CRUD methods hang off this type.
type Store struct {
	db          *sql.DB
	postgres    bool
	postgresDSN string
}

// Open opens (or creates) the SQLite database at path, enables foreign keys
// and WAL mode, then applies any embedded migrations whose version is greater
// than the database's current PRAGMA user_version.
//
// path may be ":memory:" for tests, or a filesystem path for production.
// Parent directories are NOT created here; callers (e.g. cmdr) own
// that responsibility.
func Open(path string) (*Store, error) {
	dsn, err := PostgresDSN(path)
	if err != nil {
		return nil, err
	}
	if dsn != "" {
		return OpenPostgres(dsn)
	}
	// modernc.org/sqlite uses driver name "sqlite" (not "sqlite3").
	// Per-connection pragmas go in the DSN so every pooled connection gets them
	// (db.Exec reaches one). Without busy_timeout a write that meets another
	// writer fails at once with SQLITE_BUSY instead of waiting its turn.
	// _txlock=immediate: every transaction here writes, and one begun as a
	// read (SQLite's default) that then writes after another connection wrote
	// fails at once with SQLITE_BUSY, busy_timeout or not (closing a tab did).
	// Begun IMMEDIATE, it takes the write lock up front and waits its turn.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}

	// WAL is persisted per-db. For ":memory:" it is a no-op (memory journal only).
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: enable WAL: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying *sql.DB. Useful for ad-hoc queries and for tests
// that need to inspect schema directly.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Querier is satisfied by both *sql.DB and *sql.Tx. Entity CRUD helpers in
// this package accept a Querier so they compose transparently inside a
// transaction or against the bare DB. Tests can substitute a fake.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// WithTx runs fn inside a transaction, committing on nil error and rolling
// back otherwise. The function receives a Querier so the same entity helpers
// work inside or outside a transaction.
func (s *Store) WithTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// migrate brings the database up to migrations.LatestVersion. Each .sql file
// in migrations/ is applied in lexical order; the file itself is expected to
// set PRAGMA user_version = N at the end. Re-running on an up-to-date DB is
// a no-op. A user_version > LatestVersion is a loud error (no auto-downgrade).
func (s *Store) migrate(path string) error {
	current, err := s.userVersion()
	if err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if current < migrations.LatestVersion {
		if err := checkMigrateAllowed(path, current); err != nil {
			return err
		}
	}
	if current > migrations.LatestVersion {
		return fmt.Errorf(
			"store: database schema version %d is newer than this binary supports (max %d); refusing to auto-downgrade",
			current, migrations.LatestVersion,
		)
	}
	if current == migrations.LatestVersion {
		return nil
	}

	files, err := listMigrationFiles(migrations.FS)
	if err != nil {
		return err
	}
	for _, f := range files {
		v, err := parseVersion(f)
		if err != nil {
			return err
		}
		if v <= current {
			continue
		}
		body, err := migrations.FS.ReadFile(f)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", f, err)
		}
		// Each file is a self-contained DDL script. We execute it inside a
		// transaction; partial failure rolls back the DDL.
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", f, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %s: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %s: %w", f, err)
		}
	}
	// Sanity: confirm we landed at the expected version.
	final, err := s.userVersion()
	if err != nil {
		return fmt.Errorf("store: re-read user_version after migrate: %w", err)
	}
	if final != migrations.LatestVersion {
		return fmt.Errorf(
			"store: migrations finished at user_version=%d, expected %d (migration file did not set PRAGMA user_version?)",
			final, migrations.LatestVersion,
		)
	}
	return nil
}

func (s *Store) userVersion() (int, error) {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// listMigrationFiles returns *.sql files in lexical order.
func listMigrationFiles(efs fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(efs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".sql") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// parseVersion pulls the leading integer out of a filename like
// "0001_initial.sql" → 1.
func parseVersion(filename string) (int, error) {
	base := filename
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	cut := strings.IndexAny(base, "_-.")
	if cut <= 0 {
		return 0, fmt.Errorf("store: migration filename %q missing version prefix", filename)
	}
	n, err := strconv.Atoi(base[:cut])
	if err != nil {
		return 0, fmt.Errorf("store: migration filename %q: %w", filename, err)
	}
	return n, nil
}
