package beads

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/store"
)

type qualifiedView struct {
	project int64
	nodes   map[qualifiedNode]Bead
	ids     map[string]qualifiedNode
	edges   []qualifiedEdge
}
type qualifiedViewKey struct{}
type qualifiedViews map[int64]*qualifiedView

// The recursive query retains projects at every step. LIMIT is an explicit
// refusal bound, never a partial success. UNION terminates pre-existing cycles.
const qualifiedEdgesSQL = `SELECT project_id,issue_id,target_project_id,depends_on_id,type FROM bead_dependencies
 UNION ALL SELECT b.project_id,b.id,b.project_id,b.parent_id,'parent-child' FROM bead_tasks b WHERE b.parent_id IS NOT NULL AND b.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM bead_dependencies d WHERE d.project_id=b.project_id AND d.issue_id=b.id AND d.target_project_id=b.project_id AND d.depends_on_id=b.parent_id AND d.type='parent-child')`

const qualifiedComponentSQL = `WITH RECURSIVE component(project_id,id) AS (
 SELECT project_id,id FROM bead_tasks WHERE project_id=? AND deleted_at IS NULL
 UNION
 SELECT CASE WHEN d.project_id=c.project_id AND d.issue_id=c.id THEN d.target_project_id ELSE d.project_id END,
 CASE WHEN d.project_id=c.project_id AND d.issue_id=c.id THEN d.depends_on_id ELSE d.issue_id END
 FROM component c JOIN (` + qualifiedEdgesSQL + `) d ON (d.project_id=c.project_id AND d.issue_id=c.id) OR (d.target_project_id=c.project_id AND d.depends_on_id=c.id)
) SELECT project_id,id FROM component LIMIT ?`

func loadQualifiedView(ctx context.Context, q store.Querier, pid int64, force bool) (*qualifiedView, error) {
	return loadQualifiedSeeded(ctx, q, pid, force, nil)
}

