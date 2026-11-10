package store

import (
	"context"
	"fmt"
	"sort"
)

// WorkstreamKey identifies a workstream across the fleet.
type WorkstreamKey struct {
	ProjectID int64
	BeadID    string
}

// ListWorkstreamRanks returns every stored rank (1 = top priority).
func (s *Store) ListWorkstreamRanks(ctx context.Context) (map[WorkstreamKey]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, bead_id, rank FROM workstream_ranks`)
	if err != nil {
		return nil, fmt.Errorf("store: list workstream ranks: %w", err)
	}
	defer rows.Close()
	out := make(map[WorkstreamKey]int)
	for rows.Next() {
		var k WorkstreamKey
		var rank int
		if err := rows.Scan(&k.ProjectID, &k.BeadID, &rank); err != nil {
			return nil, err
		}
		out[k] = rank
	}
	return out, rows.Err()
}

// SetWorkstreamOrder replaces all ranks so that order[0] is rank 1, order[1]
// rank 2, and so on. Anything not listed loses its rank and sorts last.
func (s *Store) SetWorkstreamOrder(ctx context.Context, order []WorkstreamKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin workstream order: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workstream_ranks`); err != nil {
		return fmt.Errorf("store: clear workstream ranks: %w", err)
	}
	for i, k := range order {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workstream_ranks(project_id, bead_id, rank) VALUES (?, ?, ?)`,
			k.ProjectID, k.BeadID, i+1); err != nil {
			return fmt.Errorf("store: rank %s: %w", k.BeadID, err)
		}
	}
	return tx.Commit()
}

// MoveWorkstream puts key at 1-based position `to` among present (the open
// workstreams, in their default order: it supplies membership and the order of
// unranked ones) and writes the result. It re-reads the stored ranks inside a
// write transaction, so a caller holding an old view cannot undo a newer move
// and two concurrent moves compose. It returns the new order.
func (s *Store) MoveWorkstream(ctx context.Context, present []WorkstreamKey, key WorkstreamKey, to int) ([]WorkstreamKey, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: move workstream: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, fmt.Errorf("store: begin move workstream: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	rows, err := conn.QueryContext(ctx, `SELECT project_id, bead_id, rank FROM workstream_ranks`)
	if err != nil {
		return nil, fmt.Errorf("store: read workstream ranks: %w", err)
	}
	ranks := make(map[WorkstreamKey]int)
	for rows.Next() {
		var k WorkstreamKey
		var rank int
		if err := rows.Scan(&k.ProjectID, &k.BeadID, &rank); err != nil {
			rows.Close()
			return nil, err
		}
		ranks[k] = rank
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	order, err := movedOrder(present, ranks, key, to)
	if err != nil {
		return nil, err
	}

	if _, err := conn.ExecContext(ctx, `DELETE FROM workstream_ranks`); err != nil {
		return nil, fmt.Errorf("store: clear workstream ranks: %w", err)
	}
	for i, k := range order {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO workstream_ranks(project_id, bead_id, rank) VALUES (?, ?, ?)`,
			k.ProjectID, k.BeadID, i+1); err != nil {
			return nil, fmt.Errorf("store: rank %s: %w", k.BeadID, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("store: commit move workstream: %w", err)
	}
	committed = true
	return order, nil
}

// movedOrder is present in its ranked order (unranked last, as listed) with key at
// 1-based position to.
func movedOrder(present []WorkstreamKey, ranks map[WorkstreamKey]int, key WorkstreamKey, to int) ([]WorkstreamKey, error) {
	order := append([]WorkstreamKey(nil), present...)
	sort.SliceStable(order, func(i, j int) bool {
		ri, iRanked := ranks[order[i]]
		rj, jRanked := ranks[order[j]]
		if iRanked != jRanked {
			return iRanked
		}
		return iRanked && ri < rj
	})
	from := -1
	for i, k := range order {
		if k == key {
			from = i
		}
	}
	if from < 0 {
		return nil, fmt.Errorf("store: workstream %s is not open", key.BeadID)
	}
	if to < 1 {
		to = 1
	}
	if to > len(order) {
		to = len(order)
	}
	order = append(order[:from], order[from+1:]...)
	return append(order[:to-1], append([]WorkstreamKey{key}, order[to-1:]...)...), nil
}

// ListBeadRanks returns every bead's stored rank among its siblings (1 = first).
func (s *Store) ListBeadRanks(ctx context.Context) (map[WorkstreamKey]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, bead_id, rank FROM bead_ranks`)
	if err != nil {
		return nil, fmt.Errorf("store: list bead ranks: %w", err)
	}
	defer rows.Close()
	out := make(map[WorkstreamKey]int)
	for rows.Next() {
		var k WorkstreamKey
		var rank int
		if err := rows.Scan(&k.ProjectID, &k.BeadID, &rank); err != nil {
			return nil, err
		}
		out[k] = rank
	}
	return out, rows.Err()
}

// MoveBead puts key at 1-based position to among its open siblings (present, in
// their default order) and writes their ranks. Other families' ranks stay.
func (s *Store) MoveBead(ctx context.Context, present []WorkstreamKey, key WorkstreamKey, to int) ([]WorkstreamKey, error) {
	ranks, err := s.ListBeadRanks(ctx)
	if err != nil {
		return nil, err
	}
	order, err := movedOrder(present, ranks, key, to)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin move bead: %w", err)
	}
	defer tx.Rollback()
	for i, k := range order {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bead_ranks(project_id, bead_id, rank) VALUES (?, ?, ?) ON CONFLICT(project_id,bead_id) DO UPDATE SET rank=excluded.rank`,
			k.ProjectID, k.BeadID, i+1); err != nil {
			return nil, fmt.Errorf("store: rank %s: %w", k.BeadID, err)
		}
	}
	return order, tx.Commit()
}
