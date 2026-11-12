package beads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
)

func humanLabels(labels []string) bool {
	if humanGate(labels) {
		return true
	}
	for _, label := range labels {
		if label == "hold" || ownercompat.Normalize(label) == NeedsUserLabel || label == GateLabel || label == "verifier:user" || label == "approver:user" {
			return true
		}
		if wait, ok := ParseWaitLabel(label); ok && wait.Kind == WaitUser {
			return true
		}
	}
	return false
}
func humanPending(labels []string) bool {
	for _, label := range labels {
		if ownercompat.Normalize(label) == NeedsUserLabel {
			return true
		}
		if wait, ok := ParseWaitLabel(label); ok && wait.Kind == WaitUser {
			return true
		}
	}
	return false
}
func humanGate(labels []string) bool {
	if slices.Contains(labels, GateLabel) {
		return true
	}
	for _, label := range labels {
		if value, ok := strings.CutPrefix(label, "verifier:"); ok && ownercompat.Normalize(value) == "user" {
			return true
		}
		if value, ok := strings.CutPrefix(label, ApproverLabelPrefix); ok && ownercompat.Normalize(value) == "user" {
			return true
		}
	}
	return false
}
func currentHumanDecision(ctx context.Context, q store.Querier, pid int64, id string) (*store.NeedsResolution, error) {
	var raw string
	e := q.QueryRowContext(ctx, `SELECT r.payload FROM decision_authorities a JOIN needs_resolutions r ON r.project_id=a.project_id AND r.bead_id=a.bead_id AND r.generation=a.generation JOIN bead_tasks b ON b.project_id=a.project_id AND b.id=a.bead_id WHERE a.project_id=? AND a.bead_id=? AND a.authority_epoch=b.human_authority_epoch AND r.state<>? AND a.generation=(SELECT CAST(MAX(version) AS TEXT) FROM pitches WHERE project_id=a.project_id AND bead_id=a.bead_id)`, pid, id, store.ResolutionSuperseded).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var decision store.NeedsResolution
	if e = json.Unmarshal([]byte(raw), &decision); e != nil {
		return nil, e
	}
	return &decision, nil
}
func approvedHumanDecision(r *store.NeedsResolution) bool {
	return r != nil && (r.Action == "approve" || r.Approved || r.GateChoice == GateApprove)
}

