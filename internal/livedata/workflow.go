package livedata

import (
	"slices"
	"strconv"
	"strings"
)

// Workflow is a workstream's (or track's) plan as the user reads it: every
// step in plan order with the same status words the board uses, where it is
// now, and what approving each open user decision starts. The app and
// `cmdr board --workflow` render it as is.
type Workflow struct {
	// Current is the step being decided (the ask NEEDS YOU leads with), else
	// where work is now, else the next gate.
	Current string `json:"current,omitempty"`
	// Unlocks is what approving Current starts (WorkflowStep.Unlocks of it).
	Unlocks []string       `json:"unlocks,omitempty"`
	Steps   []WorkflowStep `json:"steps"`
}

// WorkflowStep is one step. Kind is the board's colour class (needs red,
// waiting yellow, working green, the rest grey) or "done"; Text the words.
type WorkflowStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Since int64  `json:"since,omitempty"`
	Why   string `json:"why,omitempty"` // as BoardRow.Why
	Gate  bool   `json:"gate,omitempty"`
	Who   string `json:"who,omitempty"` // who approves a gate: "you", a reviewer, an agent
	// CardOn: a gate whose open card sits on an ancestor (a track) that
	// asks about it: the bead holding the card.
	CardOn string `json:"card_on,omitempty"`
	Track  bool   `json:"track,omitempty"`
	// Current: on a step with children, its own current step (as Workflow.Current).
	Current string `json:"current,omitempty"`
	// Unlocks: on an open decision, the open steps approving it starts, in
	// the order they can start, through the next gate (included).
	Unlocks  []string       `json:"unlocks,omitempty"`
	Children []WorkflowStep `json:"children,omitempty"` // left out once the step is done
	// Review: a plugged review block, drawn as one step whose checks open on a click.
	Review *ReviewBlock `json:"review,omitempty"`
}

// workflow derives ws's workflow and its diagram. lines holds the board's
// lines for its open tracks, so a track reads exactly as its sidebar row.
func (b board) workflow(ws SnapWorkstream, manager SnapAgent, hasManager bool, lines map[string]line, pickingUp func(line) line) (*Workflow, *Diagram) {
	if len(ws.Steps) == 0 {
		return nil, nil
	}
	byID, parent, order := indexSteps(ws.Steps)
	f := flow{b: b, ws: ws, byID: byID, parent: parent, order: order, manager: manager, hasManager: hasManager, lines: lines, pickingUp: pickingUp}
	w := &Workflow{Steps: f.steps(ws.Steps)}
	w.Current = f.current(ws)
	if c, ok := byID[w.Current]; ok && (c.Gate || f.decision(c)) {
		w.Unlocks = f.unlocks(w.Current)
	}
	return w, f.diagram(w)
}

// indexSteps maps every step of a tree by id, with its parent and plan order.
func indexSteps(steps []SnapStep) (byID map[string]SnapStep, parent map[string]string, order []string) {
	byID, parent = map[string]SnapStep{}, map[string]string{}
	var index func(up string, steps []SnapStep)
	index = func(up string, steps []SnapStep) {
		for _, s := range steps {
			if _, dup := byID[s.BeadID]; !dup {
				order = append(order, s.BeadID)
			}
			byID[s.BeadID] = s
			if up != "" {
				parent[s.BeadID] = up
			}
			index(s.BeadID, s.Children)
		}
	}
	index("", steps)
	return byID, parent, order
}

type flow struct {
	b          board
	ws         SnapWorkstream
	byID       map[string]SnapStep
	parent     map[string]string
	order      []string
	manager    SnapAgent
	hasManager bool
	lines      map[string]line
	pickingUp  func(line) line
	// xdrawn: the id each other-track step's wait is drawn as in this diagram (one card per wait).
	xdrawn map[string]string
}

func (f flow) steps(steps []SnapStep) []WorkflowStep {
	out := []WorkflowStep{}
	for _, s := range shown(steps) {
		if s.Superseded {
			continue
		}
		out = append(out, f.step(s))
	}
	return out
}

