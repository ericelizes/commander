package supervision

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/example-user/commander/internal/store"
)

type Worker struct {
	Store    *store.Store
	Deliver  func(context.Context, string, string) error
	Resume   func(context.Context, store.SupervisionFact) error
	Deferred func(error) bool
	Mode     string
	Now      func() time.Time
	Changes  <-chan struct{}
}

func (w *Worker) Run(ctx context.Context) {
	if w.Now == nil {
		w.Now = time.Now
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		work, cancel := context.WithTimeout(ctx, 20*time.Second)
		if e := w.Pass(work); e != nil {
			log.Printf("supervision: reconciliation unavailable: %v", e)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case _, ok := <-w.Changes:
			if !ok {
				w.Changes = nil
			}
		}
	}
}

type pending struct {
	f store.SupervisionFact
	d Decision
}

func (w *Worker) Pass(ctx context.Context) error {
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Mode == "off" {
		return nil
	}
	mode := w.Mode
	if mode == "active" {
		control, e := w.Store.OrchestrationControl(ctx)
		if e != nil {
			return e
		}
		mode = control.Mode
		if mode == "off" {
			return nil
		}
	}
	now := w.Now()
	planningGroups, e := w.planningQuestionGroups(ctx, mode, now)
	if e != nil {
		return e
	}
	jobs, e := w.Store.LeaseSupervision(ctx, now, 128)
	if e != nil {
		return e
	}
	groups := map[string][]pending{}
	for _, job := range jobs {
		f, e := w.Store.SupervisionFact(ctx, job)
		if e != nil {
			return e
		}
		d := Decide(*f, now)
		if d.Action == "observe" || d.Action == "defer" || d.Action == "resolve" {
			state := "observing"
			if d.Action == "defer" {
				state = "pending"
				if f.Job.AcknowledgedAt > 0 && f.Job.Epoch == f.Epoch && f.Job.ProgressSeq == f.ProgressSeq {
					state = "acknowledged"
				}
			}
			if d.Action == "resolve" {
				state = "resolved"
			}
			if e = w.Store.FinishSupervision(ctx, *f, d.Reason, state, d.Target, "", now.Add(d.CheckAfter), false); e != nil {
				return e
			}
			continue
		}
		// Acknowledgement is not completion: after a bounded interval without a
		// generation/progress change, the same obligation is retried and escalated.
		if job.AcknowledgedAt > 0 && job.Epoch == f.Epoch && job.ProgressSeq == f.ProgressSeq && now.Unix()-job.AcknowledgedAt < 120 {
			if e = w.Store.FinishSupervision(ctx, *f, d.Reason, "acknowledged", d.Target, "", now.Add(30*time.Second), false); e != nil {
				return e
			}
			continue
		}
		if job.Reason == d.Reason && job.Epoch == f.Epoch && job.ProgressSeq == f.ProgressSeq && job.Attempts >= 3 {
			d.Target = f.SupervisorID
			if job.EscalationLevel > 0 || d.Target == "" || d.Target == job.Target {
				d.Target = "commander"
			}
			d.Description = "Investigate unresolved responsibility: " + d.Description
		}
		if mode != "active" {
			if e = w.Store.FinishSupervision(ctx, *f, d.Reason, "shadow", d.Target, "Recovery proposed in observation-only mode", now.Add(time.Minute), false); e != nil {
				return e
			}
			continue
		}
		groups[d.Target] = append(groups[d.Target], pending{*f, d})
	}
	roles := make([]string, 0, len(groups)+len(planningGroups))
	roleSet := map[string]bool{}
	for role := range groups {
		roleSet[role] = true
	}
	for role := range planningGroups {
		roleSet[role] = true
	}
	for role := range roleSet {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		plans := planningGroups[role]
		if len(plans) > 16 {
			if e = w.finishPlanningQuestions(ctx, plans[16:], now.Add(5*time.Second), false, ""); e != nil {
				return e
			}
			plans = plans[:16]
		}
		batch := groups[role]
		if len(batch) > 16 {
			for _, p := range batch[16:] {
				if e = w.Store.FinishSupervision(ctx, p.f, p.d.Reason, "pending", role, "", now.Add(5*time.Second), false); e != nil {
					return e
				}
			}
			batch = batch[:16]
		}
		token, e := w.Store.ClaimSupervisionDispatch(ctx, role, now)
		if e != nil {
			return e
		}
		if token == "" {
			if e = w.finishPlanningQuestions(ctx, plans, now.Add(5*time.Second), false, ""); e != nil {
				return e
			}
			for _, p := range batch {
				if e = w.Store.FinishSupervision(ctx, p.f, p.d.Reason, "pending", role, "", now.Add(5*time.Second), false); e != nil {
					return e
				}
			}
			continue
		}
		prepared := []pending{}
		for _, p := range batch {
			f, ok, err := w.Store.PrepareSupervisionDelivery(ctx, p.f, p.d.Reason, role)
			if err != nil {
				return err
			}
			if ok {
				p.f = f
				prepared = append(prepared, p)
			}
		}
		batch = prepared
		plans, e = w.preparePlanningQuestions(ctx, plans)
		if e != nil {
			return e
		}
		if len(batch) == 0 && len(plans) == 0 {
			_ = w.Store.FinishSupervisionDispatch(ctx, role, token, now.Add(5*time.Second))
			continue
		}
		prompt := ""
		if len(batch) > 0 {
			prompt = Prompt(role, batch)
		}
		prompt += planningQuestionPrompt(role, plans)
		deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if len(plans) == 0 && len(batch) == 1 && batch[0].d.Action == "resume" && w.Resume != nil {
			e = w.Resume(deliveryCtx, batch[0].f)
		} else if w.Deliver != nil {
			e = w.Deliver(deliveryCtx, role, prompt)
		} else {
			e = errors.New("runtime delivery unavailable")
		}
		cancel()
		state, detail := "delivered", ""
		attempted := false
		delay := time.Minute
		if e != nil {
			detail = "Agent delivery failed; internal recovery retains responsibility"
			state = "retry"
			if w.Deferred != nil && w.Deferred(e) {
				state = "pending"
				attempted = false
				detail = "Agent is busy; delivery deferred"
				delay = 5 * time.Second
			}
		}
		for _, p := range batch {
			if state == "pending" && p.f.Job.Attempts > 0 {
				p.f.Job.Attempts--
			}
			status := state
			if e == nil && role != p.f.AgentID {
				status = "escalated"
			}
			retry := delay
			if p.f.Job.Attempts > 2 {
				retry = 5 * time.Minute
			}
			if err := w.Store.FinishSupervision(ctx, p.f, p.d.Reason, status, role, detail, now.Add(retry), attempted); err != nil {
				return err
			}
		}
		for _, job := range plans {
			retry := delay
			if state != "pending" && job.Attempts >= 3 {
				retry = 5 * time.Minute
			}
			if err := w.Store.FinishPlanningQuestionDelivery(ctx, job, now.Add(retry), state != "pending", detail); err != nil {
				return err
			}
		}
		if err := w.Store.FinishSupervisionDispatch(ctx, role, token, now.Add(delay)); err != nil {
			return err
		}
	}
	return nil
}
func Prompt(role string, batch []pending) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Commander supervision: responsibility needs attention for %s. This is internal recovery, not a new user instruction or approval. Read current beads first; respect all holds, waits and authority gates. Acknowledge a current request using the command shown immediately before handling that bead, then review/advance the work or record the concrete legitimate blocker. Mail delivery or acknowledgement alone does not complete the obligation.\n", role)
	for i, p := range batch {
		if i == 16 {
			b.WriteString("Additional obligations will follow in a bounded batch.\n")
			break
		}
		fmt.Fprintf(&b, "- %s: %s. %s. Assignment generation %d, progress generation %d. Acknowledge: cmdr supervision acknowledge --project-id %d --bead %s --epoch %d --request %s\n", p.f.BeadID, p.f.Title, p.d.Description, p.f.Epoch, p.f.ProgressSeq, p.f.ProjectID, p.f.BeadID, p.f.Epoch, p.f.Job.RequestID)
		if p.f.PendingHumanAnswer {
			quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
			fmt.Fprintf(&b, "  A human answer is already saved. Resume that immutable answer with: cmdr internal-resolve-needs --project %s --bead %s --action decide --resume-only. Supply no new choice or text. If its scope changed, inspect both saved history and current conditions with cmdr decision inspect --project-id %d --bead %s. Only for an obsolete/unverifiable scope, prepare an exact current decision and use cmdr decision reconcile --file PATH; it preserves the old answer and all holds, never renews approval. A still-applicable answer must be resumed rather than asked again.\n", quote(p.f.ProjectName), quote(p.f.BeadID), p.f.ProjectID, quote(p.f.BeadID))
		}
	}
	return b.String()
}
