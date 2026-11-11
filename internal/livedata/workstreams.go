package livedata

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// NestedObjective is an objective with its depth under its workstream (0 = the
// workstream itself).
type NestedObjective struct {
	Row   ObjectiveRow
	Depth int
}

// NestObjectives orders objectives parent-first, children directly beneath
// their parent. An objective whose parent isn't in the list (closed, or in
// another project) is treated as a root so it never disappears.
func NestObjectives(rows []ObjectiveRow) []NestedObjective {
	present := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.BeadID != "" {
			present[r.BeadID] = true
		}
	}
	children := make(map[string][]ObjectiveRow)
	var roots []ObjectiveRow
	for _, r := range rows {
		switch {
		case r.ParentBeadID != "" && present[r.ParentBeadID]:
			children[r.ParentBeadID] = append(children[r.ParentBeadID], r)
		case r.Task || r.KindTask:
			// An Operator task bead (any kind:task label, also beside kind:objective
			// after convert then --no-workstream) is never a workstream row: with no
			// parent in the list it is not drawn, since no Manager may own it.
		case r.Track:
			// A nested workstream never becomes a row: without its parent in the
			// list (closed, or a dotted-id guess that missed) it is not drawn.
		case r.Status == "closed" || r.Superseded:
			// A closed (or superseded) bead with no parent in the list, such as
			// a promoted or moved step, is a finished workstream: not drawn.
		default:
			roots = append(roots, r)
		}
	}
	// Siblings read by priority; unranked ones follow in the order they were added (.1, .2, …).
	for parent := range children {
		kids := children[parent]
		sort.SliceStable(kids, func(i, j int) bool {
			a, b := kids[i], kids[j]
			if (a.Rank > 0) != (b.Rank > 0) {
				return a.Rank > 0
			}
			if a.Rank != b.Rank {
				return a.Rank < b.Rank
			}
			return childSeq(a.BeadID) < childSeq(b.BeadID)
		})
	}
	out := make([]NestedObjective, 0, len(rows))
	var walk func(r ObjectiveRow, depth int)
	walk = func(r ObjectiveRow, depth int) {
		out = append(out, NestedObjective{Row: r, Depth: depth})
		for _, child := range children[r.BeadID] {
			walk(child, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

// childSeq returns the trailing ".N" number of a child bead id, or 0.
func childSeq(id string) int {
	i := strings.LastIndex(id, ".")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(id[i+1:])
	return n
}

// Workstream is one top-level objective plus its nested steps (flat, plan order).
type Workstream struct {
	Project store.Project
	Root    ObjectiveRow
	Steps   []NestedObjective
}

// WorkstreamsInOrder flattens every project's open workstreams into one list
// in the user's priority order (workstream_ranks); unranked ones follow in
// creation order.
func WorkstreamsInOrder(projects []store.Project, objectivesFor func(projectID int64) []ObjectiveRow, ranks map[store.WorkstreamKey]int) []Workstream {
	var all []Workstream
	for _, p := range projects {
		start := len(all)
		for _, n := range NestObjectives(objectivesFor(p.ID)) {
			if n.Depth == 0 {
				all = append(all, Workstream{Project: p, Root: n.Row})
			} else if len(all) > start {
				last := &all[len(all)-1]
				last.Steps = append(last.Steps, n)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		ra, aRanked := ranks[store.WorkstreamKey{ProjectID: a.Project.ID, BeadID: a.Root.BeadID}]
		rb, bRanked := ranks[store.WorkstreamKey{ProjectID: b.Project.ID, BeadID: b.Root.BeadID}]
		if aRanked != bRanked {
			return aRanked // ranked first; new workstreams sink to the bottom
		}
		if aRanked && ra != rb {
			return ra < rb
		}
		if a.Root.CreatedAt != b.Root.CreatedAt {
			return a.Root.CreatedAt < b.Root.CreatedAt
		}
		if a.Project.ID != b.Project.ID {
			return a.Project.ID < b.Project.ID
		}
		return a.Root.BeadID < b.Root.BeadID
	})
	return all
}

// ObjectivesOf returns the objectives of one project.
func ObjectivesOf(data Data, projectID int64) []ObjectiveRow {
	var rows []ObjectiveRow
	for _, o := range data.Objectives {
		if o.ProjectID == projectID {
			o.Rank = data.BeadRanks[store.WorkstreamKey{ProjectID: projectID, BeadID: o.BeadID}]
			rows = append(rows, o)
		}
	}
	return rows
}

// Workstreams is WorkstreamsInOrder over a loaded Data.
func Workstreams(data Data) []Workstream {
	if data.workstreams != nil {
		return data.workstreams
	}
	return WorkstreamsInOrder(data.Projects, func(id int64) []ObjectiveRow { return ObjectivesOf(data, id) }, data.Ranks)
}

// NeedsYou is one open step waiting on the user to verify or decide.
type NeedsYou struct {
	PlanRevision int64           `json:"plan_revision,omitempty"`
	StaleWhy     string          `json:"stale_why,omitempty"` // derived by Board for its rows
	Project      string          `json:"project"`
	BeadID       string          `json:"bead_id"`
	Title        string          `json:"title"`
	Workstream   string          `json:"workstream"`
	Rank         int             `json:"rank"`
	Ask          string          `json:"ask,omitempty"`      // what the user must do, verb first
	Note         string          `json:"note,omitempty"`     // the full note: commands, links, context
	Decision     *store.Decision `json:"decision,omitempty"` // the decision card, when the Manager wrote one
	Since        int64           `json:"since,omitempty"`    // unix seconds the ask was raised; 0 = unknown
	// SpecHash is the card's bead's spec fingerprint now (beads.SpecHash);
	// Basis is what the card was raised against. The board compares them.
	PushCount int          `json:"push_count,omitempty"`
	PushFirst int64        `json:"push_first,omitempty"`
	SpecHash  string       `json:"spec_hash,omitempty"`
	Basis     *store.Basis `json:"basis,omitempty"`
	// Track is the title (TrackID the bead) of the track the item sits in, when
	// it sits in one: the item reads "Workstream › Track".
	Track   string `json:"track,omitempty"`
	TrackID string `json:"track_id,omitempty"`
	// TrackLetter is that track's letter in its workstream (A, B…), as the
	// board shows it (cmdrd sets it with the display).
	TrackLetter string `json:"track_letter,omitempty"`
}

// NoPitch is an ask that is ready but has no pitch yet: a user gate with no
// write-up, or a needs-user step with no card. Writing it is the Manager's
// move, so it is never a NEEDS YOU item.
type NoPitch struct {
	Project      string `json:"project"`
	BeadID       string `json:"bead_id"`
	Title        string `json:"title"`
	Workstream   string `json:"workstream"`
	WorkstreamID string `json:"workstream_id"`
	ManagerRole  string `json:"manager_role"`
	Gate         bool   `json:"gate,omitempty"`
	// AskedAt is when cmdrd mailed the Manager to write it (unix s); 0 = not yet.
	AskedAt int64 `json:"asked_at,omitempty"`
	// Proposed is set when the ask is a Manager's proposal Commander has not
	// accepted yet (beads.Proposed*): Commander's move, not the Manager's pitch.
	Proposed   string `json:"proposed,omitempty"`
	ProposedAt int64  `json:"proposed_at,omitempty"`
}

// hasPitch: the ask carries something to decide on (a write-up, options or
// steps). Without one the app's PitchView can only say "No pitch yet".
func hasPitch(n NeedsYou) bool {
	if strings.TrimSpace(n.Note) != "" {
		return true
	}
	d := n.Decision
	return d != nil && (strings.TrimSpace(d.Context) != "" || len(d.Options) > 0 || len(d.Steps) > 0)
}

// NeedsYouList returns every open needs-the user step in the fleet, in workstream
// priority order. Asks with no pitch are left out (see NoPitchList).
func NeedsYouList(data Data) []NeedsYou {
	asks, _ := askLists(data)
	return asks
}

// NoPitchList is the ready asks NeedsYouList leaves out for having no pitch.
func NoPitchList(data Data) []NoPitch {
	_, none := askLists(data)
	return none
}

func askLists(data Data) ([]NeedsYou, []NoPitch) {
	out, none := []NeedsYou{}, []NoPitch{}
	byID := rowsByID(data.Objectives)
	for i, ws := range Workstreams(data) {
		add := func(row ObjectiveRow, n NeedsYou) {
			if row.HumanAnswerPending {
				return
			}
			if row.Proposed != "" {
				none = append(none, NoPitch{Project: n.Project, BeadID: n.BeadID, Title: n.Title, Workstream: n.Workstream, WorkstreamID: ws.Root.BeadID,
					ManagerRole: store.WorkstreamManagerRole(ownerIn(byID, row.BeadID)), Gate: row.Gate, Proposed: row.Proposed, ProposedAt: row.ProposedAt})
				return
			}
			if hasPitch(n) {
				out = append(out, n)
				return
			}
			none = append(none, NoPitch{Project: n.Project, BeadID: n.BeadID, Title: n.Title, Workstream: n.Workstream, WorkstreamID: ws.Root.BeadID,
				ManagerRole: store.WorkstreamManagerRole(ownerIn(byID, row.BeadID)), Gate: row.Gate})
		}
		if ws.Root.State == beads.StateNeedsUser && ws.Root.Status != "closed" && ws.Root.Status != beads.StatusDeferred {
			add(ws.Root, needsYouOf(ws, ws.Root, i+1))
		}
		byID := map[string]ObjectiveRow{}
		for _, step := range ws.Steps {
			byID[step.Row.BeadID] = step.Row
		}
		for _, step := range ws.Steps {
			if step.Row.Status == "closed" || step.Row.Status == beads.StatusDeferred {
				continue // finished or parked: out of play
			}
			if step.Row.State == beads.StateNeedsUser && !(isOpenUserGate(step.Row) && waitsOnOpenStage(step.Row, byID)) {
				add(step.Row, inTrack(ws, step.Row, needsYouOf(ws, step.Row, i+1)))
			} else if isOpenUserGate(step.Row) && !waitsOnOthers(step.Row) && !waitsOnOpenStage(step.Row, byID) && !followsOpenSibling(step.Row, ws.Steps, byID) {
				add(step.Row, inTrack(ws, step.Row, gateNeedsYou(ws, step.Row, i+1)))
			}
		}
	}
	return out, none
}

// isOpenUserGate: an unapproved gate the user approves (a workflow gate) waits in
// NEEDS YOU without a Manager having to flag it.
func isOpenUserGate(r ObjectiveRow) bool {
	return r.Gate && !r.AskAnswered && r.Status != "closed" && r.Status != beads.StatusDeferred && slices.Contains(r.Approvers, "user")
}

// waitsOnOthers: a gate recorded as waiting on someone else first (a review, a
// reply, CI, an Operator) is not the user's to answer yet.
func waitsOnOthers(r ObjectiveRow) bool {
	for _, w := range r.Waits {
		if w.Kind != beads.WaitUser {
			return true
		}
	}
	return false
}

// waitsOnOpenStage: a gate's ask is not the user's yet while an earlier stage it
// waits on (through blocked_by, followed transitively) is still open.
func waitsOnOpenStage(r ObjectiveRow, byID map[string]ObjectiveRow) bool {
	seen := map[string]bool{r.BeadID: true}
	queue := append([]string(nil), r.BlockedBy...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		b, ok := byID[id]
		if !ok {
			continue
		}
		if b.Status == beads.StatusDeferred {
			continue // parked: out of play, never blocks
		}
		if b.Status != "closed" && b.State != beads.StateDone {
			return true
		}
		queue = append(queue, b.BlockedBy...)
	}
	return false
}

// followsOpenSibling: stages run in plan order, so a gate also waits while an
// earlier sibling under the same parent is still open, even when the Manager
// never recorded a blocked_by between them. An earlier sibling that itself
// waits on the gate (a workflow gate created after its stages) does not count.
func followsOpenSibling(r ObjectiveRow, steps []NestedObjective, byID map[string]ObjectiveRow) bool {
	for _, s := range steps {
		if s.Row.BeadID == r.BeadID {
			return false
		}
		if s.Row.ParentBeadID == r.ParentBeadID && s.Row.Status != "closed" && s.Row.State != beads.StateDone && !waitsOn(s.Row, r.BeadID, byID) {
			return true
		}
	}
	return false
}

// waitsOn: r waits on the bead id, directly or through blocked_by.
func waitsOn(r ObjectiveRow, id string, byID map[string]ObjectiveRow) bool {
	seen := map[string]bool{r.BeadID: true}
	queue := append([]string(nil), r.BlockedBy...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == id {
			return true
		}
		if seen[next] {
			continue
		}
		seen[next] = true
		queue = append(queue, byID[next].BlockedBy...)
	}
	return false
}

// gateNeedsYou is the decision card of an unapproved gate: its write-up and
// the Approve / Change / Drop options.
// trackOf is the nearest nested workstream a row sits in (or is), walking up its
// parent edges; nil when it is in none.
func trackOf(ws Workstream, row ObjectiveRow) *ObjectiveRow {
	byID := map[string]ObjectiveRow{}
	for _, s := range ws.Steps {
		byID[s.Row.BeadID] = s.Row
	}
	for r, hops := row, 0; hops < 32; hops++ {
		if r.Track {
			return &r
		}
		p, ok := byID[r.ParentBeadID]
		if !ok {
			return nil
		}
		r = p
	}
	return nil
}

// inTrack tags a NEEDS YOU item with the track it belongs to.
func inTrack(ws Workstream, row ObjectiveRow, n NeedsYou) NeedsYou {
	if t := trackOf(ws, row); t != nil {
		n.Track, n.TrackID = t.Title, t.BeadID
	}
	return n
}

func gateNeedsYou(ws Workstream, row ObjectiveRow, rank int) NeedsYou {
	ask := "Approve: " + row.Title + "?"
	if strings.HasPrefix(strings.ToLower(row.Title), "greenlight") {
		ask = row.Title + "?"
	}
	n := NeedsYou{
		Project: ws.Project.Name, BeadID: row.BeadID, Title: row.Title, Workstream: ws.Root.Title, PlanRevision: row.PlanRevision,
		Rank: rank, Ask: ask,
	}
	ctx := strings.TrimSpace(row.GateNote)
	if ctx == "" {
		return n // no pitch: never a card to approve blind (askLists drops it)
	}
	n.Note = ctx
	n.Decision = GateDecision(row.Title, ctx)
	return n
}

func needsYouOf(ws Workstream, row ObjectiveRow, rank int) NeedsYou {
	return NeedsYou{
		Project: ws.Project.Name, BeadID: row.BeadID, Title: row.Title, Workstream: ws.Root.Title, PlanRevision: row.PlanRevision,
		Rank: rank, Ask: row.Ask, Note: row.Note, Decision: row.Decision, Since: row.AskSince,
		SpecHash: row.SpecHash, Basis: row.Basis, PushCount: row.PushCount, PushFirst: row.PushFirst,
	}
}

// StepNode is a step with its children (phase > unit), in plan order.
type StepNode struct {
	Row      ObjectiveRow
	Children []*StepNode
}

// Done reports whether the step is finished.
func (n *StepNode) Done() bool { return n.Row.State == beads.StateDone || n.Row.Status == "closed" }

// Active reports whether the step, or anything under it, is moving or needs
// attention.
func (n *StepNode) Active() bool {
	switch n.Row.State {
	case beads.StateInProgress, beads.StateNeedsUser, beads.StateBlocked:
		if !n.Done() {
			return true
		}
	}
	for _, c := range n.Children {
		if c.Active() {
			return true
		}
	}
	return false
}

// LiveAgent reports the live state of the agent that owns a bead ("" = no
// live agent under that owner role).
type LiveAgent func(owner string) string

// DerivedState is DerivedLive without agents: only the beads speak.
func DerivedState(n *StepNode) string {
	state, _ := DerivedLive(n, nil)
	return state
}

// DerivedLive is the state a bead shows, and whether an agent under it is
// working right now. A bead without children is its own state, except that a
// bead a live agent owns is at least in progress. A bead with children is
// what they add up to for activity, so it cannot drift: done only when its
// own bead is closed (all children done leaves it open, as progress), then
// needs you, in progress (also when a
// descendant is in review or owned by a live agent), blocked, else not
// started. Its own needs-user or blocked marker still wins: that is someone
// flagging the bead itself. live may be nil.
func DerivedLive(n *StepNode, live LiveAgent) (string, bool) {
	own := n.Row.State
	agent := ""
	if live != nil && n.Row.Owner != "" {
		agent = live(n.Row.Owner)
	}
	working := agent == store.AgentWorking
	if n.Row.Status == "closed" {
		return beads.StateDone, false
	}
	if n.Row.Status == beads.StatusDeferred {
		return beads.StateNotStarted, false // parked: nothing under it counts as moving
	}
	// A parked child is out of play: it neither moves nor holds its parent back.
	var kids []*StepNode
	for _, c := range n.Children {
		if c.Row.Status != beads.StatusDeferred {
			kids = append(kids, c)
		}
	}
	seen := map[string]bool{}
	all := true
	for _, c := range kids {
		s, w := DerivedLive(c, live)
		working = working || w
		seen[s] = true
		if s != beads.StateDone {
			all = false
		}
	}
	if own == beads.StateNeedsUser || own == beads.StateBlocked {
		return own, working
	}
	if len(kids) == 0 {
		// A claim is the bead's own word: owner set on an open step means
		// someone is on it, whether or not their process answers.
		if (agent != "" || n.Row.Owner != "") && own == beads.StateNotStarted {
			return beads.StateInProgress, working
		}
		return own, working
	}
	if all {
		// An open gate is a human approval: its children being done does not
		// approve it. It shows its own state until it is closed.
		if n.Row.Gate {
			return own, working
		}
		// Likewise an open stage: its sub-tasks being done is progress, not
		// the stage being done (that is its own bead closing). Started, though.
		if own == beads.StateNotStarted {
			return beads.StateInProgress, working
		}
		return own, working
	}
	switch {
	case seen[beads.StateNeedsUser]:
		return beads.StateNeedsUser, working
	case seen[beads.StateInProgress], seen[beads.StateReview], working:
		return beads.StateInProgress, working
	case seen[beads.StateBlocked]:
		return beads.StateBlocked, working
	case seen[beads.StateDone]:
		// Some of it done and the rest still to do: started, not "not started".
		return beads.StateInProgress, working
	}
	return beads.StateNotStarted, working
}

// BuildStepTree turns a workstream's flat nested steps into trees by depth.
func BuildStepTree(steps []NestedObjective) []*StepNode {
	var roots []*StepNode
	var stack []*StepNode // stack[d-1] is the latest node at depth d
	for _, st := range steps {
		n := &StepNode{Row: st.Row}
		d := max(st.Depth, 1)
		if d > len(stack)+1 {
			d = len(stack) + 1
		}
		stack = append(stack[:d-1], n)
		if d == 1 {
			roots = append(roots, n)
		} else {
			parent := stack[d-2]
			parent.Children = append(parent.Children, n)
		}
	}
	return roots
}

// NowStep is the step the Manager is on: the first in-progress leaf in plan
// order, else the first leaf waiting on the user.
func NowStep(roots []*StepNode) string {
	var firstNeeds string
	var found string
	var walk func(ns []*StepNode)
	walk = func(ns []*StepNode) {
		for _, n := range ns {
			if found != "" {
				return
			}
			if n.Done() {
				continue
			}
			if len(n.Children) > 0 {
				walk(n.Children)
				continue
			}
			switch n.Row.State {
			case beads.StateInProgress:
				found = n.Row.BeadID
			case beads.StateNeedsUser:
				if firstNeeds == "" {
					firstNeeds = n.Row.BeadID
				}
			}
		}
	}
	walk(roots)
	if found != "" {
		return found
	}
	return firstNeeds
}

// PhaseNote is the one-line state of a collapsed phase: "not started",
// "2 of 5 done", "in progress, 1 of 3 done", "⚑ needs you", "✗ blocked".
func PhaseNote(n *StepNode) string {
	total, done := 0, 0
	var needs, blocked, running bool
	var walk func(ns []*StepNode)
	walk = func(ns []*StepNode) {
		for _, c := range ns {
			if len(c.Children) > 0 {
				walk(c.Children)
				continue
			}
			total++
			switch {
			case c.Done():
				done++
			case c.Row.State == beads.StateNeedsUser:
				needs = true
			case c.Row.State == beads.StateBlocked:
				blocked = true
			case c.Row.State == beads.StateInProgress:
				running = true
			}
		}
	}
	walk(n.Children)
	switch {
	case needs:
		return "⚑ needs you"
	case blocked:
		return "✗ blocked"
	case running:
		return fmt.Sprintf("in progress, %d of %d done", done, total)
	case done > 0:
		return fmt.Sprintf("%d of %d done", done, total)
	}
	return "not started"
}

// GateDecision is shared by displayed automatic cards and the typed answer
// path that journals them. Recording an answer never invents different choices.
func GateDecision(title, context string) *store.Decision {
	ask := "Approve: " + title + "?"
	if strings.HasPrefix(strings.ToLower(title), "greenlight") {
		ask = title + "?"
	}
	return &store.Decision{Question: ask, Context: context, Options: []store.DecisionOption{
		{Label: beads.GateApprove, Implications: "The gate closes and the work behind it may start.", Recommended: true},
		{Label: beads.GateChange, Implications: "The gate stays open; the Manager reworks the workflow from your note."},
		{Label: beads.GateDrop, Implications: "Do not start this; the affected workstream is parked."},
	}}
}
