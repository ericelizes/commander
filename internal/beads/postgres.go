package beads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/example-user/commander/internal/store"
)

// PostgresConfigured is true even for an invalid marker, so bad configuration
// cannot route writes back into a retired backend.
func PostgresConfigured() bool {
	_, err := os.Lstat(filepath.Join(brHome(), "state.postgres"))
	return !errors.Is(err, os.ErrNotExist)
}

var pgStores struct {
	sync.Mutex
	dsn string
	st  *store.Store
}

func postgresStore() (*store.Store, error) {
	dsn, err := store.PostgresDSN(filepath.Join(brHome(), "state.db"))
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, errors.New("Postgres is not configured")
	}
	pgStores.Lock()
	defer pgStores.Unlock()
	if pgStores.st != nil && pgStores.dsn == dsn {
		return pgStores.st, nil
	}
	st, err := store.OpenPostgres(dsn)
	if err != nil {
		return nil, err
	}
	if pgStores.st != nil {
		pgStores.st.Close()
	}
	pgStores.dsn, pgStores.st = dsn, st
	return st, nil
}

type postgresClient struct{ *cmdClient }

func projectID(ctx context.Context, q store.Querier, dir string) (int64, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	if real, e := filepath.EvalSymlinks(abs); e == nil {
		abs = real
	}
	var id int64
	err = q.QueryRowContext(ctx, `SELECT id FROM projects WHERE path=? OR repo_path=? ORDER BY CASE WHEN path=? THEN 0 ELSE 1 END LIMIT 1`, abs, abs, abs).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("project is not registered: %s", dir)
	}
	return id, err
}

const pgBeadColumns = `b.id,b.title,b.description,b.acceptance_criteria,b.status,b.issue_type,b.priority,b.labels,COALESCE(b.parent_id,''),b.assigned_agent,b.created_at,b.updated_at,b.close_reason,
 (SELECT COUNT(*) FROM bead_dependencies d WHERE d.project_id=b.project_id AND d.issue_id=b.id),
 (SELECT COUNT(*) FROM bead_dependencies d WHERE d.project_id=b.project_id AND d.depends_on_id=b.id),
 (SELECT COUNT(*) FROM bead_comments c WHERE c.project_id=b.project_id AND c.bead_id=b.id)`

func scanPGBead(row interface{ Scan(...any) error }) (Bead, error) {
	var b Bead
	var labels []byte
	err := row.Scan(&b.ID, &b.Title, &b.Description, &b.AcceptanceCriteria, &b.Status, &b.Type, &b.Priority, &labels, &b.ParentID, &b.AssignedAgent, &b.CreatedAt, &b.UpdatedAt, &b.CloseReason, &b.DependencyCount, &b.DependentCount, &b.CommentCount)
	if err != nil {
		return b, err
	}
	err = json.Unmarshal(labels, &b.Labels)
	b.Parent = b.ParentID
	return b, err
}

