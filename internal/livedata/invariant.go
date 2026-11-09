package livedata

import (
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"strings"
)

// Violation is an open bead that breaks the open-bead invariant: it has no
// live agent, no recorded wait, and is not the user's. That is broken data, not a
// status.
type Violation struct {
	BeadID      string `json:"bead_id"`
	Title       string `json:"title"`
	Project     string `json:"project"`
	Workstream  string `json:"workstream"` // the top-level bead the row sits under
	ManagerRole string `json:"manager_role"`
	// Why is set when the bead is broken for what it carries, not what it lacks.
	Why string `json:"why,omitempty"`
}

// OpenBeadInvariant lists the open rows that are neither (a) on a live agent
// (an owner whose Operator is working, or whose owner role live reports; an
// Operator that died or exited holds nothing, whatever wait sits on the row),
// (b) a recorded wait (waiting:/until: labels), nor (c) the user's (an open gate,
// or a needs-user step). A row is also fine when it is a parent with an open
// child (the child answers for it), or is not in progress and waits behind at
// least one open blocker (not closed, parked or superseded; a blocker the rows do not
// list blocks nothing, so the bead is still checked).
// live may be nil (then only the Operator state speaks for an owner).
func OpenBeadInvariant(rows []ObjectiveRow, live LiveAgent) []Violation {
	byID := make(map[string]ObjectiveRow, len(rows))
	hasOpenChild := map[string]bool{}
	for _, r := range rows {
		byID[r.BeadID] = r
	}
	for _, r := range rows {
		if r.Status != "closed" && r.Status != beads.StatusDeferred && r.ParentBeadID != "" {
			hasOpenChild[r.ParentBeadID] = true
		}
	}
	memo := map[string]bool{}
	var fine func(id string) bool
	fine = func(id string) bool {
		if v, ok := memo[id]; ok {
			return v
		}
		r, ok := byID[id]
		if !ok || r.Status == "closed" {
			return false
		}
		if r.Status == beads.StatusDeferred {
			memo[id] = true // parked: out of play, covered
			return true
		}
		if untypedWait(r) != "" {
			memo[id] = false // nothing but the clock clears it, so it covers nothing
			return false
		}
		if ownBead(r, live, hasOpenChild[id]) {
			memo[id] = true
			return true
		}
		if r.Status != "in_progress" {
			for _, b := range r.BlockedBy {
				if blocker, ok := byID[b]; ok && rowBlocks(blocker) {
					memo[id] = true // waiting behind an open blocker: not ready, nothing to own yet
					return true
				}
			}
		}
		memo[id] = false
		return false
	}
	var out []Violation
	for _, r := range rows {
		if r.Status == "closed" || r.Status == beads.StatusDeferred || fine(r.BeadID) {
			continue
		}
		// The nearest owner: a nested workstream's Manager fixes its own beads.
		owner := ownerIn(byID, r.BeadID)
		v := Violation{
			BeadID: r.BeadID, Title: r.Title, Project: r.ProjectName,
			Workstream: owner, ManagerRole: store.WorkstreamManagerRole(owner),
		}
		if label := untypedWait(r); label != "" {
			v.Why = "untyped wait \"" + label + "\": rewrite it as a typed wait (review:, ci:, prod-tag:, operator:) or needs-user with a card"
		}
		out = append(out, v)
	}
	return out
}

// untypedWait is the label of the row's first slug-labelled time wait, or "".
func untypedWait(r ObjectiveRow) string {
	for _, w := range r.Waits {
		if w.Untyped() {
			return w.Ref
		}
	}
	return ""
}

// rowBlocks reports whether a blocker still holds its dependant back: it is open
// and in play (a closed, parked or superseded blocker may never finish or is done).
func rowBlocks(r ObjectiveRow) bool {
	return r.Status != "closed" && r.Status != beads.StatusDeferred && !r.Superseded
}

// ownBead is the invariant on the row's own markers: an agent, a wait, the user,
// or an open child.
func ownBead(r ObjectiveRow, live LiveAgent, openChild bool) bool {
	switch {
	case r.Gate, r.State == beads.StateNeedsUser, openChild:
		return true
	case r.OwnerState == OwnerDied || r.OwnerState == OwnerFinished:
		// Its Operator is gone: a wait it or its Manager wrote names nobody who
		// will act on it.
		return false
	case len(r.Waits) > 0, r.Until != "":
		return true
	case r.Owner == "":
		return false
	case r.OwnerState != "":
		return r.OwnerState == OwnerWorking
	case live != nil:
		// A Manager (or Commander) owns everything in its workstream, so its name on
		// a step proves nothing between turns: only a turn in progress does. Without
		// one, the step needs a recorded wait, an Operator or the user.
		if strings.HasPrefix(r.Owner, "manager-") || r.Owner == "commander" {
			return live(r.Owner) == "working"
		}
		return live(r.Owner) != ""
	}
	return true
}

