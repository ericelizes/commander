package livedata

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

// Diagram is a workstream's (or track's) plan drawn as whose move each step
// is, derived from beads only: the steps at its level in work order (a
// blocker is never below what waits on it), each in a lane (the user's, the
// agents', or elsewhere: a reviewer, CI, a clock, another team), gates with
// who approves and their criteria, and where the work is now. Kind and Text
// are the workflow's (so the board's) words. The app draws it; `cmdr board
// --diagram` prints it; `cmdr graph check <ws|bead>` lists its Findings.
type Diagram struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Current string        `json:"current,omitempty"` // the step at this level the work is in now
	Steps   []DiagramStep `json:"steps"`
	// Tracks: on a workstream, each open track's own diagram.
	Tracks []Diagram `json:"tracks,omitempty"`
	// Findings: what keeps this graph from reading right (CheckDiagram), its tracks' included.
	Findings []DiagramFinding `json:"findings,omitempty"`
}

// DiagramStep is one step. Actor is whose line it is on and Lane the coarse
// side of it (you; agents: manager, operators; elsewhere: reviewers,
// automatic); both are "" only when nothing in beads says whose move it is
// (a finding). Who says more: reviewers' or a team's names, what runs and
// what starts it.
type DiagramStep struct {
	Why       string   `json:"why,omitempty"`
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	Since     int64    `json:"since,omitempty"`
	Lane      string   `json:"lane"`
	Actor     string   `json:"actor"` // whose line it is on: you, manager, operators, reviewers, automatic ("" = nobody's)
	Who       string   `json:"who,omitempty"`
	Gate      bool     `json:"gate,omitempty"`
	Approvers string   `json:"approvers,omitempty"` // who approves a gate, the user as "you"
	Criteria  string   `json:"criteria,omitempty"`  // what counts as passed
	After     []string `json:"after,omitempty"`     // the steps of this diagram it waits on
	Track     bool     `json:"track,omitempty"`
	Progress  string   `json:"progress,omitempty"` // a step with work under it: "3 of 5 done"
	// Children: the steps nested under an open step, in work order (open ones, the latest done, every gate), drawn inside its box.
	Children []DiagramStep `json:"children,omitempty"`
	// Review: a plugged review block, drawn collapsed as this one step; its
	// checks (with what each found) open on a click. Its steps are not Children.
	Review *ReviewBlock `json:"review,omitempty"`
}

// DiagramFinding is one check that fails, in plain words, with the fix.
// Advisory findings are judgment calls (titles); the rest are certain.
type DiagramFinding struct {
	Check    string `json:"check"`
	Bead     string `json:"bead"`
	Title    string `json:"title"`
	Why      string `json:"why"`
	Fix      string `json:"fix"`
	Advisory bool   `json:"advisory,omitempty"`
}

// FindingKind separates expected waits and advice from workflow errors.
func FindingKind(f DiagramFinding) string {
	if f.Advisory {
		return "advisory"
	}
	if f.Check == "MERGE_REVIEW_INCOMPLETE" || f.Check == "MERGE_REVIEW_REQUIRED" {
		return "blocker"
	}
	return "error"
}

// diagram derives the workstream's diagram from the same flow as its workflow.
func (f flow) diagram(w *Workflow) *Diagram {
	if w == nil {
		return nil
	}
	d := f.level(f.ws.BeadID, titleOf(f.ws.Title, f.ws.BeadID), f.ws.Steps, w.Steps, w.Current)
	f.nestedDiagrams(f.ws.Steps, w.Steps, &d)
	d.Findings = CheckDiagram(f.ws, f.ws.BeadID)
	return &d
}

// nestedDiagrams gives every nested workstream its diagram, at any depth, parked
// and finished ones too: any header can be opened.
func (f flow) nestedDiagrams(steps []SnapStep, wf []WorkflowStep, d *Diagram) {
	for _, t := range steps {
		if !t.Track || t.Superseded || len(t.Children) == 0 {
			continue
		}
		ws := findWorkflowStep(wf, t.BeadID)
		if ws == nil {
			continue
		}
		children := ws.Children
		if len(children) == 0 {
			children = f.steps(t.Children) // the workflow leaves a done or parked workstream's steps out
		}
		td := f.level(t.BeadID, titleOf(ws.Title, t.BeadID), t.Children, children, ws.Current)
		td.Findings = CheckDiagram(f.ws, t.BeadID)
		d.Tracks = append(d.Tracks, td)
		f.nestedDiagrams(t.Children, children, d)
	}
}

