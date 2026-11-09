package beads

import (
	"fmt"
	"strconv"
	"strings"
)

// A Manager's new gate, ask, review step, long wait or park is a proposal
// until Commander accepts it: label proposed:<kind>:<unix>[:<state before>].
// A proposed ask or user gate is not in NEEDS YOU (livedata); accepting drops
// the label, overruling undoes the write.
const ProposedPrefix = "proposed:"

// Proposal kinds.
const (
	ProposedAsk    = "ask"    // a new needs-user decision card
	ProposedGate   = "gate"   // a new gate after the workflow was approved
	ProposedReview = "review" // a new review or re-review step after the workflow was approved
	ProposedWait   = "wait"   // an until: wait further out than the review threshold
	ProposedPark   = "park"   // a park with no time or one further out than the threshold
)

// Proposal is a bead's open proposal, read from its label.
type Proposal struct {
	Kind  string
	At    int64  // unix s it was proposed
	Prev  string // ask: the step state before the ask
	Label string // the label itself
}

// ProposedLabel is the label that marks a proposal.
func ProposedLabel(kind string, at int64, prev string) string {
	l := fmt.Sprintf("%s%s:%d", ProposedPrefix, kind, at)
	if prev != "" {
		l += ":" + prev
	}
	return l
}

// ProposalOf is the bead's open proposal; ok false when it has none.
func ProposalOf(labels []string) (Proposal, bool) {
	for _, l := range labels {
		v, ok := strings.CutPrefix(l, ProposedPrefix)
		if !ok {
			continue
		}
		parts := strings.SplitN(v, ":", 3)
		p := Proposal{Kind: parts[0], Label: l}
		if len(parts) > 1 {
			p.At, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if len(parts) > 2 {
			p.Prev = parts[2]
		}
		return p, true
	}
	return Proposal{}, false
}
