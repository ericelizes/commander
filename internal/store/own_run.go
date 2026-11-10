package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/pgbind"
)

// OpenOwnRunReader uses the normal backend selector, but never initializes a
// schema or opens legacy state. Connection defaults enforce read-only SQL even
// outside the claim query. Errors deliberately omit driver/connection details.
func OpenOwnRunReader(ctx context.Context, statePath string) (*Store, error) {
	dsn, err := PostgresDSN(statePath)
	if err != nil || dsn == "" {
		return nil, errors.New("own-run requires configured PostgreSQL; legacy fallback disabled")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("own-run PostgreSQL configuration unavailable")
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	q.Set("statement_timeout", "5000")
	u.RawQuery = q.Encode()
	db, err := sql.Open(pgbind.Driver, u.String())
	if err != nil {
		return nil, errors.New("own-run PostgreSQL connection unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Minute)
	var version int
	if err = db.QueryRowContext(ctx, `SELECT version FROM commander_schema WHERE id=1`).Scan(&version); err != nil || version != PostgresSchemaVersion {
		db.Close()
		return nil, errors.New("own-run requires available supported PostgreSQL schema; no initialization or fallback")
	}
	return &Store{db: db, postgres: true}, nil
}

type OwnRunClaim struct {
	RunID      int64  `json:"run_id"`
	Task       string `json:"task"`
	Actor      string `json:"actor"`
	Session    string `json:"session"`
	Project    string `json:"project"`
	ProjectDir string `json:"project_dir"`
	Worktree   string `json:"worktree"`
}

// OwnRun verifies all supplied launcher identity and location fields against a
// single current-claim snapshot. It returns only this run, never other tasks.
// This is the local launcher/role trust boundary, not same-UID authentication.
func (s *Store) OwnRun(ctx context.Context, want OwnRunClaim) (OwnRunClaim, error) {
	var got OwnRunClaim
	denied := errors.New("own-run identity or active claim mismatch")
	if !s.postgres || want.RunID <= 0 || want.Task == "" || !strings.HasPrefix(want.Actor, "operator-") || want.Session == "" || want.Project == "" || !filepath.IsAbs(want.ProjectDir) || !filepath.IsAbs(want.Worktree) {
		return got, denied
	}
	err := s.db.QueryRowContext(ctx, `SELECT r.id,r.task_bead_id,r.agent_name,r.opencode_session_id,p.name,p.path,r.worktree_path
 FROM agent_runs r JOIN bead_tasks b ON b.active_run_id=r.id AND b.project_id=r.project_id AND b.id=r.task_bead_id
 JOIN projects p ON p.id=r.project_id
 WHERE r.id=? AND r.task_bead_id=? AND r.status='running' AND b.deleted_at IS NULL AND b.status NOT IN ('closed','deferred')`, want.RunID, want.Task).Scan(&got.RunID, &got.Task, &got.Actor, &got.Session, &got.Project, &got.ProjectDir, &got.Worktree)
	if err != nil {
		return OwnRunClaim{}, denied
	}
	// Launchers historically canonicalize dots to dashes; return the actual row
	// actor for provenance rather than relabeling it to the caller's spelling.
	if strings.ReplaceAll(got.Actor, ".", "-") != strings.ReplaceAll(want.Actor, ".", "-") || got.Session != want.Session || got.Project != want.Project || !sameOwnRunPath(got.ProjectDir, want.ProjectDir) || !sameOwnRunPath(got.Worktree, want.Worktree) {
		return OwnRunClaim{}, denied
	}
	return got, nil
}

func sameOwnRunPath(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	x, e := filepath.EvalSymlinks(a)
	if e != nil {
		return false
	}
	y, e := filepath.EvalSymlinks(b)
	return e == nil && x == y
}