// level is one diagram: steps (their workflow steps wf) in work order.
func (f flow) level(id, title string, steps []SnapStep, wf []WorkflowStep, current string) Diagram {
	d := Diagram{ID: id, Title: title, Steps: []DiagramStep{}}
	f.xdrawn = map[string]string{} // one card per other-track wait, in this diagram
	live, top, after := levelAfter(shown(steps))
	d.Current = top[current]
	// The highlight goes where an agent works: a current step only waiting on
	// another step that is being worked hands the highlight to that step.
	if cur := slices.IndexFunc(live, func(s SnapStep) bool { return s.BeadID == d.Current }); cur >= 0 {
		for _, t := range after[d.Current] {
			if i := slices.IndexFunc(live, func(s SnapStep) bool { return s.BeadID == t }); i >= 0 && !stepClosed(live[i]) && live[i].State == "in-progress" {
				if w := findWorkflowStep(wf, t); w != nil && w.Kind == "working" {
					d.Current, current = t, t
					break
				}
			}
		}
	}
	ordered := workOrder(live, after)
	// What is done is the past: drawn before everything still open.
	slices.SortStableFunc(ordered, func(a, b SnapStep) int { return btoi(!stepClosed(a)) - btoi(!stepClosed(b)) })
	for _, s := range ordered {
		w := findWorkflowStep(wf, s.BeadID)
		if w == nil {
			continue
		}
		steps, cur := f.diagramSteps(s, w, after[s.BeadID], 0)
		if cur != "" && d.Current == s.BeadID {
			d.Current = cur
		}
		d.Steps = append(d.Steps, steps...)
	}
	// The work may be on a step nested under one drawn here (a gate in its box).
	if cur := drawnID(d.Steps, current); cur != "" {
		d.Current = cur
	}
	// Or on a check inside a review block: the block is where it is drawn.
	if cur := blockOf(d.Steps, current); cur != "" {
		d.Current = cur
	}
	return d
}

// blockOf is the drawn review block that holds the check bead id, or "".
func blockOf(steps []DiagramStep, id string) string {
	for _, s := range steps {
		if s.Review != nil && id != "" && slices.ContainsFunc(s.Review.Checks, func(c ReviewCheck) bool { return c.ID == id }) {
			return s.ID
		}
		if b := blockOf(s.Children, id); b != "" {
			return b
		}
	}
	return ""
}

// drawnID is the id the step id is drawn under anywhere in steps (nested
// ones too; a gate split into a wait step is drawn as the wait first), or "".
func drawnID(steps []DiagramStep, id string) string {
	found := ""
	var walk func([]DiagramStep, bool)
	walk = func(ss []DiagramStep, nested bool) {
		for _, s := range ss {
			if nested && (s.ID == id || s.ID == id+WaitStepSuffix) && found == "" {
				found = s.ID
			}
			walk(s.Children, true)
		}
	}
	walk(steps, false)
	return found
}

// recentDoneChildren is how many of the latest done steps under a step are
// still drawn in its box; earlier ones fold into its progress note.
const recentDoneChildren = 3

// maxChildDepth bounds how deep steps nested under steps are drawn.
const maxChildDepth = 3

// diagramSteps is the step s as drawn (a gate waiting on someone other than
// its approvers is the wait step, then the gate), with the steps nested under
// it in Children. The second result is the wait step's id when it split.
func (f flow) diagramSteps(s SnapStep, w *WorkflowStep, after []string, depth int) ([]DiagramStep, string) {
	h := f.hand(s, w.Text)
	st := DiagramStep{ID: s.BeadID, Title: w.Title, Kind: w.Kind, Text: w.Text, Why: w.Why, Since: w.Since, Lane: laneOfActor(h.actor), Actor: h.actor, Who: h.who,
		Gate: s.Gate, Criteria: s.Criteria, After: after, Track: s.Track}
	var out []DiagramStep
	cur := ""
	// A wait on a step in another track is drawn as its own step before this
	// one, so the diagram says why it is not reached.
	if !stepClosed(s) && !parked(s) && !f.openInTrack(s) {
		for _, x := range f.crossWaits(s) {
			if id, drawn := f.xdrawn[x.Who+"\x00"+x.Title]; drawn {
				st.After = append(st.After, id)
				continue
			}
			if f.xdrawn != nil {
				f.xdrawn[x.Who+"\x00"+x.Title] = x.ID
			}
			out = append(out, x)
			st.After = []string{x.ID}
			if cur == "" {
				cur = x.ID
			}
		}
	}
	if s.Gate {
		st.Approvers = approverNames(s.Approvers)
		// A gate waiting on someone other than its approvers is two moves:
		// theirs now (a reviewer, CI), then the approvers'.
		a := approverHand(s.Approvers)
		if onUserCard(s) {
			a = hand{ActorYou, ""} // the user answers its open card, whoever the approver label names
		}
		if h.actor != "" && a.actor != "" && h.actor != a.actor && !stepClosed(s) && !parked(s) {
			wait := DiagramStep{ID: s.BeadID + WaitStepSuffix, Title: gateWaitTitle(s, h), Kind: w.Kind, Text: w.Text, Why: w.Why, Since: w.Since,
				Lane: st.Lane, Actor: h.actor, Who: h.who, After: st.After}
			st.Kind, st.Text, st.Since = "idle", "Not reached", 0
			st.Lane, st.Actor, st.Who, st.After = laneOfActor(a.actor), a.actor, a.who, []string{wait.ID}
			out = append(out, wait)
			cur = wait.ID
		}
	}
	if w.Review != nil {
		// One step on its parent's level: its checks ride on it, never drawn as children.
		st.Review, st.Progress = w.Review, w.Review.progress()
		return append(out, st), cur
	}
	if !stepClosed(s) && !parked(s) && f.container(s) {
		st.Progress = s.PhaseNote
		// A track's steps are its own diagram's (Tracks), not drawn a second time under it.
		if depth < maxChildDepth && !s.Track {
			st.Children = f.childSteps(s, w, depth+1)
		}
	}
	return append(out, st), cur
}