func (f flow) step(s SnapStep) WorkflowStep {
	w := WorkflowStep{ID: s.BeadID, Title: plainTitle(s.Title), Gate: s.Gate, Track: s.Track}
	if s.Track {
		w.Title = trackTitle(w.Title)
	}
	if s.Gate {
		w.Who = approverNames(s.Approvers)
	}
	l, isTrack := f.lines[s.BeadID]
	if open := !stepClosed(s) && !parked(s); open && (isTrack || f.container(s)) {
		w.Children = f.steps(s.Children)
		if !isTrack {
			l = f.pickingUp(f.b.trackLine(f.ws, s, f.manager, f.hasManager, f.asksUnder(s)))
		}
		w.Current = f.current(trackWorkstream(f.ws, s))
	} else {
		l = f.leafLine(s)
	}
	if rb := f.reviewBlock(s); rb != nil {
		w.Review = rb
		l = reviewLine(rb, stepClosed(s), l)
	}
	w.Kind, w.Text, w.Since, w.Why = l.kind, l.text, l.since, l.why
	if !isTrack {
		w.Text = withCardWarning(w.Text, f.b.stepWarning(s))
	}
	w.CardOn = f.b.heldCard[f.ws.Project+"\x00"+s.BeadID]
	if f.decision(s) {
		w.Unlocks = f.unlocks(s.BeadID)
	}
	return w
}

// container: s has open work under it, so it reads as its children do.
func (f flow) container(s SnapStep) bool {
	return slices.ContainsFunc(shown(s.Children), func(c SnapStep) bool { return !c.Superseded && !parked(c) && !stepClosed(c) })
}

func (f flow) asksUnder(s SnapStep) int {
	n := 0
	for c := range stepValues(s.Children) {
		if f.b.needsIDs[c.BeadID] {
			n++
		}
	}
	return n
}

// decision: an open step NEEDS YOU lists as the user's (a flagged step or a user gate).
func (f flow) decision(s SnapStep) bool {
	if stepClosed(s) || parked(s) || !f.b.needsIDs[s.BeadID] {
		return false
	}
	if _, live := f.b.liveBeforeDecision(s); live {
		return false // a live Operator is still on the work it covers
	}
	return onUser(s) || (s.Gate && hasUser(s.Approvers))
}

// leafLine is a step's own state, by who has to act, in the board's words.
func (f flow) leafLine(s SnapStep) line {
	ws := f.ws
	ws.BeadID, ws.Steps = s.BeadID, []SnapStep{s}
	return recoveryLine(f.leafBaseLine(s), ws)
}

func (f flow) leafBaseLine(s SnapStep) line {
	if stepClosed(s) {
		return line{kind: "done", text: "Done"}
	}
	if parked(s) {
		if s.ParkedUntil != 0 {
			return line{kind: "idle", text: "Parked until " + clock(s.ParkedUntil, f.b.now, f.b.loc)}
		}
		return line{kind: "idle", text: "Parked"}
	}
	if p, ok := f.b.noPitch[f.ws.Project+"\x00"+s.BeadID]; ok && p.Proposed != "" {
		if p.ProposedAt > 0 && f.b.now-p.ProposedAt >= pitchDeadline {
			return line{kind: "stalled", text: "Proposed " + p.Proposed + " not reviewed: Commander's move", since: p.ProposedAt}
		}
		return line{kind: "waiting", text: "Commander reviewing the proposed " + p.Proposed, since: p.ProposedAt}
	} else if ok {
		if p.AskedAt > 0 && f.b.now-p.AskedAt >= pitchDeadline {
			return line{kind: "stalled", text: "Pitch still not written: the Manager's move", since: p.AskedAt}
		}
		return line{kind: "waiting", text: "Manager writing the pitch", since: p.AskedAt}
	}
	you := func(w string) bool { return w == "you" }
	if _, held := f.b.heldCard[f.ws.Project+"\x00"+s.BeadID]; held && !unfinishedBlocker(s, f.byID) {
		return line{kind: "needs", text: "Needs you", since: waitSince([]SnapStep{s}, you)}
	}
	if f.decision(s) {
		if unfinishedBlocker(s, f.byID) {
			return f.notYetAsked(s)
		}
		return line{kind: "needs", text: "Needs you", since: waitSince([]SnapStep{s}, you)}
	}
	if l, ok := resultHandoff([]SnapStep{s}, f.b.now); ok {
		return l
	}
	switch s.OwnerState {
	case "died":
		return line{kind: "stalled", text: "Agent stopped: the Manager's move", since: s.OwnerSince, why: s.OwnerWhy}
	case "finished":
		return line{kind: "stalled", text: "Agent finished, step not closed: the Manager's move", since: s.OwnerSince, why: s.OwnerWhy}
	}
	if (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s)) || liveTask(s) {
		return line{kind: "working", text: "Agent working"}
	}
	if f.ws.NowStep == s.BeadID && agentBusy(f.manager, f.hasManager) {
		return line{kind: "working", text: "Manager working", since: f.manager.Since}
	}
	if r := s.ReviewFact; r != nil {
		switch {
		case r.Acted:
			return line{kind: "stalled", text: r.Text + ": the Manager's move"}
		case r.NoPR:
			return line{kind: "stalled", text: "Waiting for a review of no PR: nothing can clear it"}
		}
		return line{kind: "waiting", text: "Waiting for review: " + r.Text}
	}
	var waits []string
	for _, w := range strings.Split(s.Waiting, " · ") {
		// A time wait whose time has passed holds nothing: its owner was woken.
		if w != "" && !s.waitExpired(f.b.now, w) {
			waits = append(waits, w)
		}
	}
	if l, ok := f.b.waitLine(waits, []SnapStep{s}); ok {
		return humanWait(l, f.b.tasks)
	}
	if onUser(s) && !(unfinishedBlocker(s, f.byID) || f.blockedAbove(s)) {
		return line{kind: "needs", text: "Needs you", since: waitSince([]SnapStep{s}, you)}
	}
	if unfinishedBlocker(s, f.byID) {
		return f.notYetAsked(s)
	}
	if f.blockedAbove(s) {
		if s.Gate {
			return line{kind: "idle", text: "Not reached"}
		}
		return line{kind: "idle", text: "Not started"}
	}
	if s.Gate {
		return line{kind: "waiting", text: "Waiting for " + approverNames(s.Approvers)}
	}
	if s.Broken {
		return line{kind: "stalled", text: "State missing"}
	}
	if s.State == "in-progress" {
		return line{kind: "stalled", text: "Stalled"}
	}
	return line{kind: "idle", text: "Not started"}
}