// This is a mutation invariant, shared by single writes and atomic batches.
// Coordination approvals are not human approvals. Clearing markers cannot be
// used to make a later close forget the decision that was required.
func pgCheckHumanMutation(ctx context.Context, q store.Querier, pid int64, id, verb, oldStatus, newStatus string, before, after []string, parentChanged bool, flags pgFlags) error {
	if pgCallerValue(ctx, "COMMANDER_ROLE") == "" {
		return nil
	}
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	ending := ((newStatus == "closed" || newStatus == StatusDeferred) && newStatus != oldStatus) || verb == "delete" || (!slices.Contains(before, "retired") && slices.Contains(after, "retired")) || (!slices.Contains(before, "superseded") && slices.Contains(after, "superseded"))
	protected := humanLabels(before)
	clearingHold := slices.Contains(before, "hold") && !slices.Contains(after, "hold")
	answering := !slices.Contains(before, DecisionAnsweredLabel) && slices.Contains(after, DecisionAnsweredLabel)
	dismissing := humanPending(before) && (!humanPending(after) || ending)
	for _, label := range before {
		wait, isWait := ParseWaitLabel(label)
		if (ownercompat.Normalize(label) == NeedsUserLabel || isWait && wait.Kind == WaitUser) && !slices.Contains(after, label) {
			dismissing = true
		}
	}
	weakening := humanGate(before) && (!humanGate(after) || !slices.Contains(after, "kind:gate") && slices.Contains(before, "kind:gate"))
	if humanGate(before) {
		for _, label := range before {
			if (label == GateLabel || label == "kind:gate" || strings.HasPrefix(label, "approver:") || strings.HasPrefix(label, "verifier:")) && !slices.Contains(after, label) {
				weakening = true
			}
		}
	}
	if weakening {
		return errors.New("an agent cannot weaken an existing human gate; preserve its definition and use the recorded user decision path")
	}
	if oldStatus == "closed" && newStatus == "closed" && humanGate(before) {
		changed := parentChanged
		var title, description, acceptance, kind string
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(after)
		var sameLabels bool
		if e = q.QueryRowContext(ctx, `SELECT title,description,acceptance_criteria,issue_type,commander_authority_plan_labels(?::jsonb)=commander_authority_plan_labels(?::jsonb) FROM bead_tasks WHERE project_id=? AND id=?`, string(beforeJSON), string(afterJSON), pid, id).Scan(&title, &description, &acceptance, &kind, &sameLabels); e != nil {
			return e
		}
		if !sameLabels {
			changed = true
		}
		for _, pair := range [][2]string{{"--title", title}, {"--description", description}, {"--acceptance", acceptance}, {"--type", kind}} {
			if flags.has(pair[0]) && flags.one(pair[0]) != pair[1] {
				changed = true
			}
		}
		identity := func(labels []string) []string {
			out := []string{}
			for _, label := range labels {
				if label == GateLabel || label == "kind:gate" || strings.HasPrefix(label, "approver:") || strings.HasPrefix(label, "verifier:") {
					out = append(out, label)
				}
			}
			slices.Sort(out)
			return out
		}
		if changed || !slices.Equal(identity(before), identity(after)) {
			return errors.New("reopen this approval gate when changing its intent; a past decision cannot approve new conditions")
		}
	}
	if answering || protected && (clearingHold || dismissing || weakening || ending) {
		decision, err := currentHumanDecision(ctx, q, pid, id)
		if err != nil {
			return err
		}
		if decision == nil {
			return errors.New("recorded human direction is required; preserve this hold or decision and use its typed answer path")
		}
		approvalNeeded := clearingHold || (newStatus == "closed" && newStatus != oldStatus) || verb == "delete" || weakening
		if approvalNeeded && !approvedHumanDecision(decision) {
			return errors.New("the recorded human answer does not approve releasing this hold or gate")
		}
		if humanGate(before) && newStatus == StatusDeferred && decision.GateChoice != GateDrop && decision.Action != "defer" {
			return errors.New("deferring this gate requires the recorded human Drop or defer decision")
		}
	}
	if parentChanged || ending {
		var heldAncestor bool
		e = q.QueryRowContext(ctx, `WITH RECURSIVE ancestry AS (SELECT parent_id FROM bead_tasks WHERE project_id=? AND id=? UNION SELECT b.parent_id FROM bead_tasks b JOIN ancestry a ON b.id=a.parent_id WHERE b.project_id=?) SELECT EXISTS(SELECT 1 FROM ancestry a JOIN bead_tasks b ON b.project_id=? AND b.id=a.parent_id WHERE jsonb_exists(b.labels,'hold') OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(b.labels) label WHERE label ~ '^needs-' OR label ~ '^waiting:[^:]+:user(:|$)' OR label ~ '^w:[^:]+:us(:|$)'))`, pid, id, pid, pid).Scan(&heldAncestor)
		if e != nil {
			return e
		}
		if heldAncestor {
			return errors.New("changing or finishing work cannot bypass an ancestor's human hold; record and apply that decision first")
		}
	}
	if ending && oldStatus != "closed" {
		var unfinished bool
		e = q.QueryRowContext(ctx, `SELECT (?::boolean AND (EXISTS(SELECT 1 FROM bead_tasks WHERE project_id=? AND parent_id=? AND deleted_at IS NULL AND NOT commander_dependency_complete(status,labels)) OR EXISTS(SELECT 1 FROM bead_dependencies d JOIN bead_tasks b ON b.project_id=d.project_id AND b.id=d.depends_on_id WHERE d.project_id=? AND d.issue_id=? AND d.type='blocks' AND b.deleted_at IS NULL AND NOT commander_dependency_complete(b.status,b.labels)))) OR EXISTS(SELECT 1 FROM bead_tasks b JOIN agent_runs r ON r.id=b.active_run_id WHERE b.project_id=? AND b.id=? AND r.status='running')`, newStatus != StatusDeferred, pid, id, pid, id, pid, id).Scan(&unfinished)
		if e != nil {
			return e
		}
		if unfinished {
			return errors.New("cannot finish or hide work with open children, unpassed dependencies or a live execution")
		}
	}
	return nil
}

func pgCheckHumanUnlink(ctx context.Context, q store.Querier, pid int64, from, id string) error {
	if publicationEdgeAllowed(ctx, pid, from, id) {
		return nil
	}
	if pgCallerValue(ctx, "COMMANDER_ROLE") == "" {
		return nil
	}
	active, e := pgActiveExecution(ctx, q)
	if e != nil || !active {
		return e
	}
	var raw []byte
	var status string
	if e = q.QueryRowContext(ctx, `SELECT labels,status FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL`, pid, id).Scan(&raw, &status); e != nil {
		return e
	}
	var labels []string
	if e = json.Unmarshal(raw, &labels); e != nil {
		return e
	}
	if status != "closed" && humanLabels(labels) {
		decision, e := currentHumanDecision(ctx, q, pid, id)
		if e != nil {
			return e
		}
		if !approvedHumanDecision(decision) {
			return errors.New("removing this dependency would bypass recorded human direction; resolve its decision first")
		}
	}
	return nil
}

// DependencyComplete is shared by readers: parking or retiring a required
// approval never turns it into a passed check.
func DependencyComplete(b Bead) bool {
	if b.HumanAnswerPending {
		return false
	}
	if b.Status == "closed" {
		return true
	}
	if humanLabels(b.Labels) || slices.Contains(b.Labels, "kind:gate") || workflowManaged(b) {
		return false
	}
	return b.Status == StatusDeferred || Superseded(b)
}

// HumanApprovalGate covers published and legacy user gate vocabularies.
func HumanApprovalGate(b Bead) bool { return humanGate(b.Labels) }

func pgCheckCompletedStructure(ctx context.Context, q store.Querier, pid int64, id string) error {
	var status string
	if e := q.QueryRowContext(ctx, `SELECT status FROM bead_tasks WHERE project_id=? AND id=? AND deleted_at IS NULL`, pid, id).Scan(&status); e != nil {
		return e
	}
	if status == "closed" {
		return errors.New("reopen completed work before adding children or changing its required dependencies")
	}
	return nil
}
