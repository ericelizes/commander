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

//go:embed supervision_schema.sql
var supervisionSchema string

//go:embed jurisdiction_schema.sql
var jurisdictionSchema string

//go:embed execution_schema.sql
var executionSchema string

//go:embed bead_questions_schema.sql
var beadQuestionsSchema string

//go:embed runtime_integrity_schema.sql
var runtimeIntegritySchema string

const PostgresSchemaVersion = 3

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
	if underTest() {
		if home, e := os.UserHomeDir(); e == nil {
			if raw, e := os.ReadFile(filepath.Join(home, ".commander", "state.postgres")); e == nil && strings.TrimSpace(string(raw)) == dsn {
				return nil, errors.New("tests must not open the live Postgres database")
			}
		}
	}
	db, err := sql.Open(pgbind.Driver, dsn)
	if err != nil {
		return nil, errors.New("store: invalid Postgres connection configuration")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(time.Minute)
	db.SetConnMaxLifetime(10 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("store: Postgres connection unavailable; SQLite fallback is disabled")
	}
	s := &Store{db: db, postgres: true, postgresDSN: dsn}
	// Schema migration has its own bounded budget. Reusing the connection
	// probe deadline made populated installations fail partway through upgrade.
	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer migrationCancel()
	if err := s.migratePostgres(migrationCtx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) IsPostgres() bool { return s.postgres }

func (s *Store) migratePostgres(ctx context.Context) error {
	var ready int
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM commander_schema WHERE id=1`).Scan(&ready); err == nil {
		if ready == PostgresSchemaVersion {
			return nil
		}
		if ready > 0 && ready < PostgresSchemaVersion {
			if home, e := os.UserHomeDir(); e == nil {
				if raw, e := os.ReadFile(filepath.Join(home, ".commander/state.postgres")); e == nil && strings.TrimSpace(string(raw)) == s.postgresDSN {
					if e = checkMigrateAllowed(filepath.Join(home, ".commander/state.db"), ready); e != nil {
						return e
					}
				}
			}
		}
		if ready != 1 && ready != 2 {
			return fmt.Errorf("store: unsupported Postgres schema %d", ready)
		}
	}
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
		_, err = tx.ExecContext(ctx, `INSERT INTO commander_schema(id,version) VALUES(1,1)`)
		version = 1
	} else if err == nil && version != 1 && version != 2 && version != PostgresSchemaVersion {
		return fmt.Errorf("store: unsupported Postgres schema %d", version)
	}
	if err != nil {
		return err
	}
	if version == 1 {
		if _, err = tx.ExecContext(ctx, jurisdictionSchema); err != nil {
			return fmt.Errorf("store: migrate PostgreSQL jurisdictions: %w", err)
		}
		if _, err = tx.ExecContext(ctx, beadQuestionsSchema); err != nil {
			return fmt.Errorf("store: migrate PostgreSQL questions: %w", err)
		}
		if _, err = tx.ExecContext(ctx, executionSchema); err != nil {
			return fmt.Errorf("store: migrate PostgreSQL execution: %w", err)
		}
		if _, err = tx.ExecContext(ctx, supervisionSchema); err != nil {
			return fmt.Errorf("store: migrate PostgreSQL supervision: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE commander_schema SET version=2 WHERE id=1`); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if _, err = tx.ExecContext(ctx, runtimeIntegritySchema); err != nil {
			return fmt.Errorf("store: migrate PostgreSQL runtime integrity: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE commander_schema SET version=3 WHERE id=1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StateEntitiesSince lists the distinct tables with state events after seq,
// and the newest seq seen (seq itself when there are none).
func (s *Store) StateEntitiesSince(ctx context.Context, seq int64) ([]string, int64, error) {
	if !s.postgres {
		return nil, seq, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entity,MAX(seq) FROM state_events WHERE seq>? GROUP BY entity`, seq)
	if err != nil {
		return nil, seq, err
	}
	defer rows.Close()
	var out []string
	newest := seq
	for rows.Next() {
		var entity string
		var max int64
		if err := rows.Scan(&entity, &max); err != nil {
			return nil, seq, err
		}
		out = append(out, entity)
		newest = maxInt64(newest, max)
	}
	return out, newest, rows.Err()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// NotifyStateChanged wakes state watchers without recording an event: the
// board rebuilds (a description is ready), nothing durable changed.
func (s *Store) NotifyStateChanged(ctx context.Context) {
	if s.postgres {
		_, _ = s.db.ExecContext(ctx, `SELECT pg_notify('commander_state','changed')`)
	}
}