// notYetAsked is a step with an unfinished blocker: a gate is not reached yet,
// and a card waits on its blocker before it is the user's move.
func (f flow) notYetAsked(s SnapStep) line {
	if s.Gate {
		return line{kind: "idle", text: "Not reached"}
	}
	if !onUserCard(s) {
		return line{kind: "idle", text: "Not started"}
	}
	for _, id := range s.BlockedBy {
		if b, ok := f.byID[id]; ok && id != s.BeadID && !stepClosed(b) && !parked(b) {
			return line{kind: "waiting", text: "Waiting on " + liveTitle(b.Title)}
		}
	}
	return line{kind: "idle", text: "Not started"}
}

// blockedAbove: a container s sits in waits on open work, so s cannot start yet.
func (f flow) blockedAbove(s SnapStep) bool {
	for up := f.parent[s.BeadID]; up != ""; up = f.parent[up] {
		if p, ok := f.byID[up]; ok && unfinishedBlocker(p, f.byID) {
			return true
		}
	}
	return false
}

// current is the step being decided in ws (the ask the board's line leads
// with), else the step work is on now, else ws's next gate.
func (f flow) current(ws SnapWorkstream) string {
	if g := gateVMOf(ws, f.b.needsIDs, 0); g != nil && g.at && g.forYou {
		return g.beadID
	}
	asks, _ := f.b.heldByLive(userDecisions(ws, f.b.needsIDs))
	if _, ready := boardNeedsYou(ws, asks, f.b.s.NeedsYou); len(ready) > 0 {
		return ready[0].BeadID
	}
	open := func(id string) bool {
		s, ok := f.byID[id]
		return ok && !stepClosed(s) && !parked(s)
	}
	in := func(id string) bool {
		return id == ws.BeadID || findSnapStep(ws.Steps, id) != nil
	}
	if ws.NowStep != "" && open(ws.NowStep) && in(ws.NowStep) {
		return ws.NowStep
	}
	for s := range stepValues(ws.Steps) {
		if !stepClosed(s) && !parked(s) && !s.Superseded && !f.container(s) && (s.State == "in-progress" || (s.Status == "in_progress" && !s.Gate) || liveTask(s) || (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s))) {
			return s.BeadID
		}
	}
	// The next gate, but never one still waiting on open work: that is not
	// where the work is.
	if g := gateStatusOf(ws); g != nil && !unfinishedBlocker(g.gate, f.byID) && !f.blockedAbove(g.gate) {
		return g.gate.BeadID
	}
	return ""
}

// unlocks is the open work that approving id starts: steps whose every open
// blocker (their own or a container's they sit in) is id or already started
// by it, found in rounds so the list reads in the order the work can begin.
// A gate it reaches is listed and stops the run: nothing after it starts
// until that gate is approved too.
func (f flow) unlocks(id string) []string {
	var out []string
	for _, x := range unlockRun(f.byID, f.parent, f.order, id, false) {
		// Only what the user sees as work: a container is read through its steps.
		if !f.container(f.byID[x]) {
			out = append(out, x)
		}
	}
	return out
}

