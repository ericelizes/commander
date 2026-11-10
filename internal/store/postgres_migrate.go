package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// ImportSQLite copies a consistent read-only source snapshot into an empty
// Postgres store. It never migrates or writes the source. All destination rows
// commit together; identity sequences are advanced after preserving old IDs.
func (s *Store) ImportSQLite(ctx context.Context, path string) (map[string]int64, error) {
	if !s.postgres {
		return nil, errors.New("migration destination must be Postgres")
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	source, e := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if e != nil {
		return nil, e
	}
	defer source.Close()
	src, e := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer src.Rollback()
	var version int
	if e = src.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); e != nil {
		return nil, e
	}
	if version != 22 {
		return nil, fmt.Errorf("migration requires SQLite schema 22; found %d", version)
	}
	tables := []string{"projects", "objectives", "objective_tasks", "cost_events", "agent_runs", "tui_state", "am_agents", "workstream_ranks", "workstream_managers", "inspector_tabs", "inspector_state", "needs_you", "agent_state", "gates", "watch_issues", "manager_checkins", "chat_costs", "usage_turns", "usage_scan", "pitches", "needs_resolutions", "board_since", "manager_unregistrations", "bead_ranks"}
	counts := map[string]int64{}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031822)`); e != nil {
		return nil, e
	}
	for _, table := range tables {
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); e != nil {
			return nil, e
		}
		if n != 0 {
			return nil, fmt.Errorf("migration destination table %s is not empty", table)
		}
	}
	for _, table := range tables {
		selection := "*"
		if table == "needs_resolutions" || table == "gates" {
			selection = "rowid,*"
		}
		rows, e := src.QueryContext(ctx, `SELECT `+selection+` FROM `+table)
		if e != nil {
			return nil, e
		}
		cols, e := rows.Columns()
		if e != nil {
			rows.Close()
			return nil, e
		}
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		insert := `INSERT INTO ` + table + ` (` + strings.Join(quoted, ",") + `) VALUES (` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
		for rows.Next() {
			values := make([]any, len(cols))
			targets := make([]any, len(cols))
			for i := range values {
				targets[i] = &values[i]
			}
			if e = rows.Scan(targets...); e != nil {
				rows.Close()
				return nil, e
			}
			if _, e = tx.ExecContext(ctx, insert, values...); e != nil {
				rows.Close()
				return nil, fmt.Errorf("copy %s: %w", table, e)
			}
			counts[table]++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, col := range []string{"id", "rowid"} {
			if !slices.Contains(cols, col) {
				continue
			}
			var seq sql.NullString
			if e = tx.QueryRowContext(ctx, `SELECT pg_get_serial_sequence(?,?)`, table, col).Scan(&seq); e != nil {
				return nil, e
			}
			if seq.Valid {
				if _, e = tx.ExecContext(ctx, `SELECT setval(?::regclass, GREATEST(COALESCE((SELECT MAX("`+col+`") FROM `+table+`),0),1), EXISTS(SELECT 1 FROM `+table+`))`, seq.String); e != nil {
					return nil, e
				}
			}
		}
		var destinationCount int64
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&destinationCount); e != nil {
			return nil, e
		}
		if destinationCount != counts[table] {
			return nil, fmt.Errorf("migration count mismatch for %s", table)
		}
		if e = verifyMigrationTable(ctx, src, tx, table, strings.Join(quoted, ",")); e != nil {
			return nil, e
		}
	}
	if e = src.Commit(); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return counts, nil
}
