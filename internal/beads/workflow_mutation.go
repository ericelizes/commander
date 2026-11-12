package beads

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/store"
	"slices"
	"strings"
)

func workflowManaged(b Bead) bool {
	return workflowLabel(b, "workflow-template:") != "" || workflowLabel(b, "workflow-step:") != ""
}
func workflowGate(b Bead) bool {
	return slices.Contains(b.Labels, "kind:gate") || slices.Contains(b.Labels, GateLabel)
}
func workflowIdentity(labels []string, managed bool) []string {
	out := []string{}
	for _, l := range labels {
		if managed && (strings.HasPrefix(l, "workflow-template:") || strings.HasPrefix(l, "workflow-step:") || strings.HasPrefix(l, "workflow-instance:") || strings.HasPrefix(l, "workflow-hash:") || strings.HasPrefix(l, "workflow-version:")) || !managed && (l == "kind:gate" || l == GateLabel || strings.HasPrefix(l, "gate-of:") || strings.HasPrefix(l, "gate-name:") || strings.HasPrefix(l, "verifier:") || strings.HasPrefix(l, "approver:")) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}
func pgVerifyCompletion(ctx context.Context, q store.Querier, pid int64, id string) error {
	if e := pgCheckStageGates(ctx, q, pid, id); e != nil {
		return e
	}
	v, ok := ctx.Value(completionValidatorKey{}).(CompletionValidator)
	if !ok {
		return errors.New("active completion requires the shared workflow verifier; use the guarded Commander service")
	}
	return v(context.WithValue(ctx, postgresReaderKey{}, q), q, pid, id)
}

// Before a definition or retirement can hide a required check, verify the old
// definition while it is still visible. Workflow snapshots and their generated
// identities remain immutable; re-planning creates a new explicit instance.
func pgCheckWorkflowMutation(ctx context.Context, q store.Querier, pid int64, b Bead, verb string, pos []string, f pgFlags) error {
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	after := b
	after.Labels = slices.Clone(b.Labels)
	if verb == "update" {
		if f.has("--title") {
			after.Title = f.one("--title")
		}
		if f.has("--status") {
			after.Status = f.one("--status")
		}
		if f.has("--labels") {
			after.Labels = strings.Split(f.one("--labels"), ",")
		}
		for _, l := range f["--remove-label"] {
			after.Labels = slices.DeleteFunc(after.Labels, func(v string) bool { return v == l })
		}
		for _, l := range f["--add-label"] {
			if !slices.Contains(after.Labels, l) {
				after.Labels = append(after.Labels, l)
			}
		}
	}
	if verb == "set-state" && len(pos) == 2 {
		dim, value, ok := strings.Cut(pos[1], "=")
		if ok {
			after.Labels = slices.DeleteFunc(after.Labels, func(l string) bool { return strings.HasPrefix(l, dim+":") })
			after.Labels = append(after.Labels, dim+":"+value)
		}
	}
	if verb == "close" {
		after.Status = "closed"
	}
	managed, gate := workflowManaged(b), workflowGate(b)
	if managed {
		if !slices.Equal(workflowIdentity(b.Labels, true), workflowIdentity(after.Labels, true)) {
			return errors.New("workflow identity is immutable; preserve this instance and plan an explicit replacement")
		}
		if verb == "delete" || f.has("--parent") && f.one("--parent") != b.ParentID {
			return errors.New("workflow history and instance placement cannot be removed or moved by a raw write")
		}
		if workflowLabel(b, "workflow-step:") == "" && f.has("--description") && f.one("--description") != b.Description {
			return errors.New("workflow snapshots are immutable; create an explicit replacement instance")
		}
	}
	if gate && slices.Contains(b.Labels, "verifier:auto") && slices.Contains(b.Labels, "gate-status:failed") != slices.Contains(after.Labels, "gate-status:failed") {
		result, ok := AutomaticGateResultOf(ctx)
		if !ok || result.ProjectID != pid || result.Bead != b.ID {
			return errors.New("automatic outcome labels are service-owned; use the canonical gate check")
		}
	}
	if gate && b.Status == "closed" && after.Status == "closed" {
		changed := f.has("--title") && f.one("--title") != b.Title || f.has("--description") && f.one("--description") != b.Description || f.has("--acceptance") && f.one("--acceptance") != b.AcceptanceCriteria || f.has("--parent") && f.one("--parent") != b.ParentID
		prs := func(labels []string) []string {
			out := []string{}
			for _, l := range labels {
				if strings.HasPrefix(l, "pr:") {
					out = append(out, l)
				}
			}
			slices.Sort(out)
			return out
		}
		if changed || !slices.Equal(prs(b.Labels), prs(after.Labels)) {
			return errors.New("reopen this completed gate before changing its check or PR definition")
		}
	}
	if gate && !slices.Equal(workflowIdentity(b.Labels, false), workflowIdentity(after.Labels, false)) {
		return errors.New("required gate identity cannot be weakened; use the guarded gate replacement path")
	}
	retiring := !slices.Contains(b.Labels, "retired") && slices.Contains(after.Labels, "retired") || !slices.Contains(b.Labels, "superseded") && slices.Contains(after.Labels, "superseded")
	hiding := verb == "delete" || retiring
	if hiding {
		if e = pgCheckStageGates(ctx, q, pid, b.ID); e != nil {
			return e
		}
	}
	// Deferral parks work; it must remain an unpassed dependency. Do not demand
	// a passing review in order to stop unfinished work.
	erasedMerge := WorkflowMergeTarget(b) && !WorkflowMergeTarget(after) && after.Status != "closed" && after.Status != StatusDeferred
	if (hiding && after.Status != "closed" && (managed || gate || WorkflowMergeTarget(b))) || erasedMerge {
		return pgVerifyCompletion(ctx, q, pid, b.ID)
	}
	return nil
}

func pgCheckWorkflowUnlink(ctx context.Context, q store.Querier, pid int64, from, to string) error {
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	rows, e := q.QueryContext(ctx, `SELECT b.id,b.status,b.labels FROM bead_tasks b WHERE b.project_id=? AND b.id IN(?,?) AND b.deleted_at IS NULL`, pid, from, to)
	if e != nil {
		return e
	}
	found := map[string]Bead{}
	for rows.Next() {
		var b Bead
		var raw []byte
		if e = rows.Scan(&b.ID, &b.Status, &raw); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(raw, &b.Labels); e != nil {
			rows.Close()
			return e
		}
		found[b.ID] = b
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	a, b := found[from], found[to]
	if instance := workflowLabel(a, "workflow-instance:"); instance != "" && instance == workflowLabel(b, "workflow-instance:") {
		return errors.New("workflow predecessor edges are part of its immutable snapshot")
	}
	if workflowManaged(b) || workflowGate(b) {
		var exists bool
		if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_dependencies WHERE project_id=? AND issue_id=? AND depends_on_id=? AND type='blocks')`, pid, from, to).Scan(&exists); e != nil || !exists {
			return e
		}
		if b.Status != "closed" {
			if publicationEdgeAllowed(ctx, pid, from, to) {
				return nil
			}
			// The planning Commander rewires its own gates; a user gate is
			// still guarded by pgCheckHumanUnlink. Workflow checks stay bound.
			if workflowGate(b) {
				if planner, e := pgPlanningOwnerCaller(ctx, q, pid, from); e != nil || planner {
					return e
				}
			}
			return errors.New("an unfinished required gate cannot be unlinked to release work; complete it or use an explicit guarded scope repair")
		}
		if e = pgVerifyCompletion(ctx, q, pid, from); e != nil {
			return e
		}
	}
	return nil
}

func pgCheckStageGates(ctx context.Context, q store.Querier, pid int64, id string) error {
	var unfinished bool
	e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bead_tasks g WHERE g.project_id=? AND g.id<>? AND g.deleted_at IS NULL AND jsonb_exists(g.labels,'kind:gate') AND jsonb_exists(g.labels,?) AND (g.status<>'closed' OR commander_pending_human_answer(g.project_id,g.id)))`, pid, id, "gate-of:"+EscapeLabel(id)).Scan(&unfinished)
	if e != nil {
		return e
	}
	if unfinished {
		return errors.New("this stage has an unpassed gate or an unapplied recorded answer; finish its guarded result first")
	}
	return nil
}
