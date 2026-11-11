// Package holding is the one definition of "held": an open bead is legal only
// while something is moving it. Every reader (the open-bead invariant, the
// diagram's unheld finding, the audit, the close door's UnheldAfter, StalledWork
// and graph check) decides it here, on the same inputs. The package is pure: it
// reads through a World and does no I/O.
package holding

import (
	"slices"
	"strings"
)

// Kind is what holds a bead (the closed set of invariant I1).
type Kind string

const (
	Gate             Kind = "gate"              // a gate bead: held by its stage and approvers
	User             Kind = "user"              // a needs-user card
	Children         Kind = "children"          // at least one open child answers for it
	Parked           Kind = "parked"            // status deferred
	Wait             Kind = "wait"              // a typed wait (ci, review, operator, until with a label ...)
	Operator         Kind = "operator"          // a live Operator (or a live named agent) on it
	Blocker          Kind = "blocker"           // an open, unparked, non-superseded bead it waits on
	Queue            Kind = "queue"             // durable executable intent awaiting runtime capacity
	PlanningDecision Kind = "planning-decision" // recorded question for the responsible planning owner
)

// Status values the predicate reads.
const (
	StatusClosed     = "closed"
	StatusDeferred   = "deferred"
	StatusInProgress = "in_progress"
)

// Owner states an Operator's claim can carry (empty = not an Operator run).
const (
	OwnerWorking  = "working"
	OwnerFinished = "finished"
	OwnerDied     = "died"
)

// Bead is the part of a bead Held reads.
type Bead struct {
	Queued             bool
	QuestionWaiting    bool
	HumanAnswerPending bool
	HumanHold          bool
	ID, Parent         string
	Status             string
	Gate               bool
	NeedsUser          bool
	Superseded         bool
	Owner              string
	OwnerState         string
	Waits              int // recorded typed waits (and until: time waits), waits on an Operator left out
	// OperatorWaits are the roles of the bead's waits on an Operator: such a wait holds only
	// while that Operator runs (World.OwnerLive), as the board reads it (MarkLiveOwners).
	OperatorWaits []string
	Untyped       string // label of a slug-labelled time wait; nothing but the clock clears it
	Until         bool   // an owned job with a finish line (until:<x>)
	BlockedBy     []string
	Labels        []string // the raw labels, for a writer that simulates a label edit (Held never reads them)
}

// World answers what a Bead cannot: the other beads and who is alive.
type World interface {
	Bead(id string) (Bead, bool)
	HasOpenChild(id string) bool
	// OwnerLive is the live state of an agent role ("working", "idle", ...);
	// known is false when the source cannot say (an older cmdrd), and an owner
	// is then taken as live.
	OwnerLive(owner string) (state string, known bool)
}

// Holder is who holds a bead, and Why a bead has none.
type Holder struct {
	Kind Kind
	Ref  string
}

// Why a bead is unheld ("" when held).
type Why string

const (
	WhyNone         Why = ""
	WhyClosed       Why = "closed"
	WhyUntypedWait  Why = "untyped-wait" // a slug-labelled wait names nobody who will act
	WhyOwnerGone    Why = "owner-gone"   // its Operator died or finished
	WhyNoHolder     Why = "no-holder"    // nothing at all
	WhyManagerIdle  Why = "manager-idle" // only a Manager/Commander name, and it is between turns
	WhyOwnerNotLive Why = "owner-not-live"
)

// IsManager reports whether an owner names a Manager or Commander. Such a name
// says who is accountable, not who is moving: it holds only mid-turn.
func IsManager(owner string) bool {
	return strings.HasPrefix(owner, "manager-") || owner == "commander" || strings.HasPrefix(owner, "commander-")
}

// BlockerHolds reports whether a blocker still holds its dependant back: open and
// in play. A required gate or human hold remains unpassed when parked or retired.
func BlockerHolds(b Bead) bool {
	return b.HumanAnswerPending || b.Status != StatusClosed && (b.Gate || b.NeedsUser || b.HumanHold || slices.ContainsFunc(b.Labels, func(l string) bool {
		return strings.HasPrefix(l, "workflow-template:") || strings.HasPrefix(l, "workflow-step:")
	}) || b.Status != StatusDeferred && !b.Superseded)
}

