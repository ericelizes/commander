package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/pgbind"
)

//go:embed postgres_schema.sql
var postgresSchema string

// PostgresDSN selects the database explicitly beside state.db. A configured but
// invalid/unreachable Postgres never falls back to the old SQLite database.
func PostgresDSN(statePath string) (string, error) {
	if strings.HasPrefix(statePath, "postgres://") || strings.HasPrefix(statePath, "postgresql://") {
		return statePath, nil
	}
	if filepath.Base(statePath) != "state.db" {
		return "", nil
	}
	p := filepath.Join(filepath.Dir(statePath), "state.postgres")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Postgres connection configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("Postgres connection configuration must be a regular private file (0600)")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read Postgres connection configuration: %w", err)
	}
	dsn := strings.TrimSpace(string(b))
	if !(strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")) {
		return "", fmt.Errorf("invalid Postgres connection configuration")
	}
	return dsn, nil
}

// OpenPostgres opens the canonical database, applying its schema transactionally.
// Callers must not log dsn: it may contain credentials.
func OpenPostgres(dsn string) (*Store, error) {
	db, err := sql.Open(pgbind.Driver, dsn)
	if err != nil {
		return nil, errors.New("store: invalid Postgres connection configuration")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(10 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("store: Postgres connection unavailable; SQLite fallback is disabled")
	}
	s := &Store{db: db, postgres: true, postgresDSN: dsn}
	if err := s.migratePostgres(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) IsPostgres() bool { return s.postgres }

func (s *Store) migratePostgres(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031820)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS commander_schema (id BIGINT PRIMARY KEY CHECK(id=1), version BIGINT NOT NULL)`); err != nil {
		return err
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM commander_schema WHERE id=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, postgresSchema); err != nil {
			return fmt.Errorf("store: initialize Postgres schema: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO commander_schema(id,version) VALUES(1,22)`)
	} else if err == nil && version != 22 {
		return fmt.Errorf("store: unsupported Postgres schema %d", version)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
