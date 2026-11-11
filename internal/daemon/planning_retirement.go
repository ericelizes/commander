package daemon

import (
	"context"
	"log"
	"time"
)

// A completed consolidation grants only eligibility. The store rechecks every
// project and all run custody while the local Engine attests physical identity.
func (d *Daemon) retireObsoletePlanningHosts(ctx context.Context) {
	roles, err := d.store.PlanningRetirementCandidates(ctx)
	if err != nil {
		log.Printf("planning retirement inventory: %v", err)
		return
	}
	for i, role := range roles {
		if i >= 8 || ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = d.store.RetirePlanningRuntime(attempt, role, func(stopCtx context.Context) (string, error) {
			return d.chat.RetirePlanningHost(stopCtx, role)
		})
		cancel()
		if err != nil {
			log.Printf("planning retirement %s deferred: %v", role, err)
		}
	}
}
