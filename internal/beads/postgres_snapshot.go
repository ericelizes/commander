package beads

import (
	"context"
	"database/sql"

	"github.com/example-user/commander/internal/store"
)

type postgresReaderKey struct{}

func postgresQuery(ctx context.Context, s *store.Store) store.Querier {
	if q, ok := ctx.Value(postgresReaderKey{}).(store.Querier); ok {
		return q
	}
	return s.DB()
}

// ReadPostgresSnapshot gives every bead read in a project build one MVCC
// snapshot. Busy writers therefore do not force repeated rebuilds or blank UI.
func ReadPostgresSnapshot(ctx context.Context, read func(context.Context) error) error {
	if _, ok := ctx.Value(postgresReaderKey{}).(store.Querier); ok {
		return read(ctx)
	}
	s, e := postgresStore()
	if e != nil {
		return e
	}
	tx, e := s.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = read(context.WithValue(ctx, postgresReaderKey{}, store.Querier(tx))); e != nil {
		return e
	}
	return tx.Commit()
}

// PostgresProjectRevision is read within the same MVCC transaction as the graph.
// A revision-validated graph can be reused only after this live database read.
func PostgresProjectRevision(ctx context.Context, dir string) (int64, error) {
	s, e := postgresStore()
	if e != nil {
		return 0, e
	}
	q := postgresQuery(ctx, s)
	_, e = projectID(ctx, q, dir)
	if e != nil {
		return 0, e
	}
	var rev int64
	e = q.QueryRowContext(ctx, `SELECT revision FROM qualified_graph_revision WHERE id=1`).Scan(&rev)
	return rev, e
}