// Held is the one predicate. A closed bead is not a candidate (held = false,
// WhyClosed); a parked one is held by the park.
func Held(b Bead, w World) (Holder, bool, Why) {
	if b.Status == StatusClosed {
		return Holder{}, false, WhyClosed
	}
	if b.Status == StatusDeferred {
		return Holder{Kind: Parked}, true, WhyNone
	}
	if b.Untyped != "" {
		return Holder{}, false, WhyUntypedWait
	}
	h, ok, why := own(b, w)
	if ok {
		return h, true, WhyNone
	}
	if b.Status != StatusInProgress {
		// Not ready yet: held by an open blocker of its own or of an ancestor.
		// A dead or finished Operator is a failure to act on, so only the bead's own
		// blockers excuse it; a blocker up the parent chain does not.
		if id, found := openBlocker(b, w, why != WhyOwnerGone); found {
			return Holder{Kind: Blocker, Ref: id}, true, WhyNone
		}
	}
	return Holder{}, false, why
}

func own(b Bead, w World) (Holder, bool, Why) {
	switch {
	case b.HumanAnswerPending:
		return Holder{Kind: PlanningDecision}, true, WhyNone
	case b.Gate:
		return Holder{Kind: Gate}, true, WhyNone
	case b.NeedsUser:
		return Holder{Kind: User}, true, WhyNone
	case b.HumanHold:
		return Holder{Kind: Parked}, true, WhyNone
	case w.HasOpenChild(b.ID):
		return Holder{Kind: Children}, true, WhyNone
	case b.Queued:
		return Holder{Kind: Queue}, true, WhyNone
	case b.QuestionWaiting:
		return Holder{Kind: PlanningDecision}, true, WhyNone
	case b.OwnerState == OwnerDied || b.OwnerState == OwnerFinished:
		// Its Operator is gone: a wait it or its Manager wrote names nobody who will act.
		return Holder{}, false, WhyOwnerGone
	case b.Waits > 0, b.Until:
		return Holder{Kind: Wait}, true, WhyNone
	case liveOperatorWait(b, w):
		return Holder{Kind: Wait}, true, WhyNone
	case b.Owner == "":
		return Holder{}, false, WhyNoHolder
	case b.OwnerState != "":
		if b.OwnerState == OwnerWorking {
			return Holder{Kind: Operator, Ref: b.Owner}, true, WhyNone
		}
		return Holder{}, false, WhyOwnerNotLive
	}
	state, known := w.OwnerLive(b.Owner)
	switch {
	case !known:
		return Holder{Kind: Operator, Ref: b.Owner}, true, WhyNone
	case IsManager(b.Owner):
		if state == "working" {
			return Holder{Kind: Operator, Ref: b.Owner}, true, WhyNone
		}
		return Holder{}, false, WhyManagerIdle
	case state != "":
		return Holder{Kind: Operator, Ref: b.Owner}, true, WhyNone
	}
	return Holder{}, false, WhyOwnerNotLive
}

// liveOperatorWait: one of b's waits on an Operator names one that runs (or whose state the
// source cannot say).
func liveOperatorWait(b Bead, w World) bool {
	for _, role := range b.OperatorWaits {
		if state, known := w.OwnerLive(role); !known || state != "" {
			return true
		}
	}
	return false
}

// openBlocker finds an open blocker of b or of an ancestor (a bead blocked only
// through its parent is not ready either).
func openBlocker(b Bead, w World, ancestors bool) (string, bool) {
	seen := map[string]bool{}
	for cur, ok := b, true; ok && !seen[cur.ID]; {
		seen[cur.ID] = true
		for _, id := range cur.BlockedBy {
			if bl, found := w.Bead(id); found && BlockerHolds(bl) {
				return id, true
			}
		}
		if cur.Parent == "" || !ancestors { // the first pass is the bead's own blockers
			break
		}
		cur, ok = w.Bead(cur.Parent)
	}
	return "", false
}