// unlockRun is every open step approving id starts (containers included), in
// the rounds unlocks describes. withClosed reads the run as it went for an
// approved id: id and the closed steps it started still start what is after
// them, and those closed steps are in the run too.
func unlockRun(byID map[string]SnapStep, parent map[string]string, order []string, id string, withClosed bool) []string {
	inPlay := func(x string) bool {
		if _, ok := byID[x]; !ok {
			return false
		}
		// A parked or dropped step takes everything under it out of play.
		for at := x; at != ""; at = parent[at] {
			if p := byID[at]; parked(p) || p.Superseded {
				return false
			}
		}
		return true
	}
	openStep := func(x string) bool { return inPlay(x) && !stepClosed(byID[x]) }
	blockers := func(x string) []string {
		var out []string
		for at := x; at != ""; at = parent[at] {
			if s, ok := byID[at]; ok {
				out = append(out, s.BlockedBy...)
			}
		}
		return out
	}
	started := map[string]bool{} // in the run; gates in it stop it
	var out []string
	for {
		var round []string
		for _, x := range order {
			if x == id || started[x] || !(openStep(x) || withClosed && inPlay(x)) {
				continue
			}
			triggered, free := false, true
			for _, d := range blockers(x) {
				live := openStep(d) || withClosed && (d == id || started[d])
				if d == x || !live {
					continue
				}
				if d == id || (started[d] && !byID[d].Gate) {
					triggered = true
					continue
				}
				free = false
				break
			}
			if triggered && free {
				round = append(round, x)
			}
		}
		if len(round) == 0 {
			break
		}
		for _, x := range round {
			started[x] = true
			out = append(out, x)
		}
	}
	return out
}

// approverNames is who approves a gate, the user as "you".
func approverNames(approvers []string) string {
	var who []string
	for _, a := range approvers {
		if a == "user" {
			a = "you"
		}
		if !slices.Contains(who, a) {
			who = append(who, a)
		}
	}
	return strings.Join(who, ", ")
}

// WorkflowLines is a workflow as text, one line per step, for agents: the
// same steps and words the app shows. Done steps under a done step are left out.
func WorkflowLines(w *Workflow) []string {
	if w == nil {
		return []string{"no workflow: the workstream has no steps"}
	}
	unlocked := map[string]bool{}
	for _, id := range w.Unlocks {
		unlocked[id] = true
	}
	var out []string
	var walk func(steps []WorkflowStep, depth int)
	walk = func(steps []WorkflowStep, depth int) {
		for _, s := range steps {
			mark := "  "
			switch {
			case s.ID == w.Current:
				mark = "▶ "
			case unlocked[s.ID]:
				mark = "+ "
			}
			title := s.Title
			if s.Gate {
				title = "[gate, " + s.Who + "] " + title
			}
			if s.Review != nil {
				out = append(out, mark+strings.Repeat("  ", depth)+"[review block, "+s.Review.progress()+"] "+title+": "+s.Text+" ("+s.ID+")")
				out = append(out, ReviewCheckLines(s.Review, strings.Repeat("  ", depth+2), w.Current)...)
				continue
			}
			out = append(out, mark+strings.Repeat("  ", depth)+title+": "+s.Text+" ("+s.ID+")")
			walk(s.Children, depth+1)
		}
	}
	walk(w.Steps, 0)
	if w.Current != "" {
		out = append(out, "", "▶ = now ("+w.Current+")")
	}
	if len(w.Unlocks) > 0 {
		out = append(out, "+ = approving it starts these "+strconv.Itoa(len(w.Unlocks))+", through the next gate")
	}
	return out
}

// Sub is the workflow of the step id inside w (a track), with that step's
// own current step; nil when w has no such step with work under it.
func (w *Workflow) Sub(id string) *Workflow {
	if w == nil {
		return nil
	}
	s := findWorkflowStep(w.Steps, id)
	if s == nil || len(s.Children) == 0 {
		return nil
	}
	sub := &Workflow{Current: s.Current, Steps: s.Children}
	if c := findWorkflowStep(s.Children, s.Current); c != nil {
		sub.Unlocks = c.Unlocks
	}
	return sub
}

func findWorkflowStep(steps []WorkflowStep, id string) *WorkflowStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
		if s := findWorkflowStep(steps[i].Children, id); s != nil {
			return s
		}
	}
	return nil
}
