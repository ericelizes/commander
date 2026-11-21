package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/store"
)

// ReportGrace is how long a run's chat must stay idle after its newest bead
// comment became a Final report before cmdrd ends it.
const ReportGrace = 3 * time.Minute

// executionReported ends the process of a canonical run that filed its Final
// report and then went quiet: the report is the run's result, so a session left
// idling would keep the step "live" and block closing. It never touches real
// work: a run is ended only when its chat is idle (no turn in flight, no tool
// call, no pending request) and nothing has moved in it (no event, so no
// output, tool call or commit) for the grace. Any activity, or a newer comment,
// restarts the timer; a chat that cannot be read is left alone. The exit goes
// through the normal native exit receipt, which keeps the report.
type executionReported struct {
	store interface {
		ReportedRuns(context.Context) ([]store.ReportedRun, error)
	}
	status func(role string) (chat.Status, error)
	stop   func(ctx context.Context, role string) error
	now    func() time.Time

	seen map[int64]reportSeen // by run id; pass runs on one goroutine
}

type reportSeen struct {
	reportedAt int64
	seq        uint64
	quietSince time.Time
}

func (c *executionReported) run(ctx context.Context) {
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

func (c *executionReported) pass(ctx context.Context) {
	all, err := c.store.ReportedRuns(ctx)
	if err != nil {
		log.Printf("execution reported: %v", err)
		return
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.seen == nil {
		c.seen = map[int64]reportSeen{}
	}
	live := map[int64]bool{}
	for _, x := range all {
		live[x.RunID] = true
		if x.ReportedAt == 0 {
			continue
		}
		st, err := c.status(x.Agent)
		if err != nil || !st.Running {
			delete(c.seen, x.RunID)
			continue
		}
		prev, ok := c.seen[x.RunID]
		busy := st.State != store.AgentIdle || len(st.Pending) > 0
		if !ok || prev.reportedAt != x.ReportedAt || prev.seq != st.LastSeq || busy {
			c.seen[x.RunID] = reportSeen{reportedAt: x.ReportedAt, seq: st.LastSeq, quietSince: now}
			continue
		}
		if now.Sub(prev.quietSince) < ReportGrace {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err = c.stop(stopCtx, x.Agent)
		cancel()
		if err != nil && !errors.Is(err, chat.ErrNotRunning) {
			log.Printf("execution reported: stop %s on %s: %v", x.Agent, x.BeadID, err)
			continue // quietSince is kept: the next pass tries again
		}
		delete(c.seen, x.RunID)
		log.Printf("execution reported: ended %s: idle for %s after its Final report on %s", x.Agent, now.Sub(prev.quietSince).Round(time.Second), x.BeadID)
	}
	for id := range c.seen {
		if !live[id] {
			delete(c.seen, id)
		}
	}
}
