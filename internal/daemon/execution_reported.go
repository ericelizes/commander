package daemon

import (
	"context"
	"log"
	"time"

	"github.com/example-user/commander/internal/store"
)

// ReportGrace is how long a run's session may stay up after its newest bead
// comment became a Final report before cmdrd ends it.
const ReportGrace = 3 * time.Minute

// executionReported ends the process of a canonical run that filed its Final
// report and wrote nothing since: the report is the run's result, so a session
// left idling would keep the step "live" and block closing. The exit goes
// through the normal native exit receipt, which keeps the report. A run that
// writes anything after its report is left alone until a report is last again.
type executionReported struct {
	store interface {
		ReportedRuns(context.Context) ([]store.ReportedRun, error)
	}
	stop func(ctx context.Context, role string) error
	now  func() time.Time
}

func (c executionReported) run(ctx context.Context) {
	t := time.NewTicker(redirectInterval)
	defer t.Stop()
	for {
		c.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c executionReported) pass(ctx context.Context) {
	all, err := c.store.ReportedRuns(ctx)
	if err != nil {
		log.Printf("execution reported: %v", err)
		return
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	for _, x := range all {
		if x.ReportedAt == 0 || now.Unix()-x.ReportedAt < int64(ReportGrace/time.Second) {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err := c.stop(stopCtx, x.Agent)
		cancel()
		if err != nil {
			log.Printf("execution reported: stop %s on %s: %v", x.Agent, x.BeadID, err)
			continue
		}
		log.Printf("execution reported: ended %s: its Final report on %s is %s old and it was still running", x.Agent, x.BeadID, now.Sub(time.Unix(x.ReportedAt, 0)).Round(time.Second))
	}
}