// childSteps are the steps nested under s, in work order, done ones first:
// every open or parked one, the latest few done, and always every gate and
// every step that asks the user (the step a card is about).
func (f flow) childSteps(s SnapStep, w *WorkflowStep, depth int) []DiagramStep {
	live, _, after := levelAfter(shown(s.Children))
	ordered := workOrder(live, after)
	slices.SortStableFunc(ordered, func(a, b SnapStep) int { return btoi(!stepClosed(a)) - btoi(!stepClosed(b)) })
	nDone := 0
	for _, c := range ordered {
		nDone += btoi(stepClosed(c))
	}
	var out []DiagramStep
	seenDone := 0
	for _, c := range ordered {
		if stepClosed(c) {
			seenDone++
			if nDone-seenDone >= recentDoneChildren && !c.Gate && !f.decision(c) {
				continue
			}
		}
		cw := findWorkflowStep(w.Children, c.BeadID)
		if cw == nil {
			cs := f.step(c)
			cw = &cs
		}
		steps, _ := f.diagramSteps(c, cw, after[c.BeadID], depth)
		out = append(out, steps...)
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// WaitStepSuffix marks the step a gate's current wait is drawn as (its bead
// id plus the suffix): the reviewer's or CI's move before the approvers'.
const WaitStepSuffix = "#wait"

// gateWaitTitle names the move a gate waits on, as an outcome on its actor's line.
func gateWaitTitle(s SnapStep, h hand) string {
	pr := ""
	if s.Review != "" && s.Review != "?" {
		pr = " #" + s.Review
	}
	for _, w := range strings.Split(s.Waiting, " · ") {
		switch {
		case strings.HasPrefix(w, "review ("):
			who := strings.TrimSuffix(strings.TrimPrefix(w, "review ("), ")")
			if strings.Contains(who, ",") {
				return who + " review PR" + pr
			}
			return who + " reviews PR" + pr
		case strings.HasPrefix(w, "main CI"):
			return "CI passes on main"
		case isCIWait(w):
			return "CI passes on PR " + strings.TrimSpace(ciPrefix.ReplaceAllString(w, ""))
		}
	}
	switch h.actor {
	case ActorReviewers:
		return "Review of PR" + pr + " done"
	case ActorOperators:
		return "An Operator finishes the work it waits on"
	case ActorManager:
		return "The Manager readies it"
	}
	if w := strings.Split(s.Waiting, " · ")[0]; w != "" {
		return w
	}
	return "Its wait clears"
}

// levelAfter is one level's steps (superseded ones dropped), the step of the
// level each bead under them sits in, and what each step waits on at this
// level: its own blocks edges and those of every bead under it, and a wait
// tagged on an Operator ("waiting": operator-…) as an edge to the step that
// Operator works, so a step waiting on another is drawn after it.
func levelAfter(steps []SnapStep) ([]SnapStep, map[string]string, map[string][]string) {
	var live []SnapStep
	top := map[string]string{}
	for _, s := range steps {
		if s.Superseded {
			continue
		}
		live = append(live, s)
		for x := range stepValues([]SnapStep{s}) {
			top[x.BeadID] = s.BeadID
		}
	}
	works := map[string]string{} // Operator -> the step of this level it works under
	for _, s := range live {
		for x := range stepValues([]SnapStep{s}) {
			if strings.HasPrefix(x.Owner, "operator-") && !stepClosed(x) {
				works[x.Owner] = s.BeadID
			}
		}
	}
	after := map[string][]string{}
	edge := func(from, to string) {
		if to != "" && to != from && !slices.Contains(after[from], to) {
			after[from] = append(after[from], to)
		}
	}
	for _, s := range live {
		for x := range stepValues([]SnapStep{s}) {
			for _, b := range x.BlockedBy {
				edge(s.BeadID, top[b])
			}
			if !stepClosed(x) {
				for _, op := range waitedOperators.FindAllString(x.Waiting, -1) {
					edge(s.BeadID, works[op])
				}
			}
		}
	}
	return live, top, after
}

var waitedOperators = regexp.MustCompile(`operator-[\w.-]+`)

// workOrder sorts steps by blocks depth (the longest chain of waits before
// each), plan order within a depth. An edge that closes a cycle is ignored
// here; CheckDiagram reports the cycle.
func workOrder(steps []SnapStep, after map[string][]string) []SnapStep {
	depth := map[string]int{}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(id string) int
	visit = func(id string) int {
		switch state[id] {
		case 1:
			return -1
		case 2:
			return depth[id]
		}
		state[id] = 1
		d := 0
		for _, b := range after[id] {
			if x := visit(b); x >= 0 && x+1 > d {
				d = x + 1
			}
		}
		state[id], depth[id] = 2, d
		return d
	}
	for _, s := range steps {
		visit(s.BeadID)
	}
	out := slices.Clone(steps)
	slices.SortStableFunc(out, func(a, b SnapStep) int { return depth[a.BeadID] - depth[b.BeadID] })
	return out
}

// The actors of the diagram, one lifeline each in the app (ExecutionFlow).
const (
	ActorYou       = "you"       // user gates, needs-user steps
	ActorManager   = "manager"   // the Manager's move or work, Manager-approved gates
	ActorOperators = "operators" // Operator work, live or planned
	ActorReviewers = "reviewers" // reviewers, named approvers, another team or person
	ActorAutomatic = "automatic" // CI, timers, automatic checks
)

// laneOfActor is the lane an actor's line belongs to.
func laneOfActor(actor string) string {
	switch actor {
	case ActorYou:
		return beads.LaneYou
	case ActorManager, ActorOperators:
		return beads.LaneAgents
	case ActorReviewers, ActorAutomatic:
		return beads.LaneElsewhere
	}
	return ""
}

// hand is whose move a step is: an actor and, where it says more, who.
type hand struct{ actor, who string }

// hand is whose move s is. A step with open work under it is the hand of
// the leaf its line (text) speaks for; failing that, of its most urgent leaf
// by the board's order (red, yellow, green), the first in plan order on a tie.
func (f flow) hand(s SnapStep, text string) hand {
	if _, ok := resultHandoff([]SnapStep{s}, f.b.now); ok {
		return hand{ActorManager, ""}
	}
	if stepClosed(s) || parked(s) || !f.container(s) {
		return f.leafHand(s)
	}
	var leaves []SnapStep
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, c := range steps {
			if c.Superseded || parked(c) || stepClosed(c) {
				continue
			}
			if f.container(c) {
				walk(c.Children)
			} else {
				leaves = append(leaves, c)
			}
		}
	}
	// Its own wait, owner or ask counts too: a track can wait on something itself.
	if s.Waiting != "" || onUser(s) || liveTask(s) || (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s)) {
		leaves = append(leaves, s)
	}
	walk(s.Children)
	best, bestAt := -1, 0
	for i, c := range leaves {
		l := humanWait(f.leafLine(c), f.b.tasks)
		if l.text != "" && strings.HasPrefix(text, l.text) {
			return f.leafHand(c)
		}
		if best < 0 || urgency[l.kind] < bestAt {
			best, bestAt = i, urgency[l.kind]
		}
	}
	if best < 0 {
		return restingHand(s)
	}
	return f.leafHand(leaves[best])
}

