package store

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// expectedTables lists every table the migrations must create (sorted).
// Update this when a new migration adds tables.
var expectedTables = []string{
	"agent_runs",
	"agent_state",
	"am_agents",
	"bead_ranks",
	"board_since",
	"chat_costs",
	"cost_events",
	"gates",
	"inspector_state",
	"inspector_tabs",
	"manager_checkins",
	"manager_unregistrations",
	"needs_resolutions",
	"needs_you",
	"objective_tasks",
	"objectives",
	"pitches",
	"place_comments",
	"place_runs",
	"places",
	"projects",
	"tui_state",
	"usage_scan",
	"usage_turns",
	"watch_issues",
	"workstream_managers",
	"workstream_ranks",
}

// openMem opens an in-memory store and registers Close on cleanup.
func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// listTables returns the user-defined tables in the DB, sorted.
func listTables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
	)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(got)
	return got
}

func TestOpen_RunsMigrations_CreatesAllTables(t *testing.T) {
	s := openMem(t)
	got := listTables(t, s)
	if len(got) != len(expectedTables) {
		t.Fatalf("table count: got %d (%v), want %d (%v)", len(got), got, len(expectedTables), expectedTables)
	}
	for i, want := range expectedTables {
		if got[i] != want {
			t.Errorf("table[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

func TestOpen_SetsUserVersionToLatest(t *testing.T) {
	s := openMem(t)
	var v int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if v != 23 {
		t.Errorf("user_version: got %d, want 23", v)
	}
}

func TestOpen_EnablesForeignKeys(t *testing.T) {
	s := openMem(t)
	var fk int
	if err := s.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys: got %d, want 1", fk)
	}
}

func TestOpen_Idempotent_ReopenDoesNotReapply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Insert a row; if migrations re-ran (DROP/CREATE) this row would vanish.
	ctx := context.Background()
	if _, err := s1.InsertProject(ctx, Project{
		Name: "alpha", Type: "code", Path: "/tmp/alpha", ActivatedAt: 1, Status: "active",
	}); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	p, err := s2.GetProjectByName(ctx, "alpha")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if p == nil {
		t.Fatal("expected project to persist across reopen, got nil")
	}
}

func TestOpen_RefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Bump user_version above what this binary supports.
	if _, err := s.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("expected error when reopening DB with newer schema, got nil")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error message: got %q, want substring %q", err.Error(), "newer than this binary supports")
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"0001_initial.sql", 1, false},
		{"migrations/0007_add_foo.sql", 7, false},
		{"42-something.sql", 42, false},
		{"no_prefix.sql", 0, true},
		{"_leading_underscore.sql", 0, true},
	}
	for _, tc := range cases {
		got, err := parseVersion(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseVersion(%q): err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseVersion(%q): got %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestWithTx_CommitsOnSuccess(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	err := s.WithTx(ctx, func(q Querier) error {
		_, err := q.ExecContext(ctx,
			`INSERT INTO projects(name,type,path,activated_at,status) VALUES (?,?,?,?,?)`,
			"tx-commit", "code", "/tmp/x", 1, "active")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	p, err := s.GetProjectByName(ctx, "tx-commit")
	if err != nil || p == nil {
		t.Fatalf("expected committed row, got %v err=%v", p, err)
	}
}

func TestWithTx_RollsBackOnError(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	wantErr := "boom"
	err := s.WithTx(ctx, func(q Querier) error {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO projects(name,type,path,activated_at,status) VALUES (?,?,?,?,?)`,
			"tx-rollback", "code", "/tmp/x", 1, "active"); err != nil {
			return err
		}
		return &stringErr{wantErr}
	})
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("WithTx err: got %v, want substring %q", err, wantErr)
	}
	p, err := s.GetProjectByName(ctx, "tx-rollback")
	if err != nil {
		t.Fatalf("GetProjectByName: %v", err)
	}
	if p != nil {
		t.Errorf("expected rollback to remove row, got %+v", p)
	}
}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }
