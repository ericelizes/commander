package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/store"
)

type retirementKey struct{}

// Only pgRetire can mint this transaction-local grant, after validating every
// member. A close reason or a client-supplied label cannot mint one.
func retiringIn(ctx context.Context, id string) bool {
	ids, _ := ctx.Value(retirementKey{}).(map[string]bool)
	return ids[id]
}

// Retire preserves definitions, edges, failed outcomes and comments. The service
// expands and validates the entire scope under the graph lock before any write.
// It never falls back to the legacy backend.
func Retire(ctx context.Context, dir, id, mode, reason string, dry bool) (DoorResponse, error) {
	if !PostgresConfigured() {
		return DoorResponse{}, errors.New("planning retirement requires PostgreSQL")
	}
	args := []string{"retire", id, "--mode", mode, "--reason", reason}
	if dry {
		args = append(args, "--dry-run", "true")
	}
	return viaDoor(ctx, dir, DoorRequest{Args: args, Atomic: true, NoWait: true})
}

func pgRetire(ctx context.Context, q store.Querier, pid int64, args []string) ([]byte, error) {
	pos, f, err := parsePGFlags(args, "--mode --reason --dry-run")
	if err != nil {
		return nil, err
	}
	if len(pos) != 1 || strings.TrimSpace(f.one("--reason")) == "" {
		return nil, errors.New("retirement requires one bead and a reason")
	}
	id, mode := pos[0], f.one("--mode")
	planner, err := pgPlanningOwnerCaller(ctx, q, pid, id)
	if err != nil {
		return nil, err
	}
	if !planner {
		return nil, errors.New("retirement requires the current planning Commander, without an execution identity")
	}
	root, err := pgQualifiedEndpoint(ctx, q, pid, id)
	if err != nil {
		return nil, err
	}
	ids := []string{id}
	switch mode {
	case "workflow-block":
		if workflowLabel(*root, "workflow-template:") != "pr-review" || workflowLabel(*root, "workflow-step:") != "" || workflowLabel(*root, "workflow-instance:") == "" {
			return nil, errors.New("target must be a plugged pr-review block")
		}
		rows, e := q.QueryContext(ctx, `SELECT id FROM bead_tasks WHERE project_id=? AND parent_id=? AND deleted_at IS NULL ORDER BY id`, pid, id)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var child string
			if e = rows.Scan(&child); e != nil {
				rows.Close()
				return nil, e
			}
			ids = append(ids, child)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	case "legacy-stage":
		if !slices.Contains(root.Labels, "kind:objective") || workflowManaged(*root) || workflowGate(*root) {
			return nil, errors.New("target must be an ordinary legacy stage")
		}
		rows, e := q.QueryContext(ctx, `SELECT id FROM bead_tasks WHERE project_id=? AND deleted_at IS NULL AND jsonb_exists(labels,?) ORDER BY id`, pid, "gate-of:"+EscapeLabel(id))
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var gate string
			if e = rows.Scan(&gate); e != nil {
				rows.Close()
				return nil, e
			}
			ids = append(ids, gate)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	default:
		return nil, errors.New("retirement mode must be workflow-block or legacy-stage")
	}
	if len(ids) > MaxOps {
		return nil, errors.New("retirement scope exceeds the bounded write limit")
	}
	allowed := map[string]bool{}
	for _, child := range ids {
		b, e := pgQualifiedEndpoint(ctx, q, pid, child)
		if e != nil {
			return nil, e
		}
		owner, e := pgPlanningOwnerCaller(ctx, q, pid, child)
		if e != nil {
			return nil, e
		}
		if !owner || IsWorkstream(b.Labels) || IsClosingGate(b.Labels) || humanLabels(b.Labels) || slices.Contains(b.Labels, DecisionAnsweredLabel) || slices.Contains(b.Labels, "approved") {
			return nil, fmt.Errorf("cannot retire %s: user authority, hold or workstream", child)
		}
		if child != id {
			if mode == "legacy-stage" && (!workflowGate(*b) || !slices.Contains(b.Labels, "verifier:auto")) {
				return nil, errors.New("legacy stage has a nonautomatic gate")
			}
			if mode == "workflow-block" && (workflowLabel(*b, "workflow-instance:") != workflowLabel(*root, "workflow-instance:") || workflowLabel(*b, "workflow-step:") == "" || workflowLabel(*b, "workflow-hash:") != workflowLabel(*root, "workflow-hash:")) {
				return nil, errors.New("workflow child identity mismatch")
			}
		}
		var unsafe bool
		if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE project_id=? AND task_bead_id=? AND status='running') OR EXISTS(SELECT 1 FROM needs_you WHERE project_id=? AND bead_id=?) OR EXISTS(SELECT 1 FROM workstream_managers WHERE project_id=? AND workstream_bead_id=?)`, pid, child, pid, child, pid, child).Scan(&unsafe); e != nil {
			return nil, e
		}
		if unsafe {
			return nil, fmt.Errorf("cannot retire %s: active execution or user card", child)
		}
		allowed[child] = true
	}
	// Refuse unknown children and attached checks, including parked ones: a
	// block is all or none, and retirement cannot hide a separate user gate.
	for _, child := range ids {
		rows, e := q.QueryContext(ctx, `SELECT id FROM bead_tasks WHERE project_id=? AND (parent_id=? OR jsonb_exists(labels,?)) AND deleted_at IS NULL AND status<>'closed'`, pid, child, "gate-of:"+EscapeLabel(child))
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var sub string
			if e = rows.Scan(&sub); e != nil {
				rows.Close()
				return nil, e
			}
			if !allowed[sub] {
				rows.Close()
				return nil, fmt.Errorf("unretired child or attached gate %s", sub)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	if f.one("--dry-run") == "true" {
		return json.Marshal(map[string]any{"would_retire": ids})
	}
	ctx = context.WithValue(ctx, retirementKey{}, allowed)
	reason := "superseded: " + strings.TrimSpace(f.one("--reason"))
	// Children first; predecessor order is deliberately irrelevant to retirement.
	order := append(slices.Clone(ids[1:]), id)
	for _, child := range order {
		b, e := pgQualifiedEndpoint(ctx, q, pid, child)
		if e != nil {
			return nil, e
		}
		if b.Status == "closed" {
			continue
		}
		if _, e = pgWrite(ctx, q, pid, []string{"comments", "add", child, "planning-retirement@v1 " + reason}); e != nil {
			return nil, e
		}
		if _, e = pgWrite(ctx, q, pid, []string{"update", child, "--add-label", "superseded"}); e != nil {
			return nil, e
		}
		if _, e = pgWrite(ctx, q, pid, []string{"close", child, "--reason", reason}); e != nil {
			return nil, e
		}
	}
	return json.Marshal(map[string]any{"retired": ids, "reason": reason})
}