// SnapshotViolations is OpenBeadInvariant over a snapshot (what `cmdr graph
// check` reads, so it needs no store): every step becomes a row, a workstream
// bead too. A snapshot carries a step's recorded waits as text, which is all
// the rule needs.
func SnapshotViolations(s Snapshot) []Violation {
	var rows []ObjectiveRow
	var walk func(project, parent string, steps []SnapStep)
	walk = func(project, parent string, steps []SnapStep) {
		for _, st := range steps {
			row := ObjectiveRow{
				BeadID: st.BeadID, ParentBeadID: parent, Title: st.Title, ProjectName: project, Status: st.Status,
				State: st.State, Owner: st.Owner, OwnerState: st.OwnerState, BlockedBy: st.BlockedBy, Until: st.Until, Gate: st.Gate, Superseded: st.Superseded,
				Owns: st.ManagerRole != "",
			}
			if st.Waiting != "" {
				row.Waits = []beads.Wait{{}}
			}
			rows = append(rows, row)
			walk(project, st.BeadID, st.Children)
		}
	}
	for _, ws := range s.Workstreams {
		// Gate beads are blockers too (not steps): list them so a bead behind one resolves.
		for _, g := range ws.GateNodes {
			status := "open"
			if g.Status == "passed" {
				status = "closed"
			}
			rows = append(rows, ObjectiveRow{BeadID: g.BeadID, ParentBeadID: ws.BeadID, Title: g.Name, ProjectName: ws.Project, Status: status, Gate: true})
		}
		rows = append(rows, ObjectiveRow{BeadID: ws.BeadID, Title: ws.Title, ProjectName: ws.Project, Status: ws.Status, State: ws.State, Owner: ws.Owner})
		walk(ws.Project, ws.BeadID, ws.Steps)
	}
	return OpenBeadInvariant(rows, func(owner string) string { return s.Agents[owner].State })
}

// WithEdits is the snapshot as it reads once edits land: each edit changes a
// copy of the step with its id, and Broken is derived again (SnapshotViolations)
// so Board on the result says what the row would say.
func WithEdits(s Snapshot, edits map[string]func(*SnapStep)) Snapshot {
	out := s
	out.Display = nil
	out.Workstreams = make([]SnapWorkstream, len(s.Workstreams))
	for i, ws := range s.Workstreams {
		ws.Steps, _ = editSteps(ws.Steps, edits)
		out.Workstreams[i] = ws
	}
	out.Broken = SnapshotViolations(out)
	markBroken(out.Workstreams, out.Broken)
	return out
}

// UnheldAfter lists the steps a write leaves unheld: open, with no live agent,
// no recorded wait and not the user's once edits land, and not so before. They
// are the steps the board then marks Broken and reads State missing for. Only
// workstreams with an edited step are looked at.
func UnheldAfter(s Snapshot, edits map[string]func(*SnapStep)) []Violation {
	before := map[string]bool{}
	for _, v := range SnapshotViolations(s) {
		before[v.BeadID] = true
	}
	touched := map[string]bool{}
	for _, ws := range s.Workstreams {
		if _, hit := editSteps(ws.Steps, edits); hit {
			touched[ws.BeadID] = true
		}
	}
	var out []Violation
	for _, v := range WithEdits(s, edits).Broken {
		// The workstream bead is not a step: the board reads only its steps.
		if touched[v.Workstream] && v.BeadID != v.Workstream && !before[v.BeadID] {
			out = append(out, v)
		}
	}
	return out
}

// editSteps copies steps with edits applied, reporting whether any matched.
func editSteps(steps []SnapStep, edits map[string]func(*SnapStep)) ([]SnapStep, bool) {
	if steps == nil {
		return nil, false
	}
	out := make([]SnapStep, len(steps))
	hit := false
	for i, st := range steps {
		var h bool
		st.Children, h = editSteps(st.Children, edits)
		if f, ok := edits[st.BeadID]; ok {
			f(&st)
			h = true
		}
		hit = hit || h
		out[i] = st
	}
	return out, hit
}
