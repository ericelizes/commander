package daemon

import (
	"context"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// Presence reconciliation records process evidence in the canonical database.
// This checks processes; it is not a UI poll or a model request. Only changed
// observations or a renewed 30-second heartbeat generate database writes.
func (d *Daemon) reconcilePostgresPresence(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)

		roles, at, ok := d.live.Live()
		var observations map[int64]bool
		if ok {
			observations = map[int64]bool{}
			set := map[string]bool{}
			for _, role := range roles {
				set[beads.OwnerLabelName(role)] = true
			}
			if runs, e := d.store.ListRunningRuns(checkCtx); e == nil {
				for _, run := range runs {
					observations[run.ID] = set[beads.OwnerLabelName(run.AgentName)]
				}
			} else {
				observations = nil
			}
		}
		native := []string{}
		if d.chat != nil {
			for role := range d.chat.Live() {
				native = append(native, role)
			}
		}
		_ = d.store.RecordRuntimePresence(checkCtx, observations, at, native)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
