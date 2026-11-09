package spawn

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
)

// Reaper reconciles running operator containers (Reap) and host operator tmux
// sessions (ReapHost) against the agent_runs table.
// Containers labeled commander.project=* without a matching `running` row are
// killed and recorded as cost_events.event_type='orphan-reaped'. Defends
// against a partial-start failure where cmdr crashes between docker run and
// agent_runs insert.
type Reaper struct {
	store  AgentRunStore
	docker docker.Client

	// Host-operator reconciliation (see WithHost / ReapHost).
	tmux         tmuxctl.Client
	runs         RunLister
	listSessions func(ctx context.Context) ([]TmuxSession, error)
	// exited reports whether a gone session's Operator ended on its own.
	exited func(session string, since time.Time) bool

	// now is overridable for deterministic tests; defaults to time.Now.
	now func() time.Time
}

// NewReaper constructs a Reaper.
func NewReaper(s AgentRunStore, d docker.Client) *Reaper {
	return &Reaper{store: s, docker: d, now: time.Now, exited: exitedSince}
}

// Reap inspects every container with the commander.project label, looks up
// the matching agent_runs row by container_id, and stops any container that
// has no `running` row. Returns the IDs of containers stopped (orphans) and
// the first error encountered (continues past per-container errors so a
// single misbehaving container can't block the entire sweep).
func (r *Reaper) Reap(ctx context.Context) ([]string, error) {
	containers, err := r.docker.ListByLabel(ctx, "commander.project")
	if err != nil {
		return nil, fmt.Errorf("reaper: list containers: %w", err)
	}

	var killed []string
	var firstErr error
	for _, c := range containers {
		run, err := r.store.GetAgentRunByContainer(ctx, c.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reaper: lookup %s: %w", c.ID, err)
			}
			continue
		}
		// Orphan = either no row at all, or row exists but is no longer running.
		if run != nil && run.Status == "running" {
			continue
		}

		// Stop the container. Idempotent in our wrapper — "No such container"
		// is treated as success.
		if err := r.docker.Stop(ctx, c.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reaper: stop %s: %w", c.ID, err)
			}
			continue
		}
		killed = append(killed, c.ID)

		// Record an orphan-reaped cost event so the Commander view's audit
		// trail surfaces the kill. project_id falls back to 0 when we can't
		// resolve it (no row in agent_runs); reason carries the container ID.
		var projectID int64
		var taskID sql.NullString
		var agentName string
		if run != nil {
			projectID = run.ProjectID
			taskID = sql.NullString{String: run.TaskBeadID, Valid: run.TaskBeadID != ""}
			agentName = run.AgentName
		}
		_, _ = r.store.InsertCostEvent(ctx, store.CostEvent{
			ProjectID:  projectID,
			TaskBeadID: taskID,
			AgentName:  agentName,
			EventType:  "orphan-reaped",
			Reason: sql.NullString{
				String: fmt.Sprintf("container %s had no running agent_runs row", c.ID),
				Valid:  true,
			},
			OccurredAt: r.now().Unix(),
		})
		if run != nil {
			// Mark the row closed if it existed but wasn't running — this
			// shouldn't be a common case but keeps the table in shape.
			_ = r.store.MarkClosed(ctx, run.ID, "orphan-reaped")
		}
	}

	return killed, firstErr
}
