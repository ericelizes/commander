package beads

import (
	"context"
	"fmt"
	"strings"
)

// HoldLabel on a task bead (or any ancestor up to its workstream) tells
// Operators to stop shipping: internal-ship refuses while it is set. Managers
// and Commander set it with `internal-set-objective --hold [reason]`.
const HoldLabel = "hold"

// holdCommentPrefix starts the comment that records who held a bead and why.
const holdCommentPrefix = "hold by "

// HoldComment is the comment text recorded when who holds a bead.
func HoldComment(who, reason string) string {
	return holdCommentPrefix + who + ": " + reason
}

// Hold is the first hold found on a bead or one of its ancestors.
type Hold struct {
	Bead   string // the bead carrying the label
	Who    string
	Reason string
}

func (h Hold) String() string {
	return fmt.Sprintf("held by %s: %s", h.Who, h.Reason)
}

// FindHold walks id and its ancestors (real parent edges, fresh reads) and
// returns the nearest hold, or nil when none is set. A read error is returned
// as such: the caller must treat a hold it cannot check as held.
func FindHold(ctx context.Context, c Client, dir, id string) (*Hold, error) {
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		seen[cur] = true
		b, err := c.Show(ctx, dir, cur)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, fmt.Errorf("bead %s not found", cur)
		}
		d, err := FreshDetails(ctx, c, dir, []string{cur})
		if err != nil {
			return nil, err
		}
		for _, l := range b.Labels {
			if l == HoldLabel {
				return &Hold{Bead: cur, Who: holdWho(d[cur].Comments), Reason: HoldReason(d[cur].Comments)}, nil
			}
		}
		cur = d[cur].Parent
	}
	return nil, nil
}

func lastHoldComment(comments []Comment) (Comment, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		if strings.HasPrefix(comments[i].Text, holdCommentPrefix) {
			return comments[i], true
		}
	}
	return Comment{}, false
}

func holdWho(comments []Comment) string {
	c, ok := lastHoldComment(comments)
	if !ok {
		return "unknown"
	}
	who, _, _ := strings.Cut(strings.TrimPrefix(c.Text, holdCommentPrefix), ":")
	return strings.TrimSpace(who)
}

// HoldReason is the reason the latest hold comment records.
func HoldReason(comments []Comment) string {
	c, ok := lastHoldComment(comments)
	if !ok {
		return "no reason recorded"
	}
	_, reason, _ := strings.Cut(c.Text, ":")
	if reason = strings.TrimSpace(reason); reason != "" {
		return reason
	}
	return "no reason recorded"
}
