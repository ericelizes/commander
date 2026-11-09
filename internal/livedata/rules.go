package livedata

import (
	"slices"
	"strings"
)

// planRules are the rules a plan's state keeps whoever wrote it: what can only
// be true of a step once the steps it waits on are done, one card per wait,
// one gate per decision, a stage that is done when its steps are. Each
// violation is certain (none is advisory): the board or the diagram reads
// wrong while it stands. CheckDiagram includes them, so `cmdr graph check`,
// the doors that refuse writes and cmdrd's watcher all apply the same rules.
func planRules(ws SnapWorkstream, scope []SnapStep) []DiagramFinding {
	all := flattenSteps(ws.Steps)
	byID := make(map[string]SnapStep, len(all))
	parent := map[string]string{}
	for _, s := range ws.Steps {
		byID[s.BeadID] = s
	}
	var index func([]SnapStep)
	index = func(level []SnapStep) {
		for _, s := range level {
			byID[s.BeadID] = s
			for _, c := range s.Children {
				parent[c.BeadID] = s.BeadID
			}
			index(s.Children)
		}
	}
	index(ws.Steps)
	// reached: nothing the step waits on, itself or through a step above it, is still open.
	reached := func(s SnapStep) bool {
		if unfinishedBlocker(s, byID) {
			return false
		}
		for up := parent[s.BeadID]; up != ""; up = parent[up] {
			if p, ok := byID[up]; ok && unfinishedBlocker(p, byID) {
				return false
			}
		}
		return true
	}
	live := func(s SnapStep) bool { return !s.Superseded && !parked(s) && !stepClosed(s) }
	set := func(id string) string { return "cmdr internal-set-objective --project " + ws.Project + " --bead " + id }

	var out []DiagramFinding
	add := func(check string, s SnapStep, why, fix string) {
		out = append(out, DiagramFinding{Check: check, Bead: s.BeadID, Title: plainTitle(s.Title), Why: why, Fix: fix})
	}
	inScope := flattenSteps(scope)
	for _, s := range inScope {
		if s.Superseded || parked(s) {
			continue
		}
		openKids := slices.ContainsFunc(s.Children, live)
		switch {
		case stepClosed(s):
			for _, b := range s.BlockedBy {
				if bs, ok := byID[b]; ok && b != s.BeadID && live(bs) {
					add("done-early", s, "it is done while "+b+", which it waits on, is still open, so the plan reads as finished out of order",
						"if "+b+" is no longer needed, drop it (supersede it); if the wait was never real, cmdr internal-dep rm "+s.BeadID+" "+b+"; otherwise reopen "+s.BeadID)
					break
				}
			}
			continue
		case stageDone(s, openKids) && OpenBlockerOf(ws, s.BeadID) == "":
			add("stage-done", s, "every step under it is done but it is still open, so it reads as work in progress with nothing left in it",
				"cmdr internal-close-objective --project "+ws.Project+" --bead "+s.BeadID+" (or add the step that is still missing under it)")
			continue
		}
		if openKids || reached(s) {
			continue
		}
		switch {
		case startedItself(s):
			add("started-early", s, "it is claimed or in progress while a step it waits on is still open, so it reads as where the work is before it can start",
				set(s.BeadID)+" --state not-started --owner none (or, if it really does not wait, cmdr internal-dep rm the blocker)")
		case onUser(s) && !s.Gate: // a gate's card may be written ahead; it reads Not reached until reached
			add("asked-early", s, "it waits on the user while a step it waits on is still open, so it asks before it can be answered",
				set(s.BeadID)+" --waiting none --state not-started; ask when the steps before it are done")
		}
	}

	// One card per review: a PR's review is waited on by one open step. A
	// stage carrying its own step's wait, or two steps on the same PR, draw
	// the same wait twice.
	inScopeIDs := map[string]bool{}
	for _, s := range inScope {
		inScopeIDs[s.BeadID] = true
	}
	byPR := map[string][]SnapStep{}
	var prs []string
	for _, s := range all {
		if !live(s) || s.Review == "" || s.Review == "?" || !(s.ReviewFact != nil || strings.Contains(s.Waiting, "review")) {
			continue
		}
		if len(byPR[s.Review]) == 0 {
			prs = append(prs, s.Review)
		}
		byPR[s.Review] = append(byPR[s.Review], s)
	}
	for _, pr := range prs {
		ss := byPR[pr]
		if len(ss) < 2 || !slices.ContainsFunc(ss, func(s SnapStep) bool { return inScopeIDs[s.BeadID] }) {
			continue
		}
		ids := make([]string, len(ss))
		for i, s := range ss {
			ids[i] = s.BeadID
		}
		add("duplicate-wait", ss[0], "the review of PR #"+pr+" is waited on by "+strings.Join(ids, ", ")+", so one wait draws as several cards",
			"keep the wait on the one step it belongs to (the review gate, or the step that is the PR) and clear it on the others: "+set("<id>")+" --waiting none --review none")
	}

	// One gate per decision: two open-or-recent gates with the same title under
	// the same parent read as the same checkpoint both passed and pending.
	seen := map[string]SnapStep{}
	for _, s := range all {
		if !s.Gate || s.Superseded {
			continue
		}
		key := parent[s.BeadID] + "\x00" + strings.ToLower(strings.TrimSpace(plainTitle(s.Title)))
		first, dup := seen[key]
		if !dup {
			seen[key] = s
			continue
		}
		if (!inScopeIDs[s.BeadID] && !inScopeIDs[first.BeadID]) || (stepClosed(s) && stepClosed(first)) {
			continue
		}
		add("duplicate-gate", s, "it has the same title as "+first.BeadID+" under the same step, so one checkpoint reads as both passed and pending",
			"if it replaces the old one, give it its own title (cmdr internal-rename-objective --project "+ws.Project+" --bead "+s.BeadID+" --title \"<what changed>, approved\"); if it repeats it, supersede it")
	}
	return out
}

