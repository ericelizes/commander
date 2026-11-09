package ceilings

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// killTimeout bounds how long any single kill side-effect (docker stop, am
// send, bd set-state, store updates) is allowed to block. Each side effect
// runs under its own derived ctx so a stuck am call can't wedge the docker
// stop or the cost_events insert.
const killTimeout = 30 * time.Second

// kill is the watchdog's terminal handler for one operator. Side effects, in
// order, with each step idempotent + best-effort (failures are logged but do
// not abort the rest of the chain — we want the operator dead and the
// books closed even if one downstream is degraded):
//
//  1. Stop the runtime: `docker stop` (Docker code operator) or `tmux
//     kill-pane` (host operator: knowledge-type or host-runtime code).
//  2. Send an am notification to lead-<project> with reason + usage snapshot.
//  3. bd set-state cmdr=blocked --reason "ceiling: ..."
//  4. agent_runs.MarkClosed(runID, "killed-ceiling").
//  5. cost_events insert ("ceiling-hit").
//  6. Unregister from the watchdog map.
//
// killOnce gates the entire chain so concurrent breach paths (e.g. wallclock
// kill arriving in parallel with token kill) do not double-stop.
func (w *Watchdog) kill(parentCtx context.Context, st *runState, reason string) {
	st.killOnce.Do(func() {
		log.Printf("ceilings: kill run=%d agent=%q reason=%q", st.runID, st.agentName, reason)

		// 1. Stop the runtime. Use a fresh background-derived ctx so the
		//    parent cancellation (e.g. watchdog Stop) doesn't abort the
		//    side effects half-way through.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), killTimeout)
		defer stopCancel()
		w.stopRuntime(stopCtx, st)

		// 2. Notify Lead via am.
		w.notifyLead(stopCtx, st, reason)

		// 3. bd set-state cmdr=blocked.
		if st.taskBeadID != "" && st.projectPath != "" {
			if err := w.beads.SetState(stopCtx, st.projectPath, st.taskBeadID, "cmdr", "blocked", "ceiling: "+reason); err != nil {
				log.Printf("ceilings: bd set-state %s: %v", st.taskBeadID, err)
			}
		}

		// 4. agent_runs.MarkClosed.
		if err := w.store.MarkClosed(stopCtx, st.runID, "killed-ceiling"); err != nil {
			log.Printf("ceilings: mark closed run=%d: %v", st.runID, err)
		}

		// 5. cost_events row.
		st.mu.Lock()
		usdSnap := st.usdUsed
		tokensSnap := st.tokensUsed
		st.mu.Unlock()
		var taskBead sql.NullString
		if st.taskBeadID != "" {
			taskBead = sql.NullString{String: st.taskBeadID, Valid: true}
		}
		ev := store.CostEvent{
			ProjectID:   st.projectID,
			TaskBeadID:  taskBead,
			AgentName:   st.agentName,
			EventType:   "ceiling-hit",
			Reason:      sql.NullString{String: reason, Valid: true},
			DeltaUSD:    usdSnap,
			DeltaTokens: tokensSnap,
			OccurredAt:  nowFunc().Unix(),
		}
		if _, err := w.store.InsertCostEvent(stopCtx, ev); err != nil {
			log.Printf("ceilings: insert cost_event run=%d: %v", st.runID, err)
		}

		// 6. Unregister.
		w.Unregister(st.runID)
	})
}

// stopRuntime dispatches to the right transport for the run type. Errors are
// logged + swallowed: the runtime may already be exiting on its own, and
// mark-closed is what defines the kill from commander's POV.
func (w *Watchdog) stopRuntime(ctx context.Context, st *runState) {
	if st.isKnowledge {
		// A host operator (knowledge-type or host-runtime code): there is no
		// container, so killing it means killing its tmux session's pane,
		// which takes the headless process down with it.
		if st.tmuxPaneID == "" || filepath.IsAbs(st.tmuxPaneID) {
			log.Printf("ceilings: host run=%d has no tmux session id (%q); skipping kill-pane", st.runID, st.tmuxPaneID)
			return
		}
		if err := w.killPane(ctx, st.tmuxPaneID); err != nil {
			log.Printf("ceilings: tmux kill-pane %s: %v", st.tmuxPaneID, err)
		}
		return
	}
	if st.containerID == "" {
		log.Printf("ceilings: code run=%d has no container id; skipping docker stop", st.runID)
		return
	}
	if err := w.docker.Stop(ctx, st.containerID); err != nil {
		log.Printf("ceilings: docker stop %s: %v", st.containerID, err)
	}
}

// tmuxKillPane is the production Watchdog.killPane.
func tmuxKillPane(ctx context.Context, target string) error {
	_, stderr, err := tmuxctl.RunOnSession(ctx, "kill-pane", "-t", target)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// notifyLead sends an am message with the ceiling reason + final usage
// snapshot. Subject + body shape stays terse — the Lead's chat is the human
// reader and shorter is better.
func (w *Watchdog) notifyLead(ctx context.Context, st *runState, reason string) {
	if st.projectName == "" || st.agentName == "" {
		log.Printf("ceilings: skip am notify run=%d (missing project/agent meta)", st.runID)
		return
	}
	st.mu.Lock()
	usd := st.usdUsed
	tokens := st.tokensUsed
	st.mu.Unlock()

	subject := fmt.Sprintf("operator %s killed: %s", st.agentName, reason)
	body := fmt.Sprintf(
		"Operator %s in project %s was killed by the cost watchdog.\n\n"+
			"Reason: %s\nTokens used: %d\nUSD used: $%.4f\nTask: %s\n",
		st.agentName, st.projectName, reason, tokens, usd, st.taskBeadID,
	)
	to := "lead-" + st.projectName
	if err := w.am.Send(ctx, st.projectName, st.agentName, to, subject, body, agentmail.SendOptions{Importance: "urgent"}); err != nil {
		log.Printf("ceilings: am send %s->%s: %v", st.agentName, to, err)
	}
}
