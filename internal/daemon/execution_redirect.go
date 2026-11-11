package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

// redirectInterval is how often running executions are checked for a changed bead.
const redirectInterval = 10 * time.Second

// executionRedirector tells a running agent when Commander changes its bead,
// in the agent's own session, once per plan revision.
type executionRedirector struct {
	store interface {
		ChangedExecutionPlans(context.Context) ([]store.PlanChange, error)
		RecordExecutionRedirect(context.Context, store.PlanChange, string) error
	}
	send func(ctx context.Context, role, text string) error
}

func (r executionRedirector) run(ctx context.Context) {
	t := time.NewTicker(redirectInterval)
	defer t.Stop()
	for {
		r.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r executionRedirector) pass(ctx context.Context) {
	changes, err := r.store.ChangedExecutionPlans(ctx)
	if err != nil {
		log.Printf("execution redirect: %v", err)
		return
	}
	for _, c := range changes {
		text := redirectMessage(c)
		if err := r.send(ctx, c.Agent, text); err != nil {
			log.Printf("execution redirect: tell %s about %s: %v", c.Agent, c.BeadID, err)
			continue
		}
		if err := r.store.RecordExecutionRedirect(ctx, c, text); err != nil {
			log.Printf("execution redirect: record %s: %v", c.BeadID, err)
		}
	}
}

func redirectMessage(c store.PlanChange) string {
	read := fmt.Sprintf("cmdr bead --project-dir '%s' show %s", strings.ReplaceAll(c.ProjectPath, "'", `'"'"'`), c.BeadID)
	if c.Status == "closed" || c.Status == "deferred" {
		return fmt.Sprintf("[Commander] Your assigned bead %s is now %s. Stop this work: record a Final report with what you finished, what is incomplete and where you stopped, then stop. Read it with: %s", c.BeadID, c.Status, read)
	}
	return fmt.Sprintf("[Commander] Your assigned bead %s changed after you started (plan revision %d -> %d). Re-read it now: %s. If your current work still fits its brief and acceptance, continue. If it no longer fits, change course to the new brief. If the change is unclear, ask with cmdr question ask using plan revision %d.", c.BeadID, c.ClaimedRevision, c.CurrentRevision, read, c.CurrentRevision)
}