// leafHand is a step's own hand, read in the order the board reads its line
// (leafLine), so the line it sits on and its words never disagree.
func (f flow) leafHand(s SnapStep) hand {
	if stepClosed(s) {
		return restingHand(s)
	}
	if parked(s) {
		if s.ParkedUntil != 0 {
			return hand{ActorAutomatic, "parked until " + clock(s.ParkedUntil, f.b.now, f.b.loc) + ", then the Manager picks it up"}
		}
		return hand{ActorYou, "on hold until you bring it back"}
	}
	if _, ok := f.b.noPitch[f.ws.Project+"\x00"+s.BeadID]; ok {
		return hand{ActorManager, "writing the pitch"}
	}
	if _, held := f.b.heldCard[f.ws.Project+"\x00"+s.BeadID]; held && !unfinishedBlocker(s, f.byID) {
		return hand{ActorYou, ""}
	}
	if f.decision(s) {
		return hand{ActorYou, ""}
	}
	switch s.OwnerState {
	case "died", "finished":
		return hand{ActorManager, ""}
	}
	if (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s)) || liveTask(s) {
		return hand{ActorOperators, ""}
	}
	if f.ws.NowStep == s.BeadID && agentBusy(f.manager, f.hasManager) {
		return hand{ActorManager, ""}
	}
	if r := s.ReviewFact; r != nil {
		if r.Acted || r.NoPR {
			return hand{ActorManager, ""}
		}
		return hand{ActorReviewers, r.Text}
	}
	var waits []string
	for _, w := range strings.Split(s.Waiting, " · ") {
		if w != "" && w != "you" && !s.waitExpired(f.b.now, w) {
			waits = append(waits, w)
		}
	}
	// CI first, as the board's line reads it.
	slices.SortStableFunc(waits, func(a, b string) int {
		switch {
		case isCIWait(a) && !isCIWait(b):
			return -1
		case isCIWait(b) && !isCIWait(a):
			return 1
		}
		return 0
	})
	if len(waits) > 0 {
		return waitHand(waits[0])
	}
	if onUserCard(s) {
		return hand{ActorYou, ""}
	}
	if unfinishedBlocker(s, f.byID) || f.blockedAbove(s) {
		return restingHand(s)
	}
	if s.Gate {
		return approverHand(s.Approvers)
	}
	if s.Broken {
		return hand{}
	}
	if s.State == "in-progress" {
		return hand{ActorManager, ""}
	}
	return restingHand(s)
}

