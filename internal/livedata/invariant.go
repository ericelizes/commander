package livedata

import (
	"slices"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/store"
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
	return OpenBeadInvariantAt(rows, live, time.Time{})
}

// OpenBeadInvariantAt is OpenBeadInvariant judged at now: a Manager handoff wait overdue past
// beads.HandoffWindow holds nothing (invariant I1). The snapshot build passes its load time.
func OpenBeadInvariantAt(rows []ObjectiveRow, live LiveAgent, now time.Time) []Violation {
	w := newRowWorld(rows, live)
	w.now = now
	byID := w.rows
	var out []Violation
	for _, r := range rows {
		if r.Status == "closed" || r.Status == beads.StatusDeferred {
			continue
		}
		if _, held, _ := holding.Held(rowBead(r, now), w); held {
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

// rowWorld is the holding.World of a list of rows.
type rowWorld struct {
	rows      map[string]ObjectiveRow
	openChild map[string]bool
	live      LiveAgent
	now       time.Time
}

func newRowWorld(rows []ObjectiveRow, live LiveAgent) *rowWorld {
	w := &rowWorld{rows: make(map[string]ObjectiveRow, len(rows)), openChild: map[string]bool{}, live: live}
	for _, r := range rows {
		w.rows[r.BeadID] = r
	}
	for _, r := range rows {
		if r.Status != "closed" && r.Status != beads.StatusDeferred && r.ParentBeadID != "" {
			w.openChild[r.ParentBeadID] = true
		}
	}
	return w
}

func (w *rowWorld) Bead(id string) (holding.Bead, bool) {
	r, ok := w.rows[id]
	return rowBead(r, w.now), ok
}

func (w *rowWorld) HasOpenChild(id string) bool { return w.openChild[id] }

func (w *rowWorld) OwnerLive(owner string) (string, bool) {
	if w.live == nil {
		return "", false
	}
	return w.live(owner), true
}

// rowBead is a row as the one predicate reads it at now (zero: no wait has expired, as a
// snapshot's text waits carry no deadline to judge).
func rowBead(r ObjectiveRow, now time.Time) holding.Bead {
	var labels []string
	if r.WorkflowCheck != nil {
		labels = slices.Clone(r.WorkflowCheck.Bead.Labels)
	}
	waits := 0
	for _, w := range r.Waits {
		if now.IsZero() || beads.WaitHolds(w, now) {
			waits++
		}
	}
	return holding.Bead{
		Labels: labels, Queued: r.ExecutionReady && r.PlanningCommander != "", QuestionWaiting: r.QuestionWaiting, HumanAnswerPending: r.HumanAnswerPending,
		ID: r.BeadID, Parent: r.ParentBeadID, Status: r.Status, Gate: r.Gate,
		NeedsUser: r.State == beads.StateNeedsUser, Superseded: r.Superseded,
		Owner: r.Owner, OwnerState: r.OwnerState, Waits: waits, Untyped: untypedWait(r),
		Until: r.Until != "", BlockedBy: r.BlockedBy,
	}
}

// SnapshotViolations is OpenBeadInvariant over a snapshot (what `cmdr graph
// check` reads, so it needs no store): every step becomes a row, a workstream
// bead too. A snapshot carries a step's recorded waits as text, which is all
// the rule needs.
func SnapshotViolations(s Snapshot) []Violation {
	var rows []ObjectiveRow
	for _, ws := range s.Workstreams {
		rows = append(rows, ObjectiveRow{QuestionWaiting: ws.QuestionWaiting, HumanAnswerPending: ws.HumanAnswerPending, ExecutionReady: ws.ExecutionReady, PlanningCommander: ws.PlanningCommander, BlockedBy: ws.BlockedBy, BeadID: ws.BeadID, Title: ws.Title, ProjectName: ws.Project, Status: ws.Status, State: ws.State, Owner: ws.Owner})
		rows = append(rows, workstreamRows(ws)...)
	}
	return OpenBeadInvariant(rows, func(owner string) string { return s.Agents[owner].State })
}

// workstreamRows lists a workstream's gate nodes and steps as rows (not the
// workstream bead itself). Gate beads are blockers too, so a bead behind one
// resolves; a snapshot carries a step's recorded waits as text, which is all the
// rule needs.
func workstreamRows(ws SnapWorkstream) []ObjectiveRow {
	var rows []ObjectiveRow
	for _, g := range ws.GateNodes {
		status := "open"
		if g.Status == "passed" {
			status = "closed"
		}
		rows = append(rows, ObjectiveRow{BeadID: g.BeadID, ParentBeadID: ws.BeadID, Title: g.Name, ProjectName: ws.Project, Status: status, Gate: true})
	}
	var walk func(parent string, steps []SnapStep)
	walk = func(parent string, steps []SnapStep) {
		for _, st := range steps {
			row := ObjectiveRow{
				QuestionWaiting: st.QuestionWaiting, HumanAnswerPending: st.HumanAnswerPending, ExecutionReady: st.ExecutionReady, PlanningCommander: st.PlanningCommander,
				BeadID: st.BeadID, ParentBeadID: parent, Title: st.Title, ProjectName: ws.Project, Status: st.Status,
				WorkflowCheck: st.WorkflowCheck, State: st.State, Owner: st.Owner, OwnerState: st.OwnerState, BlockedBy: st.BlockedBy, Until: st.Until, Gate: st.Gate, Superseded: st.Superseded,
				Owns: st.ManagerRole != "",
			}
			if st.Waiting != "" {
				row.Waits = []beads.Wait{{}}
			}
			rows = append(rows, row)
			walk(st.BeadID, st.Children)
		}
	}
	walk(ws.BeadID, ws.Steps)
	return rows
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
