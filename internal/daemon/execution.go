package daemon

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/chat"
	"log"
	"time"

	"github.com/example-user/commander/internal/execution"
)

func (d *Daemon) runExecution(ctx context.Context) {
	capacity := 8
	if d.cfg != nil && d.cfg.Concurrency.ExecutionSlots > 0 {
		capacity = d.cfg.Concurrency.ExecutionSlots
	}
	if capacity > 128 {
		capacity = 128
	}
	slots := make([]string, capacity)
	for i := range slots {
		slots[i] = fmt.Sprintf("vm-%d", i)
		// This VM adapter supports local code worktrees and knowledge tasks. A
		// Mac/browser-specific capability is not advertised without its adapter.
		if e := d.store.ConfigureExecutionSlot(ctx, slots[i], []string{"code", "knowledge"}, true); e != nil {
			log.Printf("execution: configure capacity: %v", e)
			return
		}
	}
	cfg := &configSource{cfg: d.cfg, runs: d.store, beads: d.beads}
	perWorkstream := 8
	if d.cfg != nil && d.cfg.Concurrency.MaxOperatorsPerWorkstream > 0 {
		perWorkstream = d.cfg.Concurrency.MaxOperatorsPerWorkstream
	}
	worker := &execution.Worker{Store: d.store, Spawner: d.spawner, Slots: slots, Capacity: capacity, WorkstreamCapacity: perWorkstream, Changes: d.store.WatchState(ctx), CheckCapacity: cfg.CheckCapacity}
	recoveryDone := make(chan struct{})
	defer func() { <-recoveryDone }()
	go func() {
		defer close(recoveryDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			d.recoverNativeExecutions(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	worker.Run(ctx)
}

// Binding is durable before any host is started. Recover committed native
// starts through the same identity; a surviving host is adopted, never resent
// its first instruction. Bounded retry state is stored on the canonical bead.
func (d *Daemon) recoverNativeExecutions(ctx context.Context) {
	if d.chat == nil {
		return
	}
	active, e := d.store.ActiveOrchestration(ctx)
	if e != nil || !active {
		return
	}
	roles, e := d.store.NativeExecutionRoles(ctx, time.Now().Unix())
	if e != nil {
		log.Printf("execution recovery: %v", e)
		return
	}
	for _, role := range roles {
		if ctx.Err() != nil {
			return
		}
		if d.chat.Running(role) {
			continue
		}
		bound, e := d.store.NativeExecution(ctx, role)
		if e != nil || bound == nil {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, e = d.chat.Start(attempt, role, chat.StartOptions{})
		cancel()
		if e != nil && !bound.CanRestart {
			continue
		} // Holds/waits do not consume startup-failure retries.
		detail := ""
		if e != nil {
			detail = e.Error()
		}
		if recordErr := d.store.RecordNativeStartupAttempt(ctx, role, bound.RunID, bound.SessionID, detail, time.Now().Unix()); recordErr != nil {
			log.Printf("execution recovery record: %v", recordErr)
		}
	}
}
