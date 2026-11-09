package ceilings

import (
	"context"
	"fmt"
	"time"
)

// tickInterval is how often the wallclock + retry loop scans live runs.
// 10s matches U10-b's spec; not exposed as Limits because raising it would
// directly enlarge the kill-window for runaway operators.
const tickInterval = 10 * time.Second

// tickLoop scans live runs every tickInterval and kills any whose wallclock
// has crossed MaxWallclockMinutes. Cost + tokens are NOT checked here —
// those tick on the per-event path in consumer.go (must enforce before the
// next call lands; a 10s window is too coarse for $-sensitive ceilings).
//
// Stops when ctx is cancelled.
func (w *Watchdog) tickLoop(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tickOnce(ctx)
		}
	}
}

// tickOnce iterates every live run and applies wallclock + retry checks.
// Extracted so tests can drive ticks deterministically without spinning the
// real ticker.
func (w *Watchdog) tickOnce(ctx context.Context) {
	now := nowFunc()
	w.runs.Range(func(_, v any) bool {
		st, ok := v.(*runState)
		if !ok {
			return true
		}
		if w.limits.MaxWallclockMinutes > 0 {
			elapsed := now.Sub(st.spawnTime)
			if elapsed >= time.Duration(w.limits.MaxWallclockMinutes)*time.Minute {
				w.kill(ctx, st, fmt.Sprintf(
					"wallclock ceiling: %d min elapsed >= %d min",
					int(elapsed.Minutes()), w.limits.MaxWallclockMinutes,
				))
				return true
			}
		}
		// Defence in depth: re-check the retry ceiling on the tick path
		// in case applyError missed a kill (e.g. failures bumped via the
		// store from a different process / hot-reload path).
		st.mu.Lock()
		failures := st.consecutiveFailures
		st.mu.Unlock()
		if w.limits.MaxRetries > 0 && failures >= w.limits.MaxRetries {
			w.kill(ctx, st, fmt.Sprintf("retry ceiling: %d consecutive failures", failures))
		}
		return true
	})
}
