package beads

import "regexp"
import "github.com/example-user/commander/internal/ownercompat"

import "strings"

// Step states shown in the Active Projects tree. A step's state is derived from
// its bead status plus two marker labels, so `br` stays the source of truth.
const (
	StateNotStarted = "not-started"
	StateInProgress = "in-progress"
	StateBlocked    = "blocked"
	StateNeedsUser  = "needs-user"
	StateDone       = "done"
	StateReview     = "review" // an Operator finished; its Manager reviews the bead

	// StatusDeferred is a bead parked with `br defer`: out of play (no next
	// gate, no NEEDS YOU, no wait, no open-work invariant) until it is undeferred.
	StatusDeferred = "deferred"

	BlockedLabel   = "blocked"
	NeedsUserLabel = "needs-user"
	ReviewLabel    = "review"
	// DecisionAnsweredLabel suppresses automatic user asks until explicitly re-flagged.
	DecisionAnsweredLabel = "decision:answered"
	// A gate is a human approval: work past it waits until it is approved
	// (closed). approver:<who> names each person who approves it.
	GateLabel = "gate"
	// A track is a separate outcome inside a workstream: a direct child of the
	// workstream bead with its own gate chain, run by the workstream's one Manager.
	WorkstreamLabel     = "kind:workstream"
	TrackLabel          = "kind:track" // WorkstreamLabel's old name: read until every project is migrated
	ApproverLabelPrefix = "approver:"
	OwnerLabelPrefix    = "owner:"
	// OperatorDiedLabel marks a step whose Operator died: cmdrd cleared the dead
	// claim, and the row reads as died until someone claims or re-plans the step.
	OperatorDiedLabel = "operator-died"
	// cmdrd's automatic restarts of a step's Operator: auto-respawn:<n> after a
	// cmdrd restart or a sleep killed it, crash-respawn:<unix> after it died on
	// its own. They stay on the bead and cap further restarts.
	AutoRespawnLabelPrefix  = "auto-respawn:"
	CrashRespawnLabelPrefix = "crash-respawn:"

	// A bead's level in the app's Graph: viz:1 (gates), viz:2 (stages), viz:3 (everything).
	VizLabelPrefix = "viz:"

	// max-operators:<n> on a workstream bead overrides how many Operators may
	// run under it at once ([concurrency] max_operators_per_workstream).
	MaxOperatorsLabelPrefix = "max-operators:"

	// The options of a workflow gate's decision card.
	GateApprove = "Approve"
	GateChange  = "Change"
	GateDrop    = "Drop"
)

// StepState derives a step's state and owner from its bead status and labels.
// Done wins over everything, then needs-user, blocked, review, in-progress.
func StepState(status string, labels []string) (state, owner string) {
	var blocked, needsUser, review bool
	for _, l := range labels {
		switch {
		case l == BlockedLabel:
			blocked = true
		case ownercompat.Normalize(l) == NeedsUserLabel:
			needsUser = true
		case l == ReviewLabel:
			review = true
		case strings.HasPrefix(l, OwnerLabelPrefix):
			owner = ownercompat.Normalize(strings.TrimPrefix(l, OwnerLabelPrefix))
		}
	}
	switch {
	case status == "closed":
		state = StateDone
	case needsUser:
		state = StateNeedsUser
	case blocked:
		state = StateBlocked
	case review:
		state = StateReview
	case status == "in_progress":
		state = StateInProgress
	default:
		state = StateNotStarted
	}
	return state, owner
}

var ownerUnsafe = regexp.MustCompile(`[^A-Za-z0-9_:-]`)

// OwnerLabelName makes an owner name safe as a label value: br accepts only
// letters, digits, hyphen, underscore and colon (a dotted bead id is not valid).
func OwnerLabelName(owner string) string { return ownerUnsafe.ReplaceAllString(owner, "-") }

// VizOf is the level a bead's viz:<n> label sets (1 to 3); 0 when it has none.
func VizOf(labels []string) int {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, VizLabelPrefix); ok && len(v) == 1 && v[0] >= '1' && v[0] <= '3' {
			return int(v[0] - '0')
		}
	}
	return 0
}

// Superseded is a bead the plan dropped: labeled superseded or retired, or
// closed with a reason that says superseded. Closed is not approved for it.
// The board (livedata) and the gate unlink door share this one reading.
func Superseded(b Bead) bool {
	for _, l := range b.Labels {
		if l == "superseded" || l == "retired" {
			return true
		}
	}
	return b.Status == "closed" && strings.Contains(strings.ToLower(b.CloseReason), "superseded")
}