// restingHand is whose move s is once it is reached (or was, once done): a
// gate's approvers', the lane label's, its owner's, else an Operator's (the
// Manager spawns one for planned work).
func restingHand(s SnapStep) hand {
	// An open card the user answers is theirs, whatever the approver label says (a gate approved
	// by a named person that the user answers by relaying).
	if onUserCard(s) {
		return hand{ActorYou, ""}
	}
	if s.Gate {
		return approverHand(s.Approvers)
	}
	if s.Lane != "" {
		if lane, who, err := beads.ParseLane(s.Lane); err == nil {
			switch lane {
			case beads.LaneYou:
				return hand{ActorYou, ""}
			case beads.LaneAgents:
				return hand{ActorOperators, ""}
			}
			return hand{ActorReviewers, who}
		}
	}
	switch {
	case s.Owner == "user":
		return hand{ActorYou, ""}
	case strings.HasPrefix(s.Owner, "manager-"):
		return hand{ActorManager, ""}
	}
	return hand{ActorOperators, ""}
}

// approverHand: a gate the user approves is theirs; one only the Manager
// approves is the Manager's; automatic checks are automatic (the Manager
// confirms them); reviewers and named people are reviewers, by name.
func approverHand(approvers []string) hand {
	if len(approvers) == 0 {
		return hand{}
	}
	if hasUser(approvers) {
		return hand{ActorYou, ""}
	}
	kinds := map[string]bool{}
	for _, a := range approvers {
		kinds[approverKind(a)] = true
	}
	switch {
	case len(kinds) == 1 && kinds[ActorAutomatic]:
		return hand{ActorAutomatic, "an automatic check, the Manager confirms"}
	case len(kinds) == 1 && kinds[ActorOperators]:
		return hand{ActorOperators, approverNames(approvers)}
	case !kinds[ActorReviewers]:
		return hand{ActorManager, ""}
	}
	return hand{ActorReviewers, approverNames(approvers)}
}

func approverKind(a string) string {
	a = strings.ToLower(a)
	switch {
	case strings.HasPrefix(a, "manager"):
		return ActorManager
	case strings.HasPrefix(a, "operator"), a == "agent", a == "agents":
		return ActorOperators
	case a == "automatic", a == "auto", a == "ci":
		return ActorAutomatic
	}
	return ActorReviewers
}

// waitHand is the hand of one recorded wait (the step's text already names
// it): automatic ones say what runs and what starts it, or that the Manager confirms.
func waitHand(w string) hand {
	switch {
	case strings.HasPrefix(w, "operator-"):
		return hand{ActorOperators, ""}
	case strings.HasPrefix(w, "manager-"), w == beads.ManagerCloseWait, w == beads.ManagerStartWait, w == beads.ManagerDispatchWait:
		return hand{ActorManager, ""}
	case strings.HasPrefix(w, "review ("):
		return hand{ActorReviewers, strings.TrimSuffix(strings.TrimPrefix(w, "review ("), ")")}
	case strings.HasPrefix(w, "main CI"):
		return hand{ActorAutomatic, "CI on main, runs on the merge"}
	case isCIWait(w):
		return hand{ActorAutomatic, "CI, runs on each push to the PR"}
	case strings.HasPrefix(w, "prod tag"):
		return hand{ActorAutomatic, "the release tags prod"}
	}
	return hand{ActorAutomatic, "a timer, then the Manager confirms"}
}