// startedItself: the bead itself says it started (status in progress, or a claim
// on it). s.State alone is no such word: an open stage with done children rolls
// up to in-progress (DerivedLive), and the diagram draws that bead Not started.
func startedItself(s SnapStep) bool {
	return s.State == "in-progress" && (s.Status == "in_progress" || s.Owner != "")
}

// stageDone: an ordinary container whose steps are all done is still open.
// Never a gate (closing it would pass the check without its approver), never
// a bead with an open card for the user (the user answers it), never one with
// no live children (nothing under it was done; superseded ones do not count).
// Its own unpassed checks still represent work, even after its children finish.
func stageDone(s SnapStep, openKids bool) bool {
	if openKids || s.Gate || onUser(s) || s.Decision != nil || unpassedGate(s) != nil {
		return false
	}
	return slices.ContainsFunc(s.Children, func(c SnapStep) bool { return !c.Superseded && stepClosed(c) }) &&
		!slices.ContainsFunc(s.Children, func(c SnapStep) bool { return !c.Superseded && !stepClosed(c) })
}

// unpassedGate is the first check s must still pass before it closes.
func unpassedGate(s SnapStep) *SnapGate {
	for i := range s.Gates {
		if s.Gates[i].Status != "passed" {
			return &s.Gates[i]
		}
	}
	return nil
}

// OpenBlockerOf is the first step id waits on, itself or through a step above
// it, that is still open (parked ones hold nothing): until it is done, id
// cannot start, ask the user or be where the work is. "" when id is reached
// (or not in ws). Steps in done count as done (one a command is closing).
func OpenBlockerOf(ws SnapWorkstream, id string, done ...string) string {
	byID := map[string]SnapStep{}
	parent := map[string]string{}
	var index func([]SnapStep)
	index = func(level []SnapStep) {
		for _, s := range level {
			byID[s.BeadID] = s
			for _, c := range s.Children {
				parent[c.BeadID] = s.BeadID
			}
			index(s.Children)
		}
	}
	index(ws.Steps)
	if _, ok := byID[id]; !ok {
		return ""
	}
	for at := id; at != ""; at = parent[at] {
		s := byID[at]
		for _, b := range s.BlockedBy {
			if bs, ok := byID[b]; ok && b != at && !stepClosed(bs) && !parked(bs) && !bs.Superseded && !slices.Contains(done, b) {
				return b
			}
		}
	}
	return ""
}

// StepIn is the workstream of project that holds step id, and the step.
func StepIn(snap *Snapshot, project, id string) (*SnapWorkstream, *SnapStep) {
	if snap == nil {
		return nil, nil
	}
	for i := range snap.Workstreams {
		w := &snap.Workstreams[i]
		if w.Project != project {
			continue
		}
		if s := findSnapStep(w.Steps, id); s != nil {
			return w, s
		}
	}
	return nil, nil
}
