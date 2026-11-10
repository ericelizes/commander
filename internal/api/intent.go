package api

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/holdread"
	"github.com/example-user/commander/internal/livedata"
)

// intentPlan is what an intent's simulation says the write leaves: who holds each bead of its
// closure, and each changed bead's status and labels (Plan.After, laid on the board at once).
type intentPlan struct {
	holders map[string]string
	after   map[string]livedata.StepPatch
}

// intentMaxClosure bounds the fresh reads one intent makes; a larger closure is not judged.
const intentMaxClosure = 60

// planIntent is the door's Plan (guarded holding layer, step 6): it reads the closure of the
// intent's bead FRESH, simulates the ops on it with the one predicate (holding.Held) and refuses a
// write that leaves a bead with no holder for an agent. The user's write (no role) is never refused:
// the ops gain the Manager's "Manager to start" receipt on what would be unheld, unless the user's
// leave_unheld (logged) says to leave it so. It returns the ops ordered holder-first (I3) and what holds each
// bead of the closure afterwards. A closure it cannot read or simulate is not judged (fail open,
// logged to door-errors.jsonl), as every door check does.
func (s *Server) planIntent(ctx context.Context, dir, role string, in beads.DoorIntent, ops [][]string) ([][]string, *intentPlan, error) {
	if in.Bead == "" || !doorChecksOn() {
		return ops, nil, nil
	}
	edits, ok := holdread.EditsFromOps(ops)
	if !ok {
		logDoorFailOpen(dir, in.Bead, "intent", fmt.Errorf("ops not simulated, not judged"))
		return ops, nil, nil
	}
	bc := s.opts.Beads
	if bc == nil {
		bc = beads.New()
	}
	g, err := holdread.Load(ctx, bc, dir, in.Bead, s.opts.LiveOwner, intentMaxClosure)
	if err != nil {
		logDoorFailOpen(dir, in.Bead, "intent", fmt.Errorf("closure not read (%v), not judged", err))
		return ops, nil, nil
	}
	after := g.With(edits)
	plan := &intentPlan{holders: map[string]string{}, after: map[string]livedata.StepPatch{}}
	for _, id := range g.Closure(in.Bead) {
		b, ok := after.Beads[id]
		if !ok {
			continue
		}
		h, held, _ := holding.Held(b, after)
		if held {
			plan.holders[id] = string(h.Kind)
		} else if b.Status != holding.StatusClosed {
			plan.holders[id] = ""
		}
		if was := g.Beads[id]; was.Status != b.Status || !slices.Equal(was.Labels, b.Labels) {
			plan.after[id] = livedata.StepPatch{Status: b.Status, Labels: slices.Clone(b.Labels)}
		}
	}
	left := holding.NewlyUnheld(g, in.Bead, edits)
	if len(left) == 0 {
		return holdread.HolderFirst(ops), plan, nil
	}
	var names []string
	for _, v := range left {
		names = append(names, fmt.Sprintf("%s %q (%s)", v.ID, v.Title, v.Why))
	}
	sort.Strings(names)
	verb := in.Verb
	if verb == "" {
		verb = "this write"
	}
	reason := strings.TrimSpace(in.LeaveUnheld)
	switch {
	case reason != "" && role != "":
		return nil, nil, fmt.Errorf("refused: leave_unheld is the user's, not %s's: name the next holder instead", role)
	case reason != "" && utf8.RuneCountInString(reason) >= 10:
		appendDoorLog("leave-unheld.jsonl", map[string]any{
			"time": time.Now().UTC().Format(time.RFC3339), "dir": dir, "bead": in.Bead, "verb": verb, "left": names, "reason": reason,
		})
		return holdread.HolderFirst(ops), plan, nil
	case reason != "":
		return nil, nil, fmt.Errorf("refused: leave_unheld needs a reason of 10 characters or more (it is logged)")
	case role == "":
		// The user's write is never refused: it carries the Manager's "Manager to start" receipt
		// onto every bead it would leave with no holder, written first (custody, I3).
		label := beads.StartReceiptLabel(time.Now())
		for _, v := range left {
			ops = append(ops, []string{"update", v.ID, "--add-label", label})
			p := plan.after[v.ID]
			if p.Status == "" {
				p.Status = after.Beads[v.ID].Status
				p.Labels = slices.Clone(after.Beads[v.ID].Labels)
			}
			p.Labels = append(p.Labels, label)
			plan.after[v.ID] = p
			plan.holders[v.ID] = string(holding.Wait)
		}
		return holdread.HolderFirst(ops), plan, nil
	}
	return nil, nil, fmt.Errorf("refused: %s would leave %s with no holder. Hold it in the same write: a running Operator (owner:operator-...), a typed wait (waiting:...), an open blocker, an open child or the user's card; a Manager's name is not a holder", verb, strings.Join(names, ", "))
}