// CheckDiagram lists what keeps the graph under scope (a workstream, or a
// track or step in it) from reading right: open steps nothing holds, bad lane
// labels, gates without approvers or criteria, dependency cycles, gate titles
// off the tense convention (advisory), and the plan rules (planRules).
func CheckDiagram(ws SnapWorkstream, scope string) []DiagramFinding {
	steps := ws.Steps
	if scope != ws.BeadID {
		s := findSnapStep(ws.Steps, scope)
		if s == nil {
			return nil
		}
		steps = []SnapStep{*s}
	}
	out := mergeReviewRules(ws, steps)
	add := func(check string, s SnapStep, why, fix string, advisory bool) {
		out = append(out, DiagramFinding{Check: check, Bead: s.BeadID, Title: plainTitle(s.Title), Why: why, Fix: fix, Advisory: advisory})
	}
	var walk func([]SnapStep)
	walk = func(level []SnapStep) {
		for _, s := range level {
			if s.Superseded || parked(s) {
				continue
			}
			walk(s.Children)
			if s.Lane != "" {
				if _, _, err := beads.ParseLane(s.Lane); err != nil {
					add("lane-label", s, "its label lane:"+s.Lane+" is not a lane, so the diagram ignores it",
						"cmdr internal-set-objective --project "+ws.Project+" --bead "+s.BeadID+" --lane you|agents|elsewhere:<who> (or --lane none)", false)
				}
			}
			if stepClosed(s) {
				continue
			}
			if s.Broken {
				add("unheld", s, "it is open with no running agent, no recorded wait and nothing asked of you, so the diagram cannot say whose move it is",
					"record what it waits on (--waiting ci:#N, review:<login>, until:<time>:<what then>), give it a running owner, or ask the user (--state needs-user --decision)", false)
			}
			if !s.Gate {
				continue
			}
			if len(s.Approvers) == 0 {
				add("gate-approvers", s, "the gate names nobody who approves it, so it sits in no lane",
					"add approver:<user|login|name> labels, or recreate it with internal-create-objective --gate --approver", false)
			}
			if s.Criteria == "" {
				add("gate-criteria", s, "the gate does not say what counts as passed",
					"cmdr internal-update --project "+ws.Project+" --bead "+s.BeadID+" --acceptance \"<what counts as passed, one sentence>\"", false)
			}
			if why := gateTitleProblem(s); why != "" {
				add("gate-title", s, why, "cmdr internal-rename-objective --project "+ws.Project+" --bead "+s.BeadID+" --title \"<Greenlight doing it | what is true, past tense>\"", true)
			}
		}
	}
	walk(steps)
	beadLoop := map[string]bool{}
	for _, c := range blockCycles(steps) {
		s := findSnapStep(ws.Steps, c[0])
		if s == nil {
			continue
		}
		for _, id := range c {
			beadLoop[id] = true
		}
		add("cycle", *s, "its dependencies loop ("+strings.Join(append(c, c[0]), " waits on ")+"), so nothing in the loop can start",
			"cmdr internal-dep --project "+ws.Project+" rm <step> <the step it does not really wait on>", false)
	}
	// The diagram's own levels: two steps can wait on each other through the
	// beads under them with no loop between beads (a gate placed beside the
	// stage whose run it sits in). The diagram cannot put either first.
	levels := [][]SnapStep{steps}
	if scope == ws.BeadID {
		for _, t := range openTracks(ws) {
			levels = append(levels, t.Children)
		}
	} else if len(steps) == 1 {
		levels = [][]SnapStep{steps[0].Children}
	}
	for _, lv := range levels {
		_, _, after := levelAfter(lv)
		for _, c := range cycles(after) {
			if slices.ContainsFunc(c, func(id string) bool { return beadLoop[id] }) {
				continue
			}
			s := findSnapStep(ws.Steps, c[0])
			if s == nil {
				continue
			}
			add("order", *s, "it and "+strings.Join(c[1:], ", ")+" wait on each other through the steps under them, so the diagram cannot draw one before the other",
				"move the step that sits inside the other's run under it (a gate in the middle of a stage is a child of that stage), or split the stage at it", true)
		}
	}
	out = append(out, planRules(ws, steps)...)
	return out
}

// CheckNeedsYouLane is the check-name of needsYouLane's finding.
const CheckNeedsYouLane = "needs-you-lane"

// needsYouLane lists the open cards the board shows as something other than
// the user's move: an open decision (userDecisions) whose diagram step is not
// in the You lane reading Needs you, or whose row does not read Needs you. It
// compares what r draws, so it does not depend on how r was derived. Left out
// on purpose, because the board means them: a decision whose prerequisite work
// a live Operator is on (heldByLive) and a gate still behind an open blocker
// ("Not reached"). Needs b.steps, so it runs inside b.row.
func (b board) needsYouLane(ws SnapWorkstream, r BoardRow) []DiagramFinding {
	asks, _ := b.heldByLive(userDecisions(ws, b.needsIDs))
	var out []DiagramFinding
	for _, s := range asks {
		if s.Gate && unfinishedBlocker(s, b.steps) {
			continue
		}
		var wrong []string
		if d := r.Diagram.drawn(s.BeadID); d != nil && (d.Lane != beads.LaneYou || d.Kind != "needs") {
			wrong = append(wrong, fmt.Sprintf("the diagram draws it in lane %s reading %q", orNobody(d.Lane), d.Text))
		}
		if r.Kind != "needs" {
			wrong = append(wrong, fmt.Sprintf("its row reads %q", r.Text))
		}
		for _, t := range r.AllTracks() {
			if t.Kind != "needs" {
				if track := findSnapStep(ws.Steps, t.BeadID); track != nil && holds(*track, s.BeadID) {
					wrong = append(wrong, fmt.Sprintf("its workstream's row reads %q", t.Text))
				}
			}
		}
		if len(wrong) == 0 {
			continue
		}
		out = append(out, DiagramFinding{Check: CheckNeedsYouLane, Bead: s.BeadID, Title: plainTitle(s.Title),
			Why: "it has an open card, but " + strings.Join(wrong, " and ") + ", so the user is not shown their move",
			Fix: "if the ask no longer stands, withdraw it: cmdr internal-set-objective --project " + ws.Project + " --bead " + s.BeadID + " --status in-progress; if it stands, the board derivation is wrong: mail Commander this finding"})
	}
	return out
}

// addNeedsYouLane puts needsYouLane's findings on r's diagram and on the
// diagram of the track each one sits in.
func (b board) addNeedsYouLane(ws SnapWorkstream, r *BoardRow) {
	if r.Diagram == nil {
		return
	}
	for _, f := range b.needsYouLane(ws, *r) {
		r.Diagram.Findings = append(r.Diagram.Findings, f)
		for i := range r.Diagram.Tracks {
			if t := findSnapStep(ws.Steps, r.Diagram.Tracks[i].ID); t != nil && holds(*t, f.Bead) {
				r.Diagram.Tracks[i].Findings = append(r.Diagram.Tracks[i].Findings, f)
			}
		}
	}
}

