package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type StateEvent struct {
	Seq        int64           `json:"seq"`
	Entity     string          `json:"entity"`
	Operation  string          `json:"operation"`
	Identity   json.RawMessage `json:"identity"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// PublishStateEvents assigns replay positions only to committed outbox rows.
// Identity sequence allocation inside a source transaction is not commit order:
// a late-committing source row must receive a NEW delivery position.
func (s *Store) PublishStateEvents(ctx context.Context, limit int) (int64, error) {
	if !s.postgres {
		return 0, errors.New("durable state events require Postgres")
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734031821)`); e != nil {
		return 0, e
	}

	var seq int64
	e = tx.QueryRowContext(ctx, `WITH pending AS MATERIALIZED (SELECT seq FROM state_events WHERE NOT delivered ORDER BY seq LIMIT ? FOR UPDATE SKIP LOCKED),published AS (INSERT INTO event_deliveries(event_id) SELECT seq FROM pending ORDER BY seq ON CONFLICT DO NOTHING RETURNING seq),marked AS (UPDATE state_events e SET delivered=TRUE FROM pending p WHERE e.seq=p.seq RETURNING e.seq) SELECT GREATEST(COALESCE((SELECT MAX(seq) FROM published),0),COALESCE((SELECT MAX(seq) FROM event_deliveries),0))`, limit).Scan(&seq)
	if e != nil {
		return 0, e
	}

	return seq, tx.Commit()
}

func (s *Store) StateEventsSince(ctx context.Context, cursor int64, limit int) ([]StateEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if _, e := s.PublishStateEvents(ctx, 1000); e != nil {
		return nil, e
	}
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var floor int64
	if e = tx.QueryRowContext(ctx, `SELECT replay_floor FROM state_identity WHERE id=1`).Scan(&floor); e != nil {
		return nil, e
	}
	if cursor > 0 && cursor < floor {
		return nil, ErrReplayExpired
	}
	rows, e := tx.QueryContext(ctx, `SELECT d.seq,e.entity,e.operation,e.identity,e.payload,e.occurred_at FROM event_deliveries d JOIN state_events e ON e.seq=d.event_id WHERE d.seq>? ORDER BY d.seq LIMIT ?`, cursor, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []StateEvent{}
	for rows.Next() {
		var ev StateEvent
		if e = rows.Scan(&ev.Seq, &ev.Entity, &ev.Operation, &ev.Identity, &ev.Payload, &ev.OccurredAt); e != nil {
			return nil, e
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// WatchState coalesces transaction notifications into refresh requests. The
// durable outbox supplies replay; NOTIFY is only a wakeup and may be lost.
// Reconnecting always wakes readers, including when the database is unavailable.
func (s *Store) WatchState(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	signal := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	go func() {
		defer close(ch)
		delay := time.Second
		for ctx.Err() == nil {
			conn, e := pgx.Connect(ctx, s.postgresDSN)
			if e == nil {
				_, e = conn.Exec(ctx, `LISTEN commander_state`)
			}
			signal()
			if e == nil {
				delay = time.Second
				for ctx.Err() == nil {
					_, e = conn.WaitForNotification(ctx)
					if e != nil {
						break
					}
					signal()
				}
			}
			if conn != nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				conn.Close(closeCtx)
				cancel()
			}
			if ctx.Err() != nil {
				return
			}
			signal()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < 15*time.Second {
				delay *= 2
			}
		}
	}()
	return ch
}
