package beads

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The push counter: a bead's until: wait or parked-until target moved LATER is
// a push. Labels (br labels allow only letters, digits, - _ :):
//
//	pushed:<n>              unexplained pushes so far
//	pushed-first:<unix>     the target before the first push
//	pushed-last:<unix>      the target after the latest counted push
//	pushed-flagged          past the threshold with a reason; Commander was mailed
//
// Only until: waits and parked-until count: not ci:, main-ci, prod-tag,
// review:, operator:, user, cmdrd's own waits or a Manager custody receipt
// (Manager to close, dispatch or start: IsManagerHandoff).
const (
	PushedPrefix       = "pushed:"
	PushedFirstPrefix  = "pushed-first:"
	PushedLastPrefix   = "pushed-last:"
	PushedFlaggedLabel = "pushed-flagged"

	// PushSlack: a target moved later by no more than this is not a push.
	PushSlack = 10 * time.Minute
	// PushReasonMin is the shortest --push-reason.
	PushReasonMin = 15

	pushCommentPrefix = "push reason (to "
)

// PushState is what the labels record.
type PushState struct {
	Count       int
	First, Last int64
	Flagged     bool
}

// PushStateOf reads the push labels.
func PushStateOf(labels []string) PushState {
	var s PushState
	for _, l := range labels {
		switch {
		case l == PushedFlaggedLabel:
			s.Flagged = true
		case strings.HasPrefix(l, PushedPrefix):
			s.Count, _ = strconv.Atoi(strings.TrimPrefix(l, PushedPrefix))
		case strings.HasPrefix(l, PushedFirstPrefix):
			s.First, _ = strconv.ParseInt(strings.TrimPrefix(l, PushedFirstPrefix), 10, 64)
		case strings.HasPrefix(l, PushedLastPrefix):
			s.Last, _ = strconv.ParseInt(strings.TrimPrefix(l, PushedLastPrefix), 10, 64)
		}
	}
	return s
}

func isPushLabel(l string) bool {
	return l == PushedFlaggedLabel || strings.HasPrefix(l, PushedPrefix) || strings.HasPrefix(l, PushedFirstPrefix) || strings.HasPrefix(l, PushedLastPrefix)
}

// PushLimits: Count unexplained pushes are free (the next needs a reason);
// Days is how far the target may slide from where it started. 0 = off.
type PushLimits struct{ Count, Days int }

// PushPlan is what judging a push decided; Apply carries it out.
type PushPlan struct {
	Remove, Add []string
	Comment     string
	Counted     bool // a counted push (labels change)
	NewlyFlag   bool // it flagged the bead: mail Commander once
	N           int  // the push number when counted
}

// PrevTarget is the target a new one is compared with: the latest of the
// bead's existing until: wait targets, its parked-until and pushed-last.
func PrevTarget(labels []string) time.Time {
	var best int64
	for _, l := range labels {
		if w, ok := ParseWaitLabel(l); ok && w.Kind == WaitUntil && !IsManagerHandoff(w) && w.Until.Unix() > best {
			best = w.Until.Unix()
		}
	}
	if p := ParkedUntil(labels); p > best {
		best = p
	}
	if l := PushStateOf(labels).Last; l > best {
		best = l
	}
	if best == 0 {
		return time.Time{}
	}
	return time.Unix(best, 0)
}

// UntilTarget is the latest until: wait target among waits (Manager custody
// receipts excluded: cmdrd's and a close's own timers are no target anyone set); zero when there is none.
func UntilTarget(waits []Wait) time.Time {
	var t time.Time
	for _, w := range waits {
		if w.Kind == WaitUntil && !IsManagerHandoff(w) && w.Until.After(t) {
			t = w.Until
		}
	}
	return t
}