func (*postgresClient) List(ctx context.Context, dir string, o ListOptions) ([]Bead, error) {
	s, err := postgresStore()
	if err != nil {
		return nil, err
	}
	pid, err := projectID(ctx, s.DB(), dir)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + pgBeadColumns + ` FROM bead_tasks b WHERE b.project_id=? AND b.deleted_at IS NULL`
	args := []any{pid}
	if o.Status != "" && o.Status != "all" {
		q += ` AND b.status=?`
		args = append(args, o.Status)
	}
	if o.Type != "" {
		q += ` AND b.issue_type=?`
		args = append(args, o.Type)
	}
	for _, l := range o.Labels {
		q += ` AND jsonb_exists(b.labels,?)`
		args = append(args, l)
	}
	if len(o.LabelAny) > 0 {
		q += ` AND (`
		for i, l := range o.LabelAny {
			if i > 0 {
				q += ` OR `
			}
			q += `jsonb_exists(b.labels,?)`
			args = append(args, l)
		}
		q += `)`
	}
	if len(o.IDs) > 0 {
		q += ` AND b.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(o.IDs)), ",") + `)`
		for _, id := range o.IDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY b.priority,b.created_at,b.id`
	if o.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, o.Limit)
	}
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bead{}
	for rows.Next() {
		b, e := scanPGBead(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (*postgresClient) Show(ctx context.Context, dir, id string) (*Bead, error) {
	s, e := postgresStore()
	if e != nil {
		return nil, e
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		return nil, e
	}
	b, e := scanPGBead(s.DB().QueryRowContext(ctx, `SELECT `+pgBeadColumns+` FROM bead_tasks b WHERE b.project_id=? AND b.id=? AND b.deleted_at IS NULL`, pid, id))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return &b, nil
}
func (c *postgresClient) freshShow(ctx context.Context, dir, id string) (*Bead, error) {
	return c.Show(ctx, dir, id)
}

func (*postgresClient) Deps(ctx context.Context, dir, id string) ([]Dep, error) {
	return pgDeps(ctx, dir, id, false)
}
func (*postgresClient) Dependents(ctx context.Context, dir, id string) ([]Dep, error) {
	return pgDeps(ctx, dir, id, true)
}
func pgDeps(ctx context.Context, dir, id string, reverse bool) ([]Dep, error) {
	s, e := postgresStore()
	if e != nil {
		return nil, e
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		return nil, e
	}
	col := "issue_id"
	if reverse {
		col = "depends_on_id"
	}
	rows, e := s.DB().QueryContext(ctx, `SELECT issue_id,depends_on_id,type FROM bead_dependencies WHERE project_id=? AND `+col+`=? ORDER BY issue_id,depends_on_id,type`, pid, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Dep{}
	for rows.Next() {
		var d Dep
		if e := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (*postgresClient) Comments(ctx context.Context, dir, id string) ([]Comment, error) {
	s, e := postgresStore()
	if e != nil {
		return nil, e
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		return nil, e
	}
	rows, e := s.DB().QueryContext(ctx, `SELECT id,author,text,created_at FROM bead_comments WHERE project_id=? AND bead_id=? ORDER BY created_at,id`, pid, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if e := rows.Scan(&c.ID, &c.Author, &c.Text, &c.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Details hydrates an arbitrary selection in three indexed queries, independent
// of the number of beads. It never reads an export or invokes a process.
func (*postgresClient) Details(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	out := map[string]Detail{}
	if len(ids) == 0 {
		return out, nil
	}
	s, e := postgresStore()
	if e != nil {
		return nil, e
	}
	pid, e := projectID(ctx, s.DB(), dir)
	if e != nil {
		return nil, e
	}
	args := []any{pid}
	for _, id := range ids {
		args = append(args, id)
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	tx, e := s.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,COALESCE(parent_id,'') FROM bead_tasks WHERE project_id=? AND id IN (`+in+`) AND deleted_at IS NULL`, args...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id, parent string
		if e = rows.Scan(&id, &parent); e != nil {
			rows.Close()
			return nil, e
		}
		out[id] = Detail{Parent: parent, Deps: []Dep{}, Comments: []Comment{}}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.QueryContext(ctx, `SELECT issue_id,depends_on_id,type FROM bead_dependencies WHERE project_id=? AND issue_id IN (`+in+`) ORDER BY issue_id,depends_on_id,type`, args...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var d Dep
		if e = rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type); e != nil {
			rows.Close()
			return nil, e
		}
		if v, ok := out[d.IssueID]; ok {
			v.Deps = append(v.Deps, d)
			out[d.IssueID] = v
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.QueryContext(ctx, `SELECT bead_id,id,author,text,created_at FROM bead_comments WHERE project_id=? AND bead_id IN (`+in+`) ORDER BY bead_id,created_at,id`, args...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		var c Comment
		if e = rows.Scan(&id, &c.ID, &c.Author, &c.Text, &c.CreatedAt); e != nil {
			rows.Close()
			return nil, e
		}
		if v, ok := out[id]; ok {
			v.Comments = append(v.Comments, c)
			out[id] = v
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (c *postgresClient) freshDetails(ctx context.Context, dir string, ids []string) (map[string]Detail, error) {
	return c.Details(ctx, dir, ids)
}
