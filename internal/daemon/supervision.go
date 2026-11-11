package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/supervision"
	"github.com/example-user/commander/internal/wake"
)

func (d *Daemon) runSupervision(ctx context.Context) {
	worker := &supervision.Worker{Store: d.store, Mode: d.orchestrationMode, Changes: d.store.WatchState(ctx), Deliver: d.deliverSupervision, Resume: d.resumeSupervisedExecution, Deferred: func(e error) bool { return errors.Is(e, wake.ErrDeferred) || errors.Is(e, chat.ErrBusy) }}
	worker.Run(ctx)
}
func (d *Daemon) deliverSupervision(ctx context.Context, agent, prompt string) error {
	if chat.IsCommanderRole(agent) && d.chat != nil {
		// Scoped Commanders are logical conversations, not workstream Managers.
		// Resolve through their registration and start their own persistent session.
		row, e := d.store.GetCommander(ctx, agent)
		if e != nil {
			return e
		}
		if row == nil {
			return errors.New("planning conversation is not registered")
		}
		if d.chat.Talking(agent) {
			return wake.ErrDeferred
		}
		if !d.chat.Running(agent) {
			if _, e = d.chat.Start(ctx, agent, chat.StartOptions{}); e != nil {
				return e
			}
		}
		e = d.chat.DeliverMail(ctx, agent, prompt)
		if errors.Is(e, chat.ErrBusy) {
			return wake.ErrDeferred
		}
		if e == nil {
			d.chat.MailDelivered(agent)
		}
		return e
	}
	if d.supervisionDeliver == nil {
		return errors.New("agent runtime is unavailable")
	}
	err := d.supervisionDeliver(ctx, agent, prompt)
	// Legacy host executions are one-shot processes. Delivery never claims that
	// their unread mailbox is an acknowledgement; the durable obligation stays
	// pending and is escalated to their workstream owner when necessary.
	return err
}
func (d *Daemon) resumeSupervisedExecution(ctx context.Context, f store.SupervisionFact) error {
	fresh, e := d.store.SupervisionFact(ctx, f.Job)
	if e != nil {
		return e
	}
	if fresh.Epoch != f.Epoch || fresh.Held || fresh.Alive || !fresh.PresenceKnown {
		return wake.ErrDeferred
	}
	run, e := d.store.GetAgentRun(ctx, f.RunID)
	if e != nil {
		return e
	}
	if run == nil || run.Status != "running" || spawn.StopPending(*run) {
		return wake.ErrDeferred
	}
	// Reuse the existing bounded same-brief recovery path. It checks current
	// ownership, original worktree and restart allowances before launching.
	if e = d.store.MarkClosed(ctx, run.ID, "supervision-missing"); e != nil {
		return e
	}
	if e = d.settle.diedBecause(ctx, *run, false, "assigned execution is no longer present"); e != nil {
		return fmt.Errorf("resume assigned execution: %w", e)
	}
	return nil
}

// Supervision delivery is administrative work, never additional authority.
func supervisionPromptInstruction() string {
	return strings.TrimSpace("A supervision request keeps responsibility on an existing bead. Read current state, acknowledge its exact request id, then advance it or record the legitimate blocker. Do not infer approvals or clear holds from a wake-up. A stale request must be reread, never applied.")
}
