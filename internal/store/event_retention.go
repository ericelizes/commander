package store

import (
	"context"
	"errors"
	"time"
)

var ErrReplayExpired = errors.New("state replay cursor expired; reload current state")

func (s *Store) StateGeneration(ctx context.Context) (string, int64, error) {
	var generation string
	var floor int64
	e := s.db.QueryRowContext(ctx, `SELECT generation,replay_floor FROM state_identity WHERE id=1`).Scan(&generation, &floor)
	return generation, floor, e
}

// PruneStateEvents removes delivered replay data only. Beads, decisions, run
// history and chat records are retained. A cursor crossing a removed event
// explicitly requires a current-state reload.
func (s *Store) PruneStateEvents(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	n := 0
	e := s.WithTx(ctx, func(q Querier) error {
		if _, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031821)`); e != nil {
			return e
		}

		return q.QueryRowContext(ctx, `WITH expired AS MATERIALIZED (SELECT d.seq,d.event_id FROM event_deliveries d JOIN state_events e ON e.seq=d.event_id WHERE e.occurred_at<? ORDER BY d.seq LIMIT ?),gone_deliveries AS (DELETE FROM event_deliveries d USING expired x WHERE d.seq=x.seq RETURNING d.event_id),gone_events AS (DELETE FROM state_events e USING gone_deliveries d WHERE e.seq=d.event_id RETURNING e.seq),floor AS (UPDATE state_identity SET replay_floor=GREATEST(replay_floor,COALESCE((SELECT MAX(seq) FROM expired),0)) WHERE id=1 RETURNING replay_floor) SELECT COUNT(*) FROM gone_events`, before, limit).Scan(&n)
	})
	return n, e
}

func (s *Store) MaintainEventHistory(ctx context.Context) {
	publish := time.NewTicker(time.Second)
	defer publish.Stop()
	prune := time.NewTicker(10 * time.Minute)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-publish.C:
			work, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, _ = s.PublishStateEvents(work, 5000)
			cancel()
		case <-prune.C:
			work, cancel := context.WithTimeout(ctx, 5*time.Second)
			for i := 0; i < 50; i++ {
				n, e := s.PruneStateEvents(work, time.Now().Add(-48*time.Hour), 5000)
				if e != nil || n < 5000 {
					break
				}
			}
			cancel()
		}
	}
}
