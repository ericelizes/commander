package beads

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/store"
)

// Execution attribution is a caller context, not an agent-name convention. It
// outranks planning registration; a registered conversation can also have an
// execution run, which must not borrow its planning authority.
func HasExecutionAttribution(env []string) bool {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && (key == "COMMANDER_RUN_ID" || key == "COMMANDER_RUN_SESSION" || key == "COMMANDER_MANAGER_ROLE") {
			return true
		}
	}
	return false
}
func ExecutionAttributed(ctx context.Context) bool {
	if env, ok := ctx.Value(doorEnvKey{}).([]string); ok {
		return HasExecutionAttribution(env)
	}
	return HasExecutionAttribution(os.Environ())
}
func requirePlanningCaller(ctx context.Context) error {
	if ExecutionAttributed(ctx) {
		return errors.New("execution sessions cannot borrow a conversation's planning authority")
	}
	return nil
}

// Active orchestration separates execution evidence from planning authority.
// Shadow mode preserves legacy behavior until the coordinated cutover.
func pgActiveExecution(ctx context.Context, q store.Querier) (bool, error) {
	var active bool
	e := q.QueryRowContext(ctx, `SELECT mode='active' FROM orchestration_control WHERE id=1`).Scan(&active)
	return active, e
}

func pgCheckExecutorWrite(ctx context.Context, q store.Querier, pid int64, verb string, pos []string, f pgFlags) error {
	if len(pos) == 0 {
		return errors.New("execution update requires its assigned bead")
	}
	switch verb {
	case "comments", "set-state", "update":
	default:
		return errors.New("execution agents report work; only a planning Commander may edit the bead tree")
	}
	rid, e := strconv.ParseInt(pgCallerValue(ctx, "COMMANDER_RUN_ID"), 10, 64)
	if e != nil || rid < 1 || pgCallerValue(ctx, "COMMANDER_RUN_SESSION") == "" {
		return errors.New("execution update requires its immutable run and session")
	}
	var current bool
	e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE b.project_id=? AND b.id=? AND r.id=? AND r.status='running' AND r.opencode_session_id=? AND replace(r.agent_name,'.','-')=replace(?::text,'.','-'))`, pid, pos[0], rid, pgCallerValue(ctx, "COMMANDER_RUN_SESSION"), pgCallerValue(ctx, "COMMANDER_ROLE")).Scan(&current)
	if e != nil {
		return e
	}
	if !current {
		return errors.New("execution update belongs to another bead, session or inactive run")
	}
	if verb == "comments" {
		return nil
	}
	if verb == "set-state" {
		if len(pos) != 2 {
			return errors.New("execution phase requires one value")
		}
		dim, value, ok := strings.Cut(pos[1], "=")
		if !ok || dim != "cmdr" || !executionPhase(value) {
			return errors.New("execution agents may report an active phase; use a question or final report for decisions and results")
		}
		for key := range f {
			if key != "--reason" {
				return errors.New("execution phase cannot change planning fields")
			}
		}
		return nil
	}
	for key, values := range f {
		switch key {
		case "--status":
			for _, value := range values {
				if value != "open" && value != "in_progress" {
					return errors.New("execution results must be processed by the planning owner")
				}
			}
		case "--add-label", "--remove-label":
			for _, value := range values {
				phase, ok := strings.CutPrefix(value, "cmdr:")
				if !ok || !executionPhase(phase) {
					return errors.New("execution agents cannot change planning labels, holds or gates")
				}
			}
		default:
			return errors.New("execution agents cannot change goals, briefs, acceptance, priority, ownership or structure")
		}
	}
	if len(f) == 0 {
		return errors.New("execution update has no phase or status")
	}
	return nil
}
func executionPhase(value string) bool {
	switch value {
	case "brainstorming", "planning", "executing", "reviewing", "working":
		return true
	}
	return false
}

func pgCheckRuntimeComment(ctx context.Context, q store.Querier, pid int64, bead string) error {
	actor := pgCallerValue(ctx, "COMMANDER_ROLE")
	if actor == "" && !ExecutionAttributed(ctx) {
		return nil
	}
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	if ExecutionAttributed(ctx) {
		return pgCheckExecutorWrite(ctx, q, pid, "comments", []string{bead}, nil)
	}
	var planner bool
	if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commander_sessions WHERE agent_id=?)`, actor).Scan(&planner); e != nil {
		return e
	}
	if planner {
		return nil
	}
	return pgCheckExecutorWrite(ctx, q, pid, "comments", []string{bead}, nil)
}
