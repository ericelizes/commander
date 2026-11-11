// Package execution runs ready beads through available capability adapters.
// It neither creates plans nor adds a delegation conversation.
package execution

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

type Spawner interface {
	Spawn(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error)
}

type Worker struct {
	Store              *store.Store
	Spawner            Spawner
	Place              string
	OfferPlace         func(context.Context, int64) (bool, error)
	Slots              []string
	Capacity           int
	WorkstreamCapacity int
	Changes            <-chan struct{}
	CheckCapacity      func(context.Context, store.Project, string) error
	Now                func() time.Time
	Interval           time.Duration
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run gives each slot an independent loop, so a slow preparation or a user
// interrupt in another conversation cannot block unrelated ready work.
func (w *Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	signals := make([]chan struct{}, len(w.Slots))
	var wg sync.WaitGroup
	for i, slot := range w.Slots {
		signals[i] = make(chan struct{}, 1)
		wg.Add(1)
		go func(slot string, signal <-chan struct{}) {
			defer wg.Done()
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				if e := w.PassSlot(ctx, slot); e != nil && ctx.Err() == nil {
					log.Printf("execution: slot %s: %v", slot, e)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				case <-signal:
				}
			}
		}(slot, signals[i])
	}
	changes := w.Changes
	var lastWake time.Time
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case _, ok := <-changes:
			if !ok {
				changes = nil
				continue
			}
			// Changes arrive several times a second. Wake the slots at most once
			// a second, and only when some ready bead is unclaimed; each slot's
			// own ticker still reaps and retries.
			if time.Since(lastWake) < time.Second {
				continue
			}
			if claimable, e := w.Store.HasClaimableExecution(ctx); e == nil && !claimable {
				continue
			}
			lastWake = time.Now()
			for _, signal := range signals {
				select {
				case signal <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (w *Worker) PassSlot(ctx context.Context, slot string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	active, e := w.Store.ActiveOrchestration(ctx)
	if e != nil {
		return e
	}
	if !active {
		return nil
	}
	if e := w.Store.ReapExecutionStartupClaims(ctx, w.now()); e != nil {
		return e
	}
	perWorkstream := w.WorkstreamCapacity
	if perWorkstream == 0 {
		perWorkstream = 8
	}
	claim, e := w.Store.ClaimNextExecution(ctx, slot, w.Capacity, w.now(), perWorkstream)
	if e != nil || claim == nil {
		return e
	}
	launchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-launchCtx.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(launchCtx, 3*time.Second)
				e := w.Store.RenewExecutionClaim(renewCtx, *claim, w.now())
				stop()
				if e != nil {
					return
				}
			}
		}
	}()
	if w.CheckCapacity != nil {
		e = w.CheckCapacity(launchCtx, claim.Project, claim.BeadID)
	}
	if e == nil && w.Spawner == nil && w.Place == "" {
		e = fmt.Errorf("no execution runtime adapter")
	}
	if e == nil {
		// Runtime role names remain an adapter detail. The bead and immutable run
		// session identify execution, and planning authority comes from jurisdiction.
		name := "exec-" + strings.ReplaceAll(slot, ":", "-") + "-" + claim.Token[:12]
		brief := fmt.Sprintf("%s\n\nGoal and brief:\n%s\n\nAcceptance criteria:\n%s\n\nPlanning owner: %s. Read the current bead and its ancestors before work. Ordinary implementation decisions are yours. Record a result with evidence; ask the planning owner only when a decision exceeds your authority. Do not infer release permission, clear holds, or change other workstreams.\n", claim.Title, claim.Description, claim.Acceptance, claim.PlanningCommander)
		var result *spawn.SpawnResult
		request := spawn.SpawnRequest{ExecutionClaimToken: claim.Token, ProjectID: claim.Project.ID, ProjectName: claim.Project.Name, ProjectPath: claim.Project.Path, ProjectType: claim.Project.Type, RepoPath: claim.Project.RepoPath, TaskBeadID: claim.BeadID, Branch: "work/" + name, Name: name, TaskText: brief}
		if w.Place == "" {
			orders, orderErr := w.Store.AgentAccounts(launchCtx)
			e = orderErr
			request.AgentAccounts = orders[store.AgentKindAgent]
			if e == nil {
				e = w.resumeQuestionCheckpoint(launchCtx, *claim, &request)
			}
		}
		if e == nil && w.Place != "" {
			if slot != "place:"+w.Place {
				e = fmt.Errorf("place does not own execution slot")
			} else if claim.ResumeRunID != 0 {
				e = fmt.Errorf("place checkpoint continuation is not supported")
			} else {
				var id int64
				id, e = place.RecordOffer(launchCtx, w.Store, place.OfferRequest{ExecutionClaimToken: claim.Token, ProjectID: claim.Project.ID, TaskID: claim.BeadID, StepID: claim.BeadID, AgentName: "operator-" + name, Place: w.Place, ManagerRole: claim.PlanningCommander, Brief: claim.Description})
				if e == nil {
					result = &spawn.SpawnResult{RunID: id}
					// Delivery can be retried at device hello. A committed offer
					// retains custody even when its channel is disconnected.
					if w.OfferPlace != nil {
						_, _ = w.OfferPlace(launchCtx, id)
					}
				}
			}
		} else if e == nil {
			result, e = w.Spawner.Spawn(launchCtx, request)
		}
		if e == nil && (result == nil || result.RunID == 0) {
			e = fmt.Errorf("runtime returned without a recorded execution")
		}
	}
	if e != nil {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if release := w.Store.FailExecutionClaim(cleanupCtx, *claim, strings.TrimSpace(e.Error()), w.now()); release != nil {
			return fmt.Errorf("launch: %v; retained claim: %w", e, release)
		}
	}
	return e
}