func normReason(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// PlanPush judges moving the bead's target to target. labels and comments are
// the bead's now. A target that is not later than the previous one by more than
// PushSlack is no push (an empty plan). An unexplained push past the limits is
// refused; a push with a reason below them is a comment only; a reason past
// them lets it through, counted and flagged.
func PlanPush(labels []string, comments []Comment, target time.Time, reason string, lim PushLimits, now time.Time) (PushPlan, error) {
	prev := PrevTarget(labels)
	if target.IsZero() || prev.IsZero() || !target.After(prev.Add(PushSlack)) {
		return PushPlan{}, nil
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		if len([]rune(reason)) < PushReasonMin {
			return PushPlan{}, fmt.Errorf("--push-reason needs %d+ characters: say why the date moves", PushReasonMin)
		}
		for _, c := range comments {
			if body, ok := strings.CutPrefix(c.Text, pushCommentPrefix); ok {
				if _, old, ok := strings.Cut(body, "): "); ok && normReason(old) == normReason(reason) {
					return PushPlan{}, fmt.Errorf("--push-reason repeats an earlier push reason on this bead verbatim: say what is different this time")
				}
			}
		}
	}
	st := PushStateOf(labels)
	first := st.First
	if first == 0 {
		first = prev.Unix()
	}
	over := (lim.Count > 0 && st.Count >= lim.Count) || (lim.Days > 0 && target.Unix()-first > int64(lim.Days)*86400)
	text := pushCommentPrefix + target.UTC().Format(time.RFC3339) + "): " + reason
	if reason == "" {
		if over {
			return PushPlan{}, fmt.Errorf("this date has already been pushed %d times (first target %s, now to %s): pushing it again needs --push-reason '<why it moves, 15+ chars>'. If the work is not coming back, finish, withdraw or park the step instead",
				st.Count, time.Unix(first, 0).UTC().Format(time.RFC3339), target.UTC().Format(time.RFC3339))
		}
		return counted(labels, st, first, target, "", false), nil
	}
	if !over {
		return PushPlan{Comment: text}, nil
	}
	p := counted(labels, st, first, target, text, !st.Flagged)
	if !st.Flagged {
		p.Add = append(p.Add, PushedFlaggedLabel)
	}
	return p, nil
}

func counted(labels []string, st PushState, first int64, target time.Time, comment string, flag bool) PushPlan {
	p := PushPlan{Counted: true, NewlyFlag: flag, N: st.Count + 1, Comment: comment}
	for _, l := range labels {
		if isPushLabel(l) && l != PushedFlaggedLabel {
			p.Remove = append(p.Remove, l)
		}
	}
	p.Add = []string{PushedPrefix + strconv.Itoa(p.N), PushedFirstPrefix + strconv.FormatInt(first, 10), PushedLastPrefix + strconv.FormatInt(target.Unix(), 10)}
	if p.Comment == "" {
		p.Comment = fmt.Sprintf("pushed to %s (push %d)", target.UTC().Format(time.RFC3339), p.N)
	}
	return p
}

// ApplyPush writes the plan's labels and comment.
func ApplyPush(ctx context.Context, c Client, dir, id string, p PushPlan) error {
	for _, l := range p.Remove {
		if err := c.SetLabel(ctx, dir, id, l, false); err != nil {
			return err
		}
	}
	for _, l := range p.Add {
		if err := c.SetLabel(ctx, dir, id, l, true); err != nil {
			return err
		}
	}
	if p.Comment != "" {
		return c.AddComment(ctx, dir, id, p.Comment)
	}
	return nil
}

// ClearPush drops every push label of the bead (it closed, or Commander reset
// the counter). It reports whether any was there.
func ClearPush(ctx context.Context, c Client, dir, id string) (bool, error) {
	b, err := c.Show(ctx, dir, id)
	if err != nil || b == nil {
		return false, err
	}
	any := false
	for _, l := range slices.Clone(b.Labels) {
		if isPushLabel(l) {
			any = true
			if err := c.SetLabel(ctx, dir, id, l, false); err != nil {
				return any, err
			}
		}
	}
	return any, nil
}