func loadQualifiedSeeded(ctx context.Context, q store.Querier, pid int64, force bool, seeds []qualifiedNode) (*qualifiedView, error) {
	// Ordinary local projects retain their existing unbounded indexed reads.
	var foreign bool
	if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id <> target_project_id AND (project_id=? OR target_project_id=?))`, pid, pid).Scan(&foreign); e != nil {
		return nil, e
	}
	if !foreign && !force {
		return nil, nil
	}
	query := qualifiedComponentSQL
	params := []any{pid}
	if len(seeds) > 0 {
		values := strings.TrimSuffix(strings.Repeat("(?::bigint,?::text),", len(seeds)), ",")
		query = strings.Replace(query, "deleted_at IS NULL\n UNION", "deleted_at IS NULL\n UNION SELECT * FROM (VALUES "+values+") AS seeds(project_id,id)\n UNION", 1)
		for _, n := range seeds {
			params = append(params, n.Project, n.ID)
		}
	}
	params = append(params, qualifiedGraphLimit+1)
	rows, e := q.QueryContext(ctx, query, params...)
	if e != nil {
		return nil, qualifiedFailure("closure", "project", e)
	}
	var roots []qualifiedNode
	for rows.Next() {
		var n qualifiedNode
		if e = rows.Scan(&n.Project, &n.ID); e != nil {
			rows.Close()
			return nil, e
		}
		roots = append(roots, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(roots) > qualifiedGraphLimit {
		return nil, qualifiedFailure("closure", "project", errors.New("qualified component exceeds bound"))
	}
	v := &qualifiedView{project: pid, nodes: map[qualifiedNode]Bead{}}
	counts := map[string]int{}
	if len(roots) == 0 {
		v.ids = map[string]qualifiedNode{}
		return v, nil
	}
	pairs := strings.TrimSuffix(strings.Repeat("(?,?),", len(roots)), ",")
	args := []any{}
	for _, n := range roots {
		args = append(args, n.Project, n.ID)
	}
	rows, e = q.QueryContext(ctx, `SELECT b.project_id,`+pgBeadColumns+` FROM bead_tasks b WHERE (b.project_id,b.id) IN (`+pairs+`) AND b.deleted_at IS NULL`, args...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var pid int64
		b, e := scanPGBead(projectBeadRow{row: rows, project: &pid})
		if e != nil {
			rows.Close()
			return nil, e
		}
		b.Parent = b.ParentID
		v.nodes[qualifiedNode{pid, b.ID}] = b
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = q.QueryContext(ctx, `SELECT project_id,issue_id,target_project_id,depends_on_id,type FROM (`+qualifiedEdgesSQL+`) d WHERE (project_id,issue_id) IN (`+pairs+`) LIMIT ?`, append(append([]any{}, args...), qualifiedGraphLimit*4+1)...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var edge qualifiedEdge
		e = rows.Scan(&edge.Source.Project, &edge.Source.ID, &edge.Target.Project, &edge.Target.ID, &edge.Kind)
		if e != nil {
			rows.Close()
			return nil, e
		}
		v.edges = append(v.edges, edge)
		if len(v.edges) > qualifiedGraphLimit*4 {
			rows.Close()
			return nil, qualifiedFailure("closure", "project", errors.New("qualified edge count exceeds bound"))
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	ids := []any{}
	for _, n := range roots {
		ids = append(ids, n.ID)
	}
	rows, e = q.QueryContext(ctx, `SELECT b.id,COUNT(*) FROM bead_tasks b JOIN projects p ON p.id=b.project_id WHERE b.deleted_at IS NULL AND b.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) GROUP BY b.id`, ids...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		var count int
		if e = rows.Scan(&id, &count); e != nil {
			rows.Close()
			return nil, e
		}
		counts[id] = count
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	v.ids, e = qualifiedClosure(v.nodes, v.edges, counts, roots, qualifiedGraphLimit)
	if e != nil {
		return nil, e
	}
	return v, nil
}

func withQualifiedView(ctx context.Context, dir string, read func(context.Context, *qualifiedView) error) (err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrNotFound) && !IsCrossProjectError(err) {
			err = qualifiedFailure("read", dir, err)
		}
	}()
	if _, ok := ctx.Value(postgresReaderKey{}).(store.Querier); !ok {
		return ReadPostgresSnapshot(ctx, func(ctx context.Context) error { return withQualifiedView(ctx, dir, read) })
	}
	s, e := postgresStore()
	if e != nil {
		return e
	}
	q := postgresQuery(ctx, s)
	pid, e := projectID(ctx, q, dir)
	if e != nil {
		return e
	}
	views, ok := ctx.Value(qualifiedViewKey{}).(qualifiedViews)
	if !ok {
		views = qualifiedViews{}
		ctx = context.WithValue(ctx, qualifiedViewKey{}, views)
	}
	v, ok := views[pid]
	if !ok {
		v, e = loadQualifiedView(ctx, q, pid, false)
		if e != nil {
			return e
		}
		views[pid] = v
	}
	return read(ctx, v)
}

func (c *postgresClient) ReadCustodySnapshot(ctx context.Context, dir string, read func(context.Context) error) error {
	return withQualifiedView(ctx, dir, func(ctx context.Context, _ *qualifiedView) error { return read(ctx) })
}
func (c *postgresClient) RequireLocalCustody(ctx context.Context, dir string, ids []string) error {
	return withQualifiedView(ctx, dir, func(_ context.Context, v *qualifiedView) error {
		if v == nil {
			return nil
		}
		for _, id := range ids {
			n, ok := v.ids[id]
			if !ok || n.Project != v.project {
				return qualifiedFailure("receipt", id, errors.New("receipt requires proven local source; foreign writes are not authorized"))
			}
		}
		return nil
	})
}
func (v *qualifiedView) bead(id string) (*Bead, error) {
	n, ok := v.ids[id]
	if !ok {
		return nil, ErrNotFound
	}
	b := v.nodes[n]
	return &b, nil
}
func (v *qualifiedView) deps(id string, reverse bool) ([]Dep, error) {
	n, ok := v.ids[id]
	if !ok {
		return nil, ErrNotFound
	}
	out := []Dep{}
	for _, e := range v.edges {
		if (!reverse && e.Source == n) || (reverse && e.Target == n) {
			out = append(out, Dep{IssueID: e.Source.ID, DependsOnID: e.Target.ID, Type: e.Kind})
		}
	}
	return out, nil
}
func (v *qualifiedView) list(o ListOptions) []Bead {
	out := []Bead{}
	for id, n := range v.ids {
		if len(o.IDs) > 0 {
			if !slices.Contains(o.IDs, id) {
				continue
			}
		} else if n.Project != v.project {
			continue
		}
		b := v.nodes[n]
		if o.Status != "" && o.Status != "all" && b.Status != o.Status {
			continue
		}
		if o.Type != "" && b.Type != o.Type {
			continue
		}
		matches := true
		for _, l := range o.Labels {
			matches = matches && slices.Contains(b.Labels, l)
		}
		if len(o.LabelAny) > 0 {
			any := false
			for _, l := range o.LabelAny {
				any = any || slices.Contains(b.Labels, l)
			}
			matches = matches && any
		}
		if matches {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Bead) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		if a.CreatedAt != b.CreatedAt {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if o.Limit > 0 && len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out
}
func (v *qualifiedView) details(ctx context.Context, q store.Querier, ids []string) (map[string]Detail, error) {
	out := map[string]Detail{}
	for _, id := range ids {
		n, ok := v.ids[id]
		if !ok {
			continue
		}
		b := v.nodes[n]
		deps, e := v.deps(id, false)
		if e != nil {
			return nil, e
		}
		d := Detail{Parent: b.ParentID, Deps: deps, Comments: []Comment{}}
		rows, e := q.QueryContext(ctx, `SELECT id,author,text,created_at FROM bead_comments WHERE project_id=? AND bead_id=? ORDER BY created_at,id`, n.Project, id)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var c Comment
			if e = rows.Scan(&c.ID, &c.Author, &c.Text, &c.CreatedAt); e != nil {
				rows.Close()
				return nil, e
			}
			d.Comments = append(d.Comments, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		out[id] = d
	}
	return out, nil
}

// Assert the public client remains ID-only; qualified provenance never escapes.
var _ CustodySnapshotReader = (*postgresClient)(nil)

func (c *postgresClient) List(ctx context.Context, dir string, o ListOptions) (out []Bead, err error) {
	err = withQualifiedView(ctx, dir, func(ctx context.Context, v *qualifiedView) error {
		if v != nil {
			out = v.list(o)
			return nil
		}
		var e error
		out, e = c.listLocal(ctx, dir, o)
		return e
	})
	return
}
func (c *postgresClient) Show(ctx context.Context, dir, id string) (out *Bead, err error) {
	err = withQualifiedView(ctx, dir, func(ctx context.Context, v *qualifiedView) error {
		var e error
		if v != nil {
			out, e = v.bead(id)
		} else {
			out, e = c.showLocal(ctx, dir, id)
		}
		return e
	})
	return
}
func (c *postgresClient) Details(ctx context.Context, dir string, ids []string) (out map[string]Detail, err error) {
	err = withQualifiedView(ctx, dir, func(ctx context.Context, v *qualifiedView) error {
		var e error
		if v != nil {
			s, e := postgresStore()
			if e != nil {
				return e
			}
			out, e = v.details(ctx, postgresQuery(ctx, s), ids)
			return e
		}
		out, e = c.detailsLocal(ctx, dir, ids)
		return e
	})
	return
}
func qualifiedDeps(ctx context.Context, dir, id string, reverse bool) (out []Dep, err error) {
	err = withQualifiedView(ctx, dir, func(ctx context.Context, v *qualifiedView) error {
		var e error
		if v != nil {
			out, e = v.deps(id, reverse)
		} else {
			out, e = pgDeps(ctx, dir, id, reverse)
		}
		return e
	})
	return
}

func (c *CachedClient) ReadCustodySnapshot(ctx context.Context, dir string, read func(context.Context) error) error {
	return ReadCustodySnapshot(ctx, c.Client, dir, read)
}
func (c *CachedClient) RequireLocalCustody(ctx context.Context, dir string, ids []string) error {
	return RequireLocalCustody(ctx, c.Client, dir, ids)
}

func (c *StateWrite) ReadCustodySnapshot(ctx context.Context, dir string, read func(context.Context) error) error {
	return ReadCustodySnapshot(ctx, c.Client, dir, read)
}
func (c *StateWrite) RequireLocalCustody(ctx context.Context, dir string, ids []string) error {
	return RequireLocalCustody(ctx, c.Client, dir, ids)
}
func (c *exportClient) ReadCustodySnapshot(ctx context.Context, dir string, read func(context.Context) error) error {
	return ReadCustodySnapshot(ctx, c.Client, dir, read)
}
func (c *exportClient) RequireLocalCustody(ctx context.Context, dir string, ids []string) error {
	return RequireLocalCustody(ctx, c.Client, dir, ids)
}

// Keep qualified snapshots on the same complete row decoder as local reads.
type projectBeadRow struct {
	row     interface{ Scan(...any) error }
	project *int64
}

func (r projectBeadRow) Scan(dest ...any) error {
	return r.row.Scan(append([]any{r.project}, dest...)...)
}