// drawn is the step id as drawn anywhere in the diagram (nested ones and its
// tracks' too), nil when it is not drawn.
func (d *Diagram) drawn(id string) *DiagramStep {
	if d == nil {
		return nil
	}
	var find func([]DiagramStep) *DiagramStep
	find = func(steps []DiagramStep) *DiagramStep {
		for i := range steps {
			if steps[i].ID == id {
				return &steps[i]
			}
			if c := find(steps[i].Children); c != nil {
				return c
			}
		}
		return nil
	}
	if s := find(d.Steps); s != nil {
		return s
	}
	for i := range d.Tracks {
		if s := d.Tracks[i].drawn(id); s != nil {
			return s
		}
	}
	return nil
}

// gateTitleProblem: a greenlight (the user's approval) reads "Greenlight
// <doing it>"; every other checkpoint says what will be true, in the past
// tense. A question or an order is never a checkpoint.
func gateTitleProblem(s SnapStep) string {
	// The Confirm done step is Commander's own, titled by it: never a finding.
	if s.ClosingGate {
		return ""
	}
	t := strings.TrimSpace(plainTitle(s.Title))
	lower := strings.ToLower(t)
	green := strings.HasPrefix(lower, "greenlight")
	switch {
	case strings.HasSuffix(t, "?"):
		return "the title is a question; a checkpoint names the outcome"
	case green && !hasUser(s.Approvers):
		return "it is titled as a greenlight, but the user does not approve it"
	case green:
		return ""
	case imperative(lower):
		return "the title is an order; a checkpoint says what will be true, in the past tense"
	case !pastTense(lower):
		if hasUser(s.Approvers) {
			return "the user approves it, so it reads \"Greenlight <doing it>\", or what will be true in the past tense (\"Spec approved\")"
		}
		return "a checkpoint says what will be true, in the past tense (\"PR approved\", \"Fix deployed to production\")"
	}
	return ""
}

var imperativeVerbs = []string{"approve ", "review ", "merge ", "deploy ", "ship ", "run ", "check ", "decide ", "confirm ", "sign off", "get "}

func imperative(t string) bool {
	return slices.ContainsFunc(imperativeVerbs, func(v string) bool { return strings.HasPrefix(t, v) })
}

// pastTense: some word reads as a past participle or a settled state.
func pastTense(t string) bool {
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if (len(w) > 3 && strings.HasSuffix(w, "ed")) || slices.Contains(settledWords, w) {
			return true
		}
	}
	return false
}

var settledWords = []string{"done", "built", "run", "met", "green", "live", "ready", "set", "shown", "written", "chosen", "known", "drawn", "given", "taken", "out", "clean", "passed", "held", "signed", "sent", "made", "found", "kept", "won", "read"}

// blockCycles are the loops in the blocks edges among the beads under steps.
func blockCycles(steps []SnapStep) [][]string {
	in := map[string][]string{}
	for s := range stepValues(steps) {
		in[s.BeadID] = s.BlockedBy
	}
	return cycles(in)
}

// cycles are the loops in a waits-on graph, each once, as the ids in the loop.
func cycles(in map[string][]string) [][]string {
	state := map[string]int{}
	var stack []string
	var out [][]string
	seen := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		state[id] = 1
		stack = append(stack, id)
		for _, b := range in[id] {
			if _, ok := in[b]; !ok || b == id {
				continue
			}
			switch state[b] {
			case 0:
				visit(b)
			case 1:
				loop := slices.Clone(stack[slices.Index(stack, b):])
				key := slices.Clone(loop)
				slices.Sort(key)
				if k := strings.Join(key, ","); !seen[k] {
					seen[k] = true
					out = append(out, loop)
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = 2
	}
	ids := make([]string, 0, len(in))
	for id := range in {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if state[id] == 0 {
			visit(id)
		}
	}
	return out
}

func findSnapStep(steps []SnapStep, id string) *SnapStep {
	for i := range steps {
		if steps[i].BeadID == id {
			return &steps[i]
		}
		if s := findSnapStep(steps[i].Children, id); s != nil {
			return s
		}
	}
	return nil
}

// Sub is the diagram of the track id inside d, or nil.
func (d *Diagram) Sub(id string) *Diagram {
	if d == nil {
		return nil
	}
	for i := range d.Tracks {
		if d.Tracks[i].ID == id {
			return &d.Tracks[i]
		}
	}
	return nil
}

var actorWords = map[string]string{ActorYou: "You", ActorManager: "Manager", ActorOperators: "Operators", ActorReviewers: "Reviewers", ActorAutomatic: "Automatic", "": "Nobody"}

// DiagramLines is a diagram as text for agents: the same steps, lanes and
// words the app draws, its tracks after it, then its findings.
func DiagramLines(d *Diagram) []string {
	if d == nil {
		return []string{"no diagram: the workstream has no steps"}
	}
	var out []string
	var one func(d Diagram, depth int)
	one = func(d Diagram, depth int) {
		pad := strings.Repeat("  ", depth)
		out = append(out, pad+d.Title+" ("+d.ID+")")
		var lines func(steps []DiagramStep, pad string)
		lines = func(steps []DiagramStep, pad string) {
			for _, s := range steps {
				mark := "  "
				if s.ID == d.Current {
					mark = "▶ "
				}
				lane := actorWords[s.Actor]
				if s.Who != "" {
					lane += ", " + s.Who
				}
				title := s.Title
				if s.Gate {
					title = "[gate, " + orNobody(s.Approvers) + "] " + title
				}
				if s.Track {
					title = "[workstream] " + title
				}
				if s.Review != nil {
					title = "[review block] " + title
				}
				l := pad + mark + "[" + lane + "] " + title + ": " + s.Text
				if s.Progress != "" {
					l += " · " + s.Progress
				}
				l += " (" + s.ID + ")"
				if len(s.After) > 0 {
					l += " after " + strings.Join(s.After, ", ")
				}
				out = append(out, l)
				if s.Gate && s.Kind != "done" {
					out = append(out, pad+"      passes when: "+orDash(s.Criteria))
				}
				if s.Review != nil {
					out = append(out, ReviewCheckLines(s.Review, pad+"      ", "")...)
				}
				lines(s.Children, pad+"    ")
			}
		}
		lines(d.Steps, pad)
		for _, t := range d.Tracks {
			out = append(out, "")
			one(t, depth+1)
		}
	}
	one(*d, 0)
	if len(d.Findings) > 0 {
		out = append(out, "", fmt.Sprintf("%d findings (cmdr graph check %s):", len(d.Findings), d.ID))
		out = append(out, FindingLines(d.Findings)...)
	}
	return out
}

// FindingLines are findings as text, each with its fix.
func FindingLines(fs []DiagramFinding) []string {
	var out []string
	for _, f := range fs {
		tag := f.Check
		if f.Advisory {
			tag += ", advisory"
		}
		out = append(out, fmt.Sprintf("  - [%s] %s %q: %s. Fix: %s", tag, f.Bead, f.Title, f.Why, f.Fix))
	}
	return out
}

func orNobody(s string) string {
	if s == "" {
		return "nobody"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "(no criteria)"
	}
	return s
}

// CrossWaitSuffix marks the step drawn for a wait on another track's step.
const CrossWaitSuffix = "#xwait"

// crossWaits are s's open blockers in another track (or the workstream
// outside its track), each drawn as a step "Waiting on workstream 2.2 (...): <its title>"
// on the line of whoever has that step's move.
func (f flow) crossWaits(s SnapStep) []DiagramStep {
	home := f.trackOf(s.BeadID)
	if home == "" || home == s.BeadID {
		// The workstream's own level draws every track, so nothing there is elsewhere.
		return nil
	}
	var out []DiagramStep
	for _, id := range s.BlockedBy {
		b, ok := f.byID[id]
		if !ok || id == s.BeadID || stepClosed(b) || parked(b) || b.Superseded || f.trackOf(id) == home {
			continue
		}
		where := "the workstream"
		if t := f.trackOf(id); t != "" {
			where = "workstream " + f.trackName(t)
		}
		line := humanWait(f.leafLine(b), f.b.tasks)
		h := f.leafHand(b)
		kind := line.kind
		if kind == "idle" || kind == "done" {
			kind = "waiting"
		}
		out = append(out, DiagramStep{ID: s.BeadID + CrossWaitSuffix + ":" + id, Title: "Waiting on " + where + ": " + plainTitle(b.Title),
			Kind: kind, Text: "Waiting on " + where + " (" + line.text + ")", Lane: laneOfActor(h.actor), Actor: h.actor, Who: h.who})
	}
	return out
}

// openInTrack: s also waits on an open step of its own track, so that step,
// drawn before it, is why it has not started; the other track's wait is not.
func (f flow) openInTrack(s SnapStep) bool {
	home := f.trackOf(s.BeadID)
	for _, id := range s.BlockedBy {
		if b, ok := f.byID[id]; ok && id != s.BeadID && f.trackOf(id) == home && !stepClosed(b) && !parked(b) && !b.Superseded {
			return true
		}
	}
	return false
}

// trackOf is the track a step sits in (its nearest track ancestor, itself
// included), or "" for a step of the workstream outside every track.
func (f flow) trackOf(id string) string {
	for at := id; at != ""; at = f.parent[at] {
		if s, ok := f.byID[at]; ok && s.Track {
			return at
		}
	}
	return ""
}

// trackName is a nested workstream's sidebar number and title ("2.2 (Mail DNS fix)").
func (f flow) trackName(id string) string {
	if n, ok := trackNumbers(f.ws.Steps)[id]; ok {
		return strconv.Itoa(f.ws.N) + n + " (" + plainTitle(f.byID[id].Title) + ")"
	}
	return "(" + plainTitle(f.byID[id].Title) + ")"
}
