package livedata

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Display is the board as the user reads it: Commander's line, the NEEDS YOU
// list and every workstream row with its tracks. The sidebar renders it as it
// is and the agents read the same lines (`cmdr board`, the check-in), so
// there is one derivation of what a row says, here.
type Display struct {
	Commander   BoardCommander `json:"commander"`
	Asks        []BoardAsk     `json:"asks"`
	Workstreams []BoardRow     `json:"workstreams"`
}

// BoardCommander is Commander's row.
type BoardCommander struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Since  int64  `json:"since"`
	Unread int    `json:"unread"`
}

// BoardAsk is one NEEDS YOU item; TrackLetter places it ("#2.3").
type BoardAsk struct {
	Key         string `json:"key"`
	Text        string `json:"text"`
	Rank        int    `json:"rank"`
	Workstream  string `json:"workstream"`
	Track       string `json:"track"`
	TrackLetter string `json:"track_letter"`
	WsKey       string `json:"ws_key"`
	// Stale is the reason code when the card is out of date (see CardStaleIn);
	// StaleWhy says what changed. The card stays answerable.
	Stale    string `json:"stale,omitempty"`
	StaleWhy string `json:"stale_why,omitempty"`
}

// Progress is units of work (leaf beads, not gates) done of all.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// BoardGate is a row's next approval gate.
type BoardGate struct {
	BeadID string `json:"bead_id"`
	Title  string `json:"title"`
	Who    string `json:"who"`
	At     bool   `json:"at"` // only the approval is left
	ForYou bool   `json:"for_you"`
}

// BoardRow is one workstream row. Kind is the state's colour class (needs red,
// waiting yellow, working green, the rest grey); Text never carries an age:
// Since is when the state began (0 when unknown), and readers format the age.
type BoardRow struct {
	Key         string       `json:"key"`
	N           int          `json:"n"`
	Title       string       `json:"title"`
	Project     string       `json:"project"`
	BeadID      string       `json:"bead_id"`
	ManagerRole string       `json:"manager_role"`
	Starred     bool         `json:"starred"`
	Kind        string       `json:"kind"`
	Text        string       `json:"text"`
	Since       int64        `json:"since"`
	Progress    Progress     `json:"progress"`
	Milestones  Progress     `json:"milestones"`
	Working     int          `json:"working"` // its Manager in a turn and its live Operators
	Flag        int          `json:"flag"`    // NEEDS YOU items under it
	Unread      int          `json:"unread"`
	Gate        *BoardGate   `json:"gate"`
	Tracks      []BoardTrack `json:"tracks"`               // its open nested workstreams; empty unless it has two or more
	Scope       string       `json:"scope,omitempty"`      // the one open nested workstream standing in for this row: its title, and where a click lands
	ScopeRole   string       `json:"scope_role,omitempty"` // that stand-in's own Manager, when it has one (nested Managers on)
	Why         string       `json:"why,omitempty"`        // when the row is not what the beads last said: what cmdrd saw instead
	AgeAs       string       `json:"age_as,omitempty"`     // the state its age runs under when that is not Kind|Text
	Workflow    *Workflow    `json:"workflow,omitempty"`
	Diagram     *Diagram     `json:"diagram,omitempty"`
}

// BoardTrack is a nested workstream's sub-row. Letter is its number after the
// row's ("#3" + ".1.2"). Like the row, one with a single open child shows that
// child: its bead, title and children.
type BoardTrack struct {
	Key        string       `json:"key"`
	BeadID     string       `json:"bead_id"`
	Letter     string       `json:"letter"`
	Children   []BoardTrack `json:"children,omitempty"`
	Title      string       `json:"title"`
	Kind       string       `json:"kind"`
	Text       string       `json:"text"`
	Since      int64        `json:"since"`
	Progress   Progress     `json:"progress"`
	Milestones Progress     `json:"milestones"`
	Working    int          `json:"working"` // its live Operators
	Why        string       `json:"why,omitempty"`
	AgeAs      string       `json:"age_as,omitempty"`
	// ManagerRole is set when the nested workstream has a Manager of its own.
	ManagerRole string `json:"manager_role,omitempty"`
}

// AllTracks is every nested workstream the row shows, each before its children.
func (r BoardRow) AllTracks() []BoardTrack {
	var out []BoardTrack
	var walk func([]BoardTrack)
	walk = func(ts []BoardTrack) {
		for _, t := range ts {
			out = append(out, t)
			walk(t.Children)
		}
	}
	walk(r.Tracks)
	return out
}

// line is a derived status line; why says what the line rests on when it is
// not what the beads last said (a claim on an agent that is not running).
type line struct {
	kind, text string
	since      int64
	why        string
	ageAs      string // the state its age runs under, when two texts are one state
}

// ageState is the state the line's age runs under (what sinceTracker keys on).
func (l line) ageState() string {
	if l.ageAs != "" {
		return l.ageAs
	}
	return l.kind + "|" + l.text
}

// Board derives the display from a snapshot at nowSec. Clock times in wait
// lines are in BoardZone (the [user] timezone, else the process zone).
func Board(s Snapshot, nowSec int64) Display {
	b := newBoard(s, nowSec)
	s = b.s
	d := Display{Asks: []BoardAsk{}, Workstreams: []BoardRow{}}
	for _, ws := range s.Workstreams {
		d.Workstreams = append(d.Workstreams, b.row(ws))
	}
	wsKeys := map[string]string{}
	for _, ws := range s.Workstreams {
		wsKeys[ws.Project+"\x00"+ws.Title] = ws.Project + ":" + ws.BeadID
	}
	for _, a := range s.NeedsYou {
		text := a.Title
		if a.Decision != nil && a.Decision.Question != "" {
			text = a.Decision.Question
		} else if a.Ask != "" {
			text = a.Ask
		}
		stale, staleWhy := CardStaleIn(a, s, nowSec)
		if stale == StaleSpec {
			staleWhy = "" // kept as the reason code for the Manager's re-check mail, never shown
		}
		d.Asks = append(d.Asks, BoardAsk{Key: "ask:" + a.Project + ":" + a.BeadID, Text: text, Rank: a.Rank, Workstream: a.Workstream,
			Track: a.Track, TrackLetter: b.trackLetterOf(a), WsKey: wsKeys[a.Project+"\x00"+a.Workstream], Stale: stale, StaleWhy: staleWhy})
	}
	cmd, ok := s.Agents["commander"]
	l := agentLine(cmd, ok)
	if _, watched := s.Watch["commander"]; l.kind == "idle" && watched {
		l.text = "monitoring"
	}
	d.Commander = BoardCommander{Kind: l.kind, Text: l.text, Since: cmd.Since, Unread: s.Unread["commander"]}
	return d
}

// newBoard reads the snapshot's facts into a board, with claims corrected by liveness.
func newBoard(s Snapshot, nowSec int64) board {
	s = withLiveness(s)
	s = withoutIDTitles(s)
	s.NeedsYou = slices.Clone(s.NeedsYou)
	for i := range s.NeedsYou {
		// A spec edit only re-checks the card with its Manager (the stale mail);
		// Managers reword steps all the time, so the user is not shown it.
		if reason, why := CardStaleIn(s.NeedsYou[i], s, nowSec); reason != StaleSpec {
			s.NeedsYou[i].StaleWhy = why
		}
	}
	b := board{s: s, now: nowSec, loc: BoardZone(), needsIDs: map[string]bool{}, asks: map[string]int{}, trackAsks: map[string]int{}, noPitch: map[string]NoPitch{}, heldCard: map[string]string{}}
	for _, p := range s.NoPitch {
		b.noPitch[p.Project+"\x00"+p.BeadID] = p
	}
	for _, a := range s.NeedsYou {
		b.needsIDs[a.BeadID] = true
		b.asks[a.Project+"\x00"+a.Workstream]++
		if a.TrackID != "" {
			b.trackAsks[a.Project+"\x00"+a.TrackID]++
		}
	}
	b.tasks = operatorTaskTitles(s)
	b.byID = map[string]SnapStep{}
	var index func([]SnapStep)
	index = func(steps []SnapStep) {
		for _, st := range steps {
			b.byID[st.BeadID] = st
			index(st.Children)
		}
	}
	for _, w := range s.Workstreams {
		index(w.Steps)
	}
	b.rowOf = rowLabels(s)
	b.adoptHeldCards()
	return b
}

type board struct {
	s               Snapshot
	now             int64
	loc             *time.Location
	needsIDs        map[string]bool
	asks, trackAsks map[string]int
	noPitch         map[string]NoPitch // project\x00bead: ready asks whose pitch the Manager has not written
	heldCard        map[string]string  // project\x00gate: the ancestor whose open card is this gate's ask
	tasks           map[string]string
	byID            map[string]SnapStep     // every step of every workstream by bead id
	steps           map[string]SnapStep     // the row's workstream, every bead by id: a track's blockers sit outside it
	rowOf           map[string]string       // every step by bead id: the sidebar label of the row it sits in ("#2.2", "#2")
	openGates       map[string]SnapGateNode // the row's workstream, standalone gate beads not yet passed: they are no steps
}

// rowLabels maps each step to the row that shows it: its nested workstream ("#2.2") or the workstream ("#2").
func rowLabels(s Snapshot) map[string]string {
	out := map[string]string{}
	for _, ws := range s.Workstreams {
		base := "#" + strconv.Itoa(ws.N)
		if ws.N == 0 {
			base = ws.Title
		}
		numbers := trackNumbers(ws.Steps)
		var mark func(steps []SnapStep, label string)
		mark = func(steps []SnapStep, label string) {
			for _, st := range steps {
				here := label
				if n, ok := numbers[st.BeadID]; ok {
					here = base + n
				}
				out[st.BeadID] = here
				mark(st.Children, here)
			}
		}
		mark(ws.Steps, base)
	}
	return out
}

func (b board) trackLetterOf(a NeedsYou) string {
	if a.TrackID == "" {
		return ""
	}
	for _, ws := range b.s.Workstreams {
		if ws.Project == a.Project && ws.Title == a.Workstream {
			return trackNumbers(ws.Steps)[a.TrackID]
		}
	}
	return ""
}

func (b board) row(ws SnapWorkstream) BoardRow {
	b.steps = map[string]SnapStep{}
	for _, s := range flattenSteps(ws.Steps) {
		b.steps[s.BeadID] = s
	}
	asks := b.asks[ws.Project+"\x00"+ws.Title]
	manager, hasManager := b.s.Agents[ws.ManagerRole]
	reacting := managerReacting(b.s, ws.ManagerRole)
	pickingUp := func(l line) line {
		if l.kind == "stalled" && reacting {
			l = line{kind: "working", text: "Manager picking it up", why: l.why, ageAs: l.ageAs}
		}
		return humanWait(l, b.tasks)
	}
	byTrack := map[string]line{}
	level := b.trackLevel(ws, ws.Steps, "", manager, hasManager, pickingUp, byTrack)
	l := pickingUp(b.workstreamLine(ws, manager, hasManager, asks))
	if l.kind == "quiet" || l.kind == "stalled" || l.kind == "idle" {
		if hidden, ok := b.hiddenOperator(ws); ok {
			l = line{kind: "working", text: "Operator working", why: "Its step was closed while it still runs (" + hidden + ")"}
		}
	}
	l = rollUp(l, level, ownOpen(ws.Steps))
	// One open nested workstream with nothing beside it stands in for the row.
	title := titleOf(ws.Title, ws.BeadID)
	scope, scopeRole := "", ""
	tracks := level.rows
	if _, sole := soleChild(ws.Steps); sole {
		title, scope, scopeRole, tracks = tracks[0].Title, tracks[0].BeadID, tracks[0].ManagerRole, tracks[0].Children
	}
	if tracks == nil {
		tracks = []BoardTrack{}
	}
	r := BoardRow{Key: ws.Project + ":" + ws.BeadID, N: ws.N, Title: title, Scope: scope, ScopeRole: scopeRole, Project: ws.Project, BeadID: ws.BeadID, ManagerRole: ws.ManagerRole, Starred: ws.Starred, Kind: l.kind, Text: l.text, Since: l.since, Why: l.why, AgeAs: l.ageAs,
		Progress: progressOf(ws), Milestones: DeliveryMilestones(ws), Working: workingAgents(ws, manager, hasManager), Flag: asks, Unread: b.s.Unread[ws.ManagerRole], Tracks: tracks}
	r.Workflow, r.Diagram = b.workflow(ws, manager, hasManager, byTrack, pickingUp)
	b.addNeedsYouLane(ws, &r)
	if g := gateVMOf(ws, b.needsIDs, asks); g != nil {
		r.Gate = &BoardGate{BeadID: g.beadID, Title: g.title, Who: g.who, At: g.at, ForYou: g.forYou}
	}
	return r
}

// trackLevel is one level's open nested workstreams: their sub-rows and lines.
type trackLevel struct {
	rows  []BoardTrack
	lines []line
	names []string
	live  []bool
}

// trackLevel builds the sub-rows of steps' open nested workstreams, each number
// following prefix. Every line goes in byTrack, a collapsed parent's too.
func (b board) trackLevel(ws SnapWorkstream, steps []SnapStep, prefix string, manager SnapAgent, hasManager bool, pickingUp func(line) line, byTrack map[string]line) trackLevel {
	var out trackLevel
	_, sole := soleChild(steps)
	for i, t := range openTracksIn(steps) {
		number := prefix + trackLetter(i)
		if sole {
			number = prefix
		}
		var chain []string
		for {
			c, only := soleChild(t.Children)
			if !only {
				break
			}
			chain = append(chain, t.BeadID)
			t = c
		}
		tws := trackWorkstream(ws, t)
		own := b.trackLine(ws, t, manager, hasManager, b.asksIn(ws.Project, t))
		kids := b.trackLevel(ws, t.Children, number, manager, hasManager, pickingUp, byTrack)
		l := rollUp(pickingUp(own), kids, ownOpen(t.Children))
		for _, id := range append(chain, t.BeadID) {
			byTrack[id] = l
		}
		out.lines = append(out.lines, l)
		out.names = append(out.names, clip(strings.Join(strings.Fields(plainTitle(trackTitle(t.Title))), " "), needsTitleMax))
		out.live = append(out.live, own.kind == "working" && workingOperators(tws) > 0) // before pickingUp: the Manager's turn does not make a stalled track live
		out.rows = append(out.rows, BoardTrack{Key: ws.Project + ":" + t.BeadID, BeadID: t.BeadID, Letter: number, Title: trackTitle(t.Title), Children: kids.rows,
			Kind: l.kind, Text: l.text, Since: l.since, Progress: progressOf(tws), Milestones: DeliveryMilestones(tws), Working: workingOperators(tws), Why: l.why, AgeAs: l.ageAs,
			ManagerRole: t.ManagerRole})
	}
	return out
}

// rollUp is a parent's line given the nested workstreams under it: its most urgent one.
func rollUp(l line, level trackLevel, ownWork bool) line {
	if len(level.rows) == 0 {
		return l
	}
	cands, names, live := level.lines, level.names, level.live
	if ownWork {
		cands = append([]line{l}, cands...)
		names = append([]string{""}, names...)
		live = append([]bool{false}, live...)
	}
	// Items that sit in nested workstreams: the row names the first card and counts them all, not one's.
	if pick, ok := mostUrgent(cands); ok && !(pick.kind == "needs" && l.kind == "needs") {
		return liveOverWaiting(pick, cands, names, live)
	}
	return l
}

// asksIn counts the NEEDS YOU items under t, at any depth.
func (b board) asksIn(project string, t SnapStep) int {
	n := b.trackAsks[project+"\x00"+t.BeadID]
	for _, c := range t.Children {
		if c.Track {
			n += b.asksIn(project, c)
		}
	}
	return n
}

// managerReacting: the Manager is in a turn or has mail coming, so a stalled line reads "Manager picking it up".
func managerReacting(s Snapshot, role string) bool {
	manager, ok := s.Agents[role]
	return s.Unread[role] > 0 || agentBusy(manager, ok)
}

// trackLine is a track's line; the Manager counts as working on it only while its current step is inside.
func (b board) trackLine(ws SnapWorkstream, t SnapStep, manager SnapAgent, hasManager bool, asks int) line {
	here := holds(t, ws.NowStep)
	agent := manager
	if hasManager && !here && agentBusy(manager, true) {
		agent.State, agent.Wait = store.AgentIdle, ""
	}
	tws := trackWorkstream(ws, t)
	tws.NowStep = ""
	if here {
		tws.NowStep = ws.NowStep
	}
	return b.workstreamLine(tws, agent, hasManager, asks)
}

func holds(s SnapStep, id string) bool {
	if id == "" {
		return false
	}
	if s.BeadID == id {
		return true
	}
	for _, c := range s.Children {
		if holds(c, id) {
			return true
		}
	}
	return false
}

// workstreamLine is the status line with respect to the next approval gate; without gates it is managerLine.
func (b board) workstreamLine(ws SnapWorkstream, agent SnapAgent, hasAgent bool, asks int) line {
	l := b.workstreamBaseLine(ws, agent, hasAgent, asks)
	if l.kind != "needs" {
		for _, s := range flattenSteps(ws.Steps) {
			if !stepClosed(s) && !s.Superseded && ((s.Waiting != "" && s.Waiting != heldElsewhere) || parked(s)) {
				if why := pushWhy(s.PushCount, s.PushFirst); why != "" {
					l.text = withCardWarning(l.text, why)
					break
				}
			}
		}
	}
	return l
}
func (b board) workstreamBaseLine(ws SnapWorkstream, agent SnapAgent, hasAgent bool, asks int) line {
	g := gateVMOf(ws, b.needsIDs, asks)
	if g == nil {
		p := b.managerLine(ws, agent, hasAgent)
		if startText(p.text) && maskedBroken(ws.Steps) {
			p = line{kind: "stalled", text: "State missing"}
		}
		return b.brokenBeside(ws, p)
	}
	active := agentBusy(agent, hasAgent) || g.operators > 0
	open, held := b.heldByLive(userDecisions(ws, b.needsIDs))
	decisionText, decisions := boardNeedsYou(ws, open, b.s.NeedsYou)
	var p line
	// An open user decision anywhere in the workstream outranks working, waiting and ready.
	if len(decisions) > 0 && !(g.at && g.forYou) {
		p = line{kind: "needs", text: decisionText, since: waitSince(decisions, func(w string) bool { return w == "you" })}
	} else if l, ok := b.pitchLine(ws, active); ok && !(g.at && g.forYou) {
		p = l
	} else {
		p = b.gatePhase(ws, g, active)
		// The ask the Operator's work covers is not the user's yet: it reads as that work.
		if held != nil && p.kind == "needs" && !(g.at && g.forYou) {
			p = held.line()
		} else if held != nil && p.kind == "working" && p.why == "" {
			p.why = held.why()
		}
	}
	// Nobody on it and no recorded wait is missing state, not a status.
	if p.text == "Stalled" && hasBroken(ws.Steps) {
		return line{kind: "stalled", text: "State missing"}
	}
	// A live Operator's work has its own age; the Manager's turn does not restart it.
	if p.since == 0 && agentBusy(agent, hasAgent) && p.kind == "working" && workingOperators(ws) == 0 {
		p.since = agent.Since
	}
	if handoffText(p.text) || p.text == "Stalled" || p.text == "State missing" {
		// A receipt names the Manager's move; it never hides a step nothing holds beside it.
		if l, ok := resultHandoff(ws.Steps, b.now); ok {
			p = l
		}
		if startText(p.text) && maskedBroken(ws.Steps) {
			p = line{kind: "stalled", text: "State missing"}
		}
	}
	return b.brokenBeside(ws, p)
}

// brokenBeside: a live Operator does not make the open work nothing holds beside it true, so that stays
// the row's line, the same whatever the Manager is doing; why keeps the live work in view. Its age is the
// live work's (ageState), so it runs through the Manager's turns as "Manager picking it up", and through a
// Manager-owned step that is broken only while the Manager is idle.
func (b board) brokenBeside(ws SnapWorkstream, l line) line {
	if broken := brokenSteps(ws.Steps); len(broken) > 0 && l.kind == "working" && workingOperators(ws) > 0 {
		return line{kind: "stalled", text: "State missing", why: brokenWhy(b.liveSteps(ws), broken), ageAs: l.ageState()}
	}
	return l
}

// maskedBroken: a step nothing holds has no Manager custody receipt, so a
// "Manager to start" line (startText) would hide it. A broken step that
// carries an (overdue) receipt is told better by the receipt's own line. A
// reported Operator's "Manager to close" keeps leading its row as before
// (the reported gap's known limit).
func maskedBroken(steps []SnapStep) bool {
	return slices.ContainsFunc(brokenSteps(steps), func(s SnapStep) bool { return !holdsHandoff(s) })
}

func hasBroken(steps []SnapStep) bool {
	return len(brokenSteps(steps)) > 0
}

func brokenSteps(steps []SnapStep) []SnapStep {
	var out []SnapStep
	for _, s := range flattenSteps(steps) {
		if s.Broken {
			out = append(out, s)
		}
	}
	return out
}

// brokenWhy says what the row's State missing hides: the live work, and the first step nothing holds.
func brokenWhy(live, broken []SnapStep) string {
	t := workingText(live) + "; nothing holds " + liveTitle(broken[0].Title)
	if len(broken) > 1 {
		t += " +" + strconv.Itoa(len(broken)-1) + " more"
	}
	return t
}

var ciPrefix = regexp.MustCompile(`^(main )?CI\s*`)

// gatePhase is what a workstream is doing with respect to its next gate.
func (b board) gatePhase(ws SnapWorkstream, g *gateVM, active bool) line {
	gate := lower(g.title)
	if g.at && g.forYou {
		return line{kind: "needs", text: "Needs you · " + gate}
	}
	if g.at {
		if gateOperator(ws, g) {
			return line{kind: "working", text: "Readying for " + gate}
		}
		if g.gateWait != "" {
			return line{kind: "waiting", text: "Waiting for " + g.gateWait}
		}
		return line{kind: "waiting", text: "Waiting for " + gate}
	}
	// An Operator on the gate's own work reads as readying; a Manager merely being busy does not
	// outrank a recorded external wait (else the row flips with the Manager's chat state).
	if g.operators > 0 {
		return b.workingLine(ws, g, gate)
	}
	// Everything else is one derivation, so the row never depends on the Manager's chat state;
	// only "nothing recorded" differs: a busy Manager is the one doing the work. A live Operator off the
	// gate's path is work too, and it reads the same whatever the Manager is doing.
	if l, ok := goneLine(g.units); ok {
		return l
	}
	if l, ok := b.liveWaitLine(g); ok {
		return l
	}
	if g.forYou || g.asks > 0 || b.userWait(g) {
		return line{kind: "needs", text: "Needs you"}
	}
	if active || workingOperators(ws) > 0 {
		return b.workingLine(ws, g, gate)
	}
	return b.idleLine(ws)
}

// liveSteps are the steps work is on now: those a live Operator is on first, then in flow.current's order the
// workstream's current step and open in-progress leaves. A step whose Operator stopped, that a recorded wait
// holds, or that nothing holds is not work under way.
func (b board) liveSteps(ws SnapWorkstream) []SnapStep {
	var flat []SnapStep
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, s := range steps {
			if !parked(s) { // parked subtrees are out of play
				flat = append(flat, s)
				walk(s.Children)
			}
		}
	}
	walk(ws.Steps)
	var out []SnapStep
	add := func(s SnapStep) {
		if !operatorOn(s) && (!stepAlive(s) || b.waitHolds(s) || s.Broken) {
			return
		}
		if !slices.ContainsFunc(out, func(o SnapStep) bool { return o.BeadID == s.BeadID }) {
			out = append(out, s)
		}
	}
	if ws.NowStep != "" {
		for _, s := range flat {
			if s.BeadID == ws.NowStep && !stepClosed(s) && !parked(s) && !s.Superseded && !s.Gate {
				// A stopped or held task says nothing about its step, unless an Operator is on that step.
				if up := servedStep(ws.Steps, s); operatorOn(up) || (stepAlive(s) && !b.waitHolds(s) && !s.Broken) {
					add(up)
				}
			}
		}
	}
	for _, s := range flat {
		// An Operator task bead is agent-internal: the step it belongs to is the live one.
		leaf := !slices.ContainsFunc(shown(s.Children), func(c SnapStep) bool { return !stepClosed(c) && !parked(c) })
		if servedStep(ws.Steps, s).BeadID == s.BeadID && !stepClosed(s) && !parked(s) && !s.Superseded && !s.Gate &&
			(operatorOn(s) || (leaf && !stoppedTask(s) && (s.State == "in-progress" || s.Status == "in_progress"))) {
			add(s)
		}
	}
	slices.SortStableFunc(out, func(x, y SnapStep) int {
		if operatorOn(x) == operatorOn(y) {
			return 0
		}
		if operatorOn(x) {
			return -1
		}
		return 1
	})
	return out
}

// servedStep is the step a row names for s: a task bead is agent-internal, so the step it serves. A task
// with none in the level (directly under a track or workstream), or under a gate or a superseded or closed
// bead, which are not work under way, names itself.
func servedStep(steps []SnapStep, s SnapStep) SnapStep {
	if up := userStep(steps, s); !up.Gate && !up.Superseded && !stepClosed(up) {
		return up
	}
	return s
}

// stoppedTask: an open task bead under s whose Operator stopped, so s reads in progress only from that claim.
func stoppedTask(s SnapStep) bool {
	return slices.ContainsFunc(s.Children, func(c SnapStep) bool { return c.Task && !stepClosed(c) && !parked(c) && !stepAlive(c) })
}

// operatorOn: a live Operator owns s, or a task bead under it.
func operatorOn(s SnapStep) bool {
	return liveTask(s) || (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s))
}

// waitHolds: s has a recorded wait that still holds (a time wait past its time does not).
func (b board) waitHolds(s SnapStep) bool {
	return slices.ContainsFunc(strings.Split(s.Waiting, " · "), func(w string) bool {
		return w != "" && !s.waitExpired(b.now, w)
	})
}

// liveTask: a live Operator task bead under s. The task is agent-internal, so its
// step reads and is drawn as the Operator's own (row, box line, lane, Current).
func liveTask(s SnapStep) bool {
	return !s.Gate && slices.ContainsFunc(s.Children, func(c SnapStep) bool {
		return c.Task && strings.HasPrefix(c.Owner, "operator-") && !stepClosed(c) && !parked(c) && stepAlive(c) && (c.Status == "in_progress" || c.OwnerState == "working")
	})
}

// workingText names the live step ("Working on X", "+N more" for the rest); "Working" when none is known.
func workingText(live []SnapStep) string {
	if len(live) == 0 {
		return "Working"
	}
	t := "Working on " + liveTitle(live[0].Title)
	if len(live) > 1 {
		t += " +" + strconv.Itoa(len(live)-1) + " more"
	}
	return t
}

// workingLine's age: the first live step's, so the Manager claiming or clearing another step does not restart it.
func liveAgeAs(live []SnapStep, suffix string) string {
	if len(live) < 2 {
		return "" // the text says the same
	}
	return "working|" + workingText(live[:1]) + suffix
}

// liveTitle is a live step's title on a row: lower-cased, no ticket ids, whitespace collapsed to one line, cut like a needs title.
func liveTitle(raw string) string {
	return clip(strings.Join(strings.Fields(lower(plainTitle(raw))), " "), needsTitleMax)
}

// gateOperator: a live Operator owns the gate bead itself.
func gateOperator(ws SnapWorkstream, g *gateVM) bool {
	return slices.ContainsFunc(flattenSteps(ws.Steps), func(s SnapStep) bool {
		return s.BeadID == g.beadID && strings.HasPrefix(s.Owner, "operator-") && stepAlive(s)
	})
}

// workingLine is the row of a workstream whose work is under way toward gate: the live step first, the
// gate only as "then: <gate>". Only an Operator on the gate bead itself is readying that gate.
func (b board) workingLine(ws SnapWorkstream, g *gateVM, gate string) line {
	if gateOperator(ws, g) {
		return line{kind: "working", text: "Readying for " + gate}
	}
	live, then := b.liveSteps(ws), " · then: "+gate
	return line{kind: "working", text: workingText(live) + then, ageAs: liveAgeAs(live, then)}
}

// userWait reports a live wait on the user recorded under the gate's work.
func (b board) userWait(g *gateVM) bool {
	for _, s := range g.waitBeads {
		for _, w := range strings.Split(s.Waiting, " · ") {
			if w == "you" && !s.waitExpired(b.now, w) {
				return true
			}
		}
	}
	return false
}

// liveWaitLine is the line for the gate's recorded waits that still hold (a time wait past its time does not).
func (b board) liveWaitLine(g *gateVM) (line, bool) {
	var waits []string
	var beads []SnapStep
	for _, s := range g.waitBeads {
		for _, w := range strings.Split(s.Waiting, " · ") {
			if w != "" && !s.waitExpired(b.now, w) {
				waits = append(waits, w)
				beads = append(beads, s)
			}
		}
	}
	return b.waitLine(waits, beads)
}

// waitLine reads recorded waits, CI first; "you" is not one (NEEDS YOU says what is the user's).
func (b board) waitLine(waits []string, beads []SnapStep) (line, bool) {
	var ci []string
	for _, w := range waits {
		if isCIWait(w) {
			if n := ciPrefix.ReplaceAllString(w, ""); !slices.Contains(ci, n) {
				ci = append(ci, n)
			}
		}
	}
	if len(ci) > 0 {
		text := "Waiting for CI"
		if len(ci) > 1 {
			text = "Waiting for CI on " + strconv.Itoa(len(ci)) + " PRs"
		}
		return line{kind: "waiting", text: text, since: waitSince(beads, isCIWait)}, true
	}
	for _, w := range waits {
		if w != "you" {
			return line{kind: "waiting", text: b.waitText(beads, w), since: waitSince(beads, func(x string) bool { return x == w })}, true
		}
	}
	return line{}, false
}

// idleLine is the line when nobody is on the work: what it waits on (the
// user, or any wait recorded anywhere under it), Stalled only when nothing.
func (b board) idleLine(ws SnapWorkstream) line {
	var open []SnapStep
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, s := range steps {
			if parked(s) || s.Superseded {
				continue
			}
			if !stepClosed(s) {
				open = append(open, s)
			}
			walk(s.Children)
		}
	}
	walk(ws.Steps)
	outside := b.outsideBlockers(ws, open)
	if s, ok := b.blockedOnYou(ws, append(slices.Clone(open), outside...)); ok {
		var card *NeedsYou
		for i := range b.s.NeedsYou {
			if b.s.NeedsYou[i].BeadID == s.BeadID {
				card = &b.s.NeedsYou[i]
				break
			}
		}
		return line{kind: "needs", text: "Needs you: " + needsTitle(s, card), since: waitSince([]SnapStep{s}, func(w string) bool { return w == "you" })}
	}
	var waits []string
	var beads []SnapStep
	for _, s := range open {
		for _, w := range strings.Split(s.Waiting, " · ") {
			// A time wait whose time has passed holds nothing: its owner was woken.
			if w != "" && !s.waitExpired(b.now, w) {
				waits = append(waits, w)
				beads = append(beads, s)
			}
		}
	}
	if l, ok := b.waitLine(waits, beads); ok {
		return l
	}
	if l, ok := resultHandoff(ws.Steps, b.now); ok {
		return l
	}
	// Something held on a blocker in another row (track or workstream) holds this row, but the row
	// names who it waits on, never the blocker's own line.
	for _, s := range outside {
		held := strings.HasPrefix(s.Owner, "operator-") && stepAlive(s) && !onUser(s)
		since := int64(0)
		for _, w := range strings.Split(s.Waiting, " · ") {
			if w != "" && w != "you" && !s.waitExpired(b.now, w) {
				held = true
				since = waitSince([]SnapStep{s}, func(x string) bool { return x == w })
				break
			}
		}
		if held {
			text := "Waiting on " + b.rowOf[s.BeadID]
			if b.rowOf[s.BeadID] == "" {
				text = "Waiting on another row"
			}
			return line{kind: "waiting", text: text + " (" + liveTitle(s.Title) + ")", since: since}
		}
	}
	return line{kind: "stalled", text: "Stalled"}
}

// outsideBlockers are the open, unparked steps of other workstreams that the
// row's open steps (and, for a track, its own blockers) wait on through blocks
// edges, transitively: their recorded waits and live Operators hold the row too.
func (b board) outsideBlockers(ws SnapWorkstream, open []SnapStep) []SnapStep {
	// Outside = not under this row (a track row's subtree, so a sibling track
	// is outside it).
	inside := map[string]bool{}
	for _, s := range flattenSteps(ws.Steps) {
		inside[s.BeadID] = true
	}
	var queue []SnapStep
	if root, ok := b.byID[ws.BeadID]; ok {
		queue = append(queue, root)
	}
	queue = append(queue, open...)
	seen := map[string]bool{}
	var out []SnapStep
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, id := range cur.BlockedBy {
			far, ok := b.byID[id]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			if parked(far) || far.Superseded || stepClosed(far) {
				continue
			}
			if !inside[id] {
				out = append(out, far)
			}
			queue = append(queue, far)
		}
	}
	return out
}

// blockedOnYou is the open item NEEDS YOU lists that this work waits on,
// directly or through blockers nobody is on; a track's own blockers count.
func (b board) blockedOnYou(ws SnapWorkstream, open []SnapStep) (SnapStep, bool) {
	from := open
	if root, ok := b.steps[ws.BeadID]; ok {
		from = append([]SnapStep{root}, open...)
	}
	seen := map[string]bool{}
	var find func(ids []string) (SnapStep, bool)
	find = func(ids []string) (SnapStep, bool) {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			s, ok := b.steps[id]
			if !ok || stepClosed(s) || parked(s) || s.Superseded {
				continue
			}
			if b.needsIDs[id] && (onUser(s) || (s.Gate && hasUser(s.Approvers))) {
				return s, true
			}
			held := s.Waiting != "" || s.State == "in-progress" || (strings.HasPrefix(s.Owner, "operator-") && stepAlive(s))
			if !held {
				if hit, ok := find(s.BlockedBy); ok {
					return hit, true
				}
			}
		}
		return SnapStep{}, false
	}
	for _, s := range from {
		if hit, ok := find(s.BlockedBy); ok {
			return hit, true
		}
	}
	return SnapStep{}, false
}

// pitchDeadline is how long a Manager has to write a ready ask's pitch, from
// cmdrd's mail, before the row says it is late.
const pitchDeadline = 30 * 60

// pitchLine: a ready ask with no pitch yet is the Manager's move (writing it),
// never the user's and never Stalled; past pitchDeadline the row says so. A
// proposed ask is Commander's move (accept or overrule) on the same clock.
func (b board) pitchLine(ws SnapWorkstream, busy bool) (line, bool) {
	ids := []string{ws.BeadID}
	for _, s := range flattenSteps(ws.Steps) {
		if !stepClosed(s) && !parked(s) {
			ids = append(ids, s.BeadID)
		}
	}
	for _, id := range ids {
		p, ok := b.noPitch[ws.Project+"\x00"+id]
		if !ok {
			continue
		}
		what := lower(plainTitle(p.Title))
		if p.Proposed != "" {
			if p.ProposedAt > 0 && b.now-p.ProposedAt >= pitchDeadline {
				return line{kind: "stalled", text: "Proposed " + p.Proposed + " on " + what + " not reviewed: Commander's move", since: p.ProposedAt}, true
			}
			return line{kind: "waiting", text: "Commander reviewing the proposed " + p.Proposed + " on " + what, since: p.ProposedAt}, true
		}
		if p.AskedAt > 0 && b.now-p.AskedAt >= pitchDeadline {
			return line{kind: "stalled", text: "Pitch for " + what + " still not written: the Manager's move", since: p.AskedAt}, true
		}
		kind := "waiting"
		if busy {
			kind = "working"
		}
		return line{kind: kind, text: "Manager writing the pitch for " + what, since: p.AskedAt}, true
	}
	return line{}, false
}

// goneLine is the line for open work whose Operator exited: the Manager's move.
func goneLine(units []SnapStep) (line, bool) {
	for _, s := range units {
		if !stepClosed(s) && s.OwnerState == "died" {
			return line{kind: "stalled", text: "Agent stopped on " + lower(plainTitle(s.Title)) + ": the Manager's move", since: s.OwnerSince, why: s.OwnerWhy}, true
		}
	}
	for _, s := range units {
		if !stepClosed(s) && s.OwnerState == "finished" {
			return line{kind: "stalled", text: "Agent finished " + lower(plainTitle(s.Title)) + ", step not closed: the Manager's move", since: s.OwnerSince, why: s.OwnerWhy}, true
		}
	}
	return line{}, false
}

// notPlannedText is the line of a workstream or track with no step yet.
const notPlannedText = "Not started: Manager to plan"

// hasPlan: steps hold at least one planned step, open or done. Superseded steps
// and an agent's task beads are no plan.
func hasPlan(steps []SnapStep) bool {
	return slices.ContainsFunc(steps, func(s SnapStep) bool { return !s.Superseded && !s.Task })
}

// openWork is the open steps under steps and, of those, the leaves; parked subtrees are out of play.
func openWork(steps []SnapStep) (open, leaves []SnapStep) {
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, s := range steps {
			if parked(s) {
				continue
			}
			if !stepClosed(s) {
				open = append(open, s)
			}
			walk(s.Children)
		}
	}
	walk(steps)
	for _, s := range open {
		// Parked children are out of play: a step with only those is work of its own.
		if !slices.ContainsFunc(s.Children, func(c SnapStep) bool { return !stepClosed(c) && !parked(c) }) {
			leaves = append(leaves, s)
		}
	}
	return open, leaves
}

// stalledWork is the line for open work nothing will move until the Manager does.
func stalledWork(open, leaves []SnapStep) (line, bool) {
	if l, ok := goneLine(leaves); ok {
		return l, true
	}
	// Review waits read GitHub's live answer, not what an agent last wrote. A
	// wait can sit on a track or step with open children (the approval on the
	// track, the sign-off step under it), so every open step is read, not just leaves.
	for _, s := range open {
		if s.ReviewFact != nil && s.ReviewFact.Acted {
			return line{kind: "stalled", text: s.ReviewFact.Text + ": the Manager's move"}, true
		}
	}
	if slices.ContainsFunc(open, func(s SnapStep) bool { return s.ReviewFact != nil && s.ReviewFact.NoPR }) {
		return line{kind: "stalled", text: "Waiting for a review of no PR: nothing can clear it"}, true
	}
	return line{}, false
}

// managerLine is a workstream's state when it has no gates, by who has to act.
func (b board) managerLine(ws SnapWorkstream, agent SnapAgent, hasAgent bool) line {
	live := agentLine(agent, hasAgent)
	if live.kind == "none" {
		return line{kind: "none", text: "No Manager yet"}
	}
	return b.managerState(ws, agent, live)
}

func (b board) managerState(ws SnapWorkstream, agent SnapAgent, live line) line {
	open, leaves := openWork(ws.Steps)
	onYou := userDecisions(ws, b.needsIDs)
	for _, s := range leaves {
		if _, unwritten := b.noPitch[ws.Project+"\x00"+s.BeadID]; !unwritten && !b.needsIDs[s.BeadID] && (s.State == "needs-user" || s.Waiting == "you") {
			onYou = append(onYou, s)
		}
	}
	onYou, held := b.heldByLive(onYou)
	timed := func(kind, text string, match func(string) bool) line {
		return line{kind: kind, text: text, since: waitSince(leaves, match)}
	}
	if len(onYou) > 0 {
		text, _ := boardNeedsYou(ws, onYou, b.s.NeedsYou)
		if text == "" {
			text = "Needs you"
		}
		return timed("needs", text, func(w string) bool { return w == "you" })
	}
	if live.kind == "stuck" {
		return line{kind: "stalled", text: "Stuck"}
	}
	if l, ok := b.pitchLine(ws, agentBusy(agent, true)); ok {
		return l
	}
	operators := workingOperators(ws) > 0
	if busy := agentBusy(agent, true); busy || operators {
		// Name the work (the step it is on), not the tool it is running.
		steps := b.liveSteps(ws)
		l := line{kind: "working", text: workingText(steps), ageAs: liveAgeAs(steps, "")}
		if busy && !operators {
			l.since = agent.Since
		}
		if held != nil {
			l.why = held.why()
		}
		return l
	}
	if held != nil {
		return held.line()
	}
	if l, ok := stalledWork(open, leaves); ok {
		return l
	}
	for _, s := range open {
		if s.ReviewFact != nil && !s.ReviewFact.Acted {
			return line{kind: "waiting", text: "Waiting for review: " + s.ReviewFact.Text}
		}
	}
	if handoff, ok := resultHandoff(ws.Steps, b.now); ok {
		return handoff
	}
	for _, s := range leaves {
		if w := s.Waiting; w != "" && w != "you" {
			return timed("waiting", b.waitText(leaves, w), func(x string) bool { return x == w })
		}
	}
	if slices.ContainsFunc(leaves, func(s SnapStep) bool { return s.Review != "" && (s.State == "in-progress" || s.State == "review") }) {
		return line{kind: "waiting", text: "Waiting for review"}
	}
	if len(leaves) == 0 {
		if l, ok := b.ownWaitLine(ws); ok {
			return l
		}
		if t := b.parkedReturn(ws.Steps); t != 0 {
			return line{kind: "quiet", text: "Parked work returns " + clock(t, b.now, b.loc)}
		}
		// No step was ever planned (a workstream or track just made): planning is the Manager's move.
		if !hasPlan(ws.Steps) {
			return line{kind: "idle", text: notPlannedText}
		}
		return line{kind: "quiet", text: "Nothing to do"}
	}
	l := b.idleLine(ws)
	// An open step's state wins; the row's own wait only reads when that is quiet.
	if l.kind == "quiet" {
		if o, ok := b.ownWaitLine(ws); ok {
			return o
		}
	}
	// Open work nobody holds is broken data (no agent, no recorded wait), not a status.
	if l.kind == "stalled" && slices.ContainsFunc(leaves, func(s SnapStep) bool { return s.Broken }) {
		return line{kind: "stalled", text: "State missing"}
	}
	return l
}

// agentLine is the one-line live state of an agent.
func agentLine(a SnapAgent, ok bool) line {
	if !ok {
		return line{kind: "none", text: "no agent yet"}
	}
	switch a.State {
	case store.AgentWorking:
		return line{kind: "working", text: "working"}
	case store.AgentWaiting:
		if a.Wait != "" {
			return line{kind: "waiting", text: "waiting · " + a.Wait}
		}
		d := a.Detail
		if d == "" {
			d = "background tasks"
		}
		return line{kind: "waiting", text: "waiting on " + d}
	case store.AgentStuck:
		if a.Detail != "" {
			return line{kind: "stuck", text: "stuck: " + a.Detail}
		}
		return line{kind: "stuck", text: "stuck"}
	}
	return line{kind: "idle", text: "idle"}
}

// waitText is the line for a wait someone else must finish: "Waiting for review", "Waiting for reconcile · 09:45 PM".
func (b board) waitText(steps []SnapStep, w string) string {
	if w == beads.ManagerCloseWait {
		return resultReviewText
	}
	if w == beads.ManagerPlanWait {
		return managerPlanningText
	}
	if w == beads.ManagerRecoverWait {
		return "Manager recovering"
	}
	for _, s := range steps {
		if u, ok := s.WaitUntils[w]; ok {
			return "Waiting for " + w + " · " + clock(u, b.now, b.loc)
		}
		if s.WaitUntil != 0 && s.WaitUntilAs == w {
			return "Waiting for " + w + " · " + clock(s.WaitUntil, b.now, b.loc)
		}
	}
	return "Waiting for " + waitName(w)
}

// clock is a unix time as the app's clock reads it (en-US) in loc: "09:45 PM", with the weekday when it is not the day of now.
func clock(unix, now int64, loc *time.Location) string {
	t, n := time.Unix(unix, 0).In(loc), time.Unix(now, 0).In(loc)
	if t.YearDay() == n.YearDay() && t.Year() == n.Year() {
		return t.Format("03:04 PM")
	}
	return t.Format("Mon 03:04 PM")
}

// waitName is what a wait waits for, without the who: "review (reviewer-a)" -> "review", "main CI 349a7f4" -> "CI".
func waitName(w string) string {
	switch {
	case strings.HasPrefix(w, "prod tag"):
		return "prod tag"
	case strings.HasPrefix(w, "main CI"):
		return "CI"
	}
	if i := strings.IndexFunc(w, func(r rune) bool { return unicode.IsSpace(r) || r == '(' }); i >= 0 {
		return w[:i]
	}
	return w
}

func isCIWait(w string) bool { return ciWait.MatchString(w) }

var ciWait = regexp.MustCompile(`^(main )?CI\b`)

// waitSince is when the oldest of these beads' waits matching match began (unix s, 0 when none).
func waitSince(beads []SnapStep, match func(string) bool) int64 {
	var at int64
	for _, b := range beads {
		if b.WaitSince != 0 && slices.ContainsFunc(strings.Split(b.Waiting, " · "), match) && (at == 0 || b.WaitSince < at) {
			at = b.WaitSince
		}
	}
	return at
}

var operatorWait = regexp.MustCompile(`^Waiting for (operator-[\w.-]+)`)

// humanWait names a wait on an Operator by what it is doing, never by its internal name.
func humanWait(l line, tasks map[string]string) line {
	m := operatorWait.FindStringSubmatch(l.text)
	if m == nil {
		return l
	}
	task, ok := tasks[m[1]]
	if !ok {
		task, ok = tasks[strings.ReplaceAll(m[1], ".", "-")]
	}
	with := "Waiting for an Operator"
	if ok {
		with += ": " + lower(task)
	}
	l.text = with + l.text[len(m[0]):]
	return l
}

// operatorTaskTitles maps each Operator (by its owner label) to the step it is on.
func operatorTaskTitles(s Snapshot) map[string]string {
	out := map[string]string{}
	for _, ws := range s.Workstreams {
		for _, st := range flattenSteps(ws.Steps) {
			if strings.HasPrefix(st.Owner, "operator-") && st.Status != "closed" {
				out[st.Owner] = plainTitle(st.Title)
			}
		}
	}
	return out
}

// urgency orders lines by who acts: red, yellow, green; grey only when nothing is moving.
var urgency = map[string]int{"needs": 0, "waiting": 1, "working": 2, "stuck": 3, "stalled": 4, "idle": 5, "quiet": 6, "none": 7}

func mostUrgent(ls []line) (line, bool) {
	if len(ls) == 0 {
		return line{}, false
	}
	best := ls[0]
	for _, l := range ls[1:] {
		if urgency[l.kind] < urgency[best.kind] {
			best = l
		}
	}
	return best, true
}

// liveOverWaiting: a track with live Operators at work leads a row whose most
// urgent line only waits (a waiting sibling does not make the workstream wait);
// the line then names what waits beside it. Needs you still leads.
func liveOverWaiting(pick line, cands []line, names []string, live []bool) line {
	if pick.kind != "waiting" {
		return pick
	}
	at := slices.Index(live, true)
	if at < 0 {
		return pick
	}
	var others []string
	for i, c := range cands {
		if i != at && c.kind == "waiting" {
			others = append(others, besideText(names[i], c.text))
		}
	}
	out := cands[at]
	out.ageAs = out.ageState() // the sibling's wait changing is no new state of the live work
	out.text += " · " + others[0]
	if len(others) > 1 {
		out.text += " +" + strconv.Itoa(len(others)-1) + " more"
	}
	return out
}

// besideText is a waiting sibling named after the live work: "Ship waiting for CI", "Ship: Manager writing the pitch ...".
func besideText(name, text string) string {
	switch {
	case name == "":
		return lower(text)
	case strings.HasPrefix(text, "Waiting "):
		return name + " " + lower(text)
	}
	return name + ": " + text
}

// openTracks are a workstream's nested workstreams still in play, in priority order.
func openTracks(ws SnapWorkstream) []SnapStep { return openTracksIn(ws.Steps) }

func openTracksIn(steps []SnapStep) []SnapStep {
	var out []SnapStep
	for _, s := range steps {
		if s.Track && !parked(s) && !s.Superseded && !stepClosed(s) {
			out = append(out, s)
		}
	}
	return out
}

// ownOpen: steps hold open work that is not a nested workstream. An agent's task bead is not plan work.
func ownOpen(steps []SnapStep) bool {
	return slices.ContainsFunc(steps, func(x SnapStep) bool { return !x.Track && !x.Task && !parked(x) && !stepClosed(x) })
}

// soleChild is the one open nested workstream among steps, when nothing else is
// open beside it. A parent shows only with two or more, so this one stands in for it.
func soleChild(steps []SnapStep) (SnapStep, bool) {
	open := openTracksIn(steps)
	if len(open) != 1 || ownOpen(steps) {
		return SnapStep{}, false
	}
	return open[0], true
}

// trackLetter is a nested workstream's number among its open siblings (first is ".1").
func trackLetter(i int) string { return "." + strconv.Itoa(i+1) }

// trackNumbers maps every open nested workstream to its number after the row's
// (".1.2"). One standing in for its parent takes the parent's number.
func trackNumbers(steps []SnapStep) map[string]string {
	out := map[string]string{}
	var walk func(steps []SnapStep, prefix string)
	walk = func(steps []SnapStep, prefix string) {
		_, sole := soleChild(steps)
		for i, t := range openTracksIn(steps) {
			n := prefix + trackLetter(i)
			if sole {
				n = prefix
			}
			out[t.BeadID] = n
			walk(t.Children, n)
		}
	}
	walk(steps, "")
	return out
}

var typedLetter = regexp.MustCompile(`^[A-Z][.):]\s+`)

// trackTitle drops a letter an agent typed into the title ("A. Script audits"): the letter is built in.
func trackTitle(t string) string { return typedLetter.ReplaceAllString(t, "") }

// trackWorkstream views one track as a workstream of its own, so its line is derived exactly as a row's.
func trackWorkstream(ws SnapWorkstream, t SnapStep) SnapWorkstream {
	ws.BeadID, ws.Title, ws.State, ws.Status, ws.Steps = t.BeadID, t.Title, t.State, t.Status, t.Children
	ws.GateNodes, ws.NoGates, ws.Total, ws.Done = nil, t.NoGates, 0, 0
	ws.Waiting, ws.WaitSince, ws.WaitUntil, ws.WaitUntilAs, ws.WaitUntils = t.Waiting, t.WaitSince, t.WaitUntil, t.WaitUntilAs, t.WaitUntils
	return ws
}

// parked: deferred by the user, out of play until undeferred.
func parked(s SnapStep) bool { return s.Status == statusDeferred }

// onUser is an open, unparked bead that waits on the user.
func onUser(s SnapStep) bool {
	return !parked(s) && !stepClosed(s) && (s.State == "needs-user" || s.Waiting == "you")
}

// onUserCard is onUser, or an open step holding a stored decision card the user answers.
func onUserCard(s SnapStep) bool {
	return onUser(s) || (s.Decision != nil && !parked(s) && !stepClosed(s))
}

func hasUser(approvers []string) bool { return slices.Contains(approvers, "user") }

// userDecisions are the open beads (tracks included) an open user decision
// sits on: flagged beads and user gates NEEDS YOU lists, which is the truth
// for what is the user's.
func userDecisions(ws SnapWorkstream, needsIDs map[string]bool) []SnapStep {
	var out []SnapStep
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, s := range steps {
			if parked(s) {
				continue
			}
			listed := needsIDs == nil || needsIDs[s.BeadID]
			userGate := s.Gate && hasUser(s.Approvers) && needsIDs[s.BeadID]
			if !stepClosed(s) && ((onUser(s) && listed) || userGate) {
				out = append(out, s)
			}
			walk(s.Children)
		}
	}
	walk(ws.Steps)
	return out
}

// unfinishedBlocker: a gate waits on an open, unparked bead; a user gate so blocked is not ready.
func unfinishedBlocker(g SnapStep, byID map[string]SnapStep) bool {
	for _, id := range g.BlockedBy {
		if id == g.BeadID {
			continue
		}
		if b, ok := byID[id]; ok && !stepClosed(b) && !parked(b) {
			return true
		}
	}
	return false
}

const needsTitleMax = 48

// needsTitle is a card's label on the row: its question, else the bead title, no ticket ids, one line, cut at 48 characters.
func needsTitle(s SnapStep, n *NeedsYou) string {
	raw := s.Title
	if n != nil {
		if q := n.decisionQuestion(); q != "" {
			raw = q
		} else if t := strings.TrimSpace(n.Title); t != "" {
			raw = t
		}
	}
	text := clip(strings.Join(strings.Fields(plainTitle(raw)), " "), needsTitleMax)
	if n != nil {
		text = withCardWarning(text, n.StaleWhy)
	}
	return text
}

// clip cuts t to max runes at a word boundary, marking the cut with an ellipsis.
func clip(t string, max int) string {
	r := []rune(t)
	if len(r) <= max {
		return t
	}
	cut := string(r[:max-1])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-") + "…"
}

func (n *NeedsYou) decisionQuestion() string {
	if n.Decision == nil {
		return ""
	}
	return strings.TrimSpace(n.Decision.Question)
}

// needsYouOf names the first ready card (NEEDS YOU order) and counts the
// rest; a blocked user gate is not ready. The text is empty when none is.
func boardNeedsYou(ws SnapWorkstream, decisions []SnapStep, needs []NeedsYou) (string, []SnapStep) {
	byID := map[string]SnapStep{}
	for _, s := range flattenSteps(ws.Steps) {
		byID[s.BeadID] = s
	}
	order := map[string]int{}
	for i := range needs {
		if _, ok := order[needs[i].BeadID]; !ok {
			order[needs[i].BeadID] = i
		}
	}
	var ready []SnapStep
	for _, d := range decisions {
		if !(d.Gate && hasUser(d.Approvers) && unfinishedBlocker(d, byID)) {
			ready = append(ready, d)
		}
	}
	if len(ready) == 0 {
		return "", nil
	}
	rank := func(s SnapStep) int {
		if i, ok := order[s.BeadID]; ok {
			return i
		}
		return len(needs)
	}
	sort.SliceStable(ready, func(i, j int) bool { return rank(ready[i]) < rank(ready[j]) })
	var card *NeedsYou
	if i, ok := order[ready[0].BeadID]; ok {
		card = &needs[i]
	}
	text := "Needs you: " + needsTitle(ready[0], card)
	if len(ready) > 1 {
		text += " (+" + strconv.Itoa(len(ready)-1) + " more)"
	}
	return text, ready
}

// gateStat is a workstream's next gate not yet approved, and the work before it.
type gateStat struct {
	gate          SnapStep
	at            bool // everything it waits on is done: only the approval is left
	done, total   int
	operators     int      // live Operators on its unfinished units
	operatorRoles []string // everyone who claimed them, live or not
	waits         []string // recorded waits on the unfinished work before it
	gateWait      string   // what the gate itself waits on
	waitBeads     []SnapStep
	units         []SnapStep // the work before it
}

// gateStatusOf finds the next gate: a gate waits on the work before it (its
// blocked_by, followed through containers to their leaves and through other
// work, stopping at an approved gate).
func gateStatusOf(ws SnapWorkstream) *gateStat {
	byID := map[string]SnapStep{}
	var ids []string
	var index func([]SnapStep)
	index = func(steps []SnapStep) {
		for _, s := range steps {
			if parked(s) {
				continue
			}
			if _, dup := byID[s.BeadID]; !dup {
				ids = append(ids, s.BeadID)
			}
			byID[s.BeadID] = s
			index(s.Children)
		}
	}
	index(ws.Steps)
	type cand struct {
		g           SnapStep
		units       []SnapStep
		gatesBefore int
		waits       []string
		waitBeads   []SnapStep
	}
	before := func(g SnapStep) cand {
		c := cand{g: g}
		units := map[string]int{} // bead -> index in c.units
		seen := map[string]bool{}
		var visit func(id string)
		visit = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			s, ok := byID[id]
			if !ok {
				return
			}
			if s.Gate && s.BeadID != g.BeadID {
				c.gatesBefore++
				if stepClosed(s) {
					return // an approved gate closes the stretch before it
				}
			}
			if !s.Gate && !stepClosed(s) && s.Waiting != "" {
				c.waits = append(c.waits, strings.Split(s.Waiting, " · ")...)
				c.waitBeads = append(c.waitBeads, s)
			}
			for _, leaf := range workUnits(s) {
				if leaf.Gate {
					continue
				}
				if i, dup := units[leaf.BeadID]; dup {
					c.units[i] = leaf
				} else {
					units[leaf.BeadID] = len(c.units)
					c.units = append(c.units, leaf)
				}
			}
			if !s.Gate {
				for _, leaf := range workUnits(s) {
					for _, d := range leaf.BlockedBy {
						visit(d)
					}
				}
			}
			for _, d := range s.BlockedBy {
				visit(d)
			}
		}
		for _, d := range g.BlockedBy {
			visit(d)
		}
		return c
	}
	var cands []cand
	for _, id := range ids {
		// A superseded gate was dropped from the plan: it is never an approval to wait for.
		if byID[id].Gate && !byID[id].Superseded {
			cands = append(cands, before(byID[id]))
		}
	}
	if len(cands) == 0 {
		return nil
	}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	// Earliest open stage first. A gate that waits on nothing (the workflow
	// gate) only blocks the work after it, so it never takes over from a stage
	// that is waiting on its own work or reviewers.
	stage := func(g SnapStep) int { return b2i(len(g.BlockedBy) == 0) }
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if x, y := b2i(stepClosed(a.g)), b2i(stepClosed(b.g)); x != y {
			return x < y
		}
		if x, y := b2i(onUser(a.g)), b2i(onUser(b.g)); x != y {
			return x > y
		}
		if a.gatesBefore != b.gatesBefore {
			return a.gatesBefore < b.gatesBefore
		}
		return stage(a.g) < stage(b.g)
	})
	i := slices.IndexFunc(cands, func(c cand) bool { return !stepClosed(c.g) })
	if i < 0 {
		return nil
	}
	next := cands[i]
	// A gate with no recorded "before" waits on the workstream's other work.
	units := next.units
	if len(units) == 0 {
		for _, id := range ids {
			if s := byID[id]; !s.Gate && len(s.Children) == 0 {
				units = append(units, s)
			}
		}
	}
	st := &gateStat{gate: next.g, total: len(units), waitBeads: next.waitBeads, units: units}
	live := map[string]bool{}
	for _, u := range units {
		if stepClosed(u) {
			st.done++
			continue
		}
		if strings.HasPrefix(u.Owner, "operator-") {
			if !slices.Contains(st.operatorRoles, u.Owner) {
				st.operatorRoles = append(st.operatorRoles, u.Owner)
			}
			if stepAlive(u) {
				live[u.Owner] = true
			}
		}
	}
	st.at = st.done == st.total
	st.operators = len(live)
	if next.g.Waiting != "you" {
		st.gateWait = next.g.Waiting
	}
	for _, w := range next.waits {
		if !slices.Contains(st.waits, w) {
			st.waits = append(st.waits, w)
		}
	}
	return st
}

// gateVM is a row's view of its next gate.
type gateVM struct {
	beadID     string
	title, who string
	at, forYou bool
	asks       int
	operators  int
	waits      []string
	gateWait   string
	waitBeads  []SnapStep
	units      []SnapStep
}

func gateVMOf(ws SnapWorkstream, needsIDs map[string]bool, asks int) *gateVM {
	g := gateStatusOf(ws)
	if g == nil {
		return nil
	}
	user := hasUser(g.gate.Approvers)
	// A user gate NEEDS YOU does not list (an earlier stage is still open) is
	// not waiting on the user: the row shows the earlier stage's wait instead.
	hidden := user && needsIDs != nil && !needsIDs[g.gate.BeadID]
	who := make([]string, len(g.gate.Approvers))
	for i, a := range g.gate.Approvers {
		who[i] = a
		if a == "user" {
			who[i] = "you"
		}
	}
	return &gateVM{beadID: g.gate.BeadID, title: plainTitle(g.gate.Title), who: strings.Join(who, ", "), at: g.at && !hidden, forYou: user && !hidden, asks: asks,
		operators: g.operators, waits: g.waits, gateWait: g.gateWait, waitBeads: g.waitBeads, units: g.units}
}

// workUnits are the beads that count as work under s: its leaves. A stage
// whose sub-tasks are all closed but which is itself still open is a unit of
// its own (only closing the stage finishes it).
func workUnits(s SnapStep) []SnapStep {
	if parked(s) {
		return nil
	}
	var units []SnapStep
	kids := 0
	for _, c := range s.Children {
		if !parked(c) {
			kids++
			units = append(units, workUnits(c)...)
		}
	}
	if kids == 0 {
		return []SnapStep{s}
	}
	if !s.Gate && s.Status != "closed" && !slices.ContainsFunc(units, func(u SnapStep) bool { return !stepClosed(u) }) {
		return []SnapStep{s}
	}
	return units
}

// progressOf is a workstream's units of work (not gates), done in aggregate.
func progressOf(ws SnapWorkstream) Progress {
	var p Progress
	for _, x := range ws.Steps {
		for _, u := range workUnits(x) {
			if u.Gate {
				continue
			}
			p.Total++
			if stepClosed(u) {
				p.Done++
			}
		}
	}
	return p
}

// workingOperators counts the live Operators on a workstream's (or track's) unfinished beads.
func workingOperators(ws SnapWorkstream) int {
	ops := map[string]bool{}
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, x := range steps {
			if parked(x) {
				continue
			}
			if !stepClosed(x) && strings.HasPrefix(x.Owner, "operator-") && stepAlive(x) {
				ops[x.Owner] = true
			}
			walk(x.Children)
		}
	}
	walk(ws.Steps)
	return len(ops)
}

// workingAgents counts who is working on a workstream now: its Manager in a
// turn (or with Operators out, between turns too) plus its live Operators.
func workingAgents(ws SnapWorkstream, manager SnapAgent, hasManager bool) int {
	ops := workingOperators(ws)
	if agentBusy(manager, hasManager) || ops > 0 {
		return ops + 1
	}
	return ops
}

// realBeadID is the shape of a project-prefixed bead id ("lab-hub.1.2").
var realBeadID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+(\.[0-9]+)*$`)

// isIDTitle: the title is the step's own bead id (an id-shaped title is never a plan).
func isIDTitle(title, id string) bool {
	t := strings.TrimSpace(title)
	return t != "" && t == id && realBeadID.MatchString(t)
}

// withoutIDTitles blanks every step title that is its own bead id (so it reads
// "Untitled step"); it copies what it changes, never the caller's snapshot.
func withoutIDTitles(s Snapshot) Snapshot {
	var fix func([]SnapStep) ([]SnapStep, bool)
	fix = func(steps []SnapStep) ([]SnapStep, bool) {
		out, copied := steps, false
		for i, st := range steps {
			c, changed := fix(st.Children)
			idTitle := isIDTitle(st.Title, st.BeadID)
			if !idTitle && !changed {
				continue
			}
			if !copied {
				out, copied = slices.Clone(steps), true
			}
			if idTitle {
				out[i].Title = ""
			}
			out[i].Children = c
		}
		return out, copied
	}
	copied := false
	for i, w := range s.Workstreams {
		if st, changed := fix(w.Steps); changed {
			if !copied {
				s.Workstreams, copied = slices.Clone(s.Workstreams), true
			}
			s.Workstreams[i].Steps = st
		}
	}
	return s
}

// titleOf is a workstream's title for display: never empty, never its own bead id.
func titleOf(t, id string) string {
	if c := strings.TrimSpace(t); c != "" && !isIDTitle(c, id) {
		return t
	}
	return UntitledStep
}

// plainTitle is a title without its ticket ids (the pill shows them).
func plainTitle(t string) string {
	if c := cleanTitle(strings.TrimSpace(withoutTicketIDs(t))); c != "" {
		return c
	}
	return UntitledStep
}

// UntitledStep stands in for a step with no title: a bead id is never shown to the user.
const UntitledStep = "Untitled step"

// runtimeHint is a trailing parenthetical naming the agent runtime
// ("(retry on Claude)", "(on Codex)"): agent-internal, never shown.
var runtimeHint = regexp.MustCompile(`(?i)\s*\((?:(?:retry|resume|attempt\s+\d+)\s+)?on\s+(?:claude|codex|opencode)\)\s*$`)

// cleanTitle drops trailing runtime hints from a title shown to the user.
func cleanTitle(t string) string {
	for {
		c := strings.TrimSpace(runtimeHint.ReplaceAllString(t, ""))
		if c == t || c == "" {
			return t
		}
		t = c
	}
}

// shown are the steps the user plans: Operator task beads are left out (their step's own state says what they do).
func shown(steps []SnapStep) []SnapStep {
	if !slices.ContainsFunc(steps, func(s SnapStep) bool { return s.Task }) {
		return steps
	}
	return slices.DeleteFunc(slices.Clone(steps), func(s SnapStep) bool { return s.Task })
}

// lower lowercases a title's first letter for mid-sentence use, leaving acronyms ("PR approval") alone.
func lower(t string) string {
	r := []rune(t)
	if len(r) == 0 || (len(r) >= 2 && r[0] >= 'A' && r[0] <= 'Z' && r[1] >= 'A' && r[1] <= 'Z') {
		return t
	}
	return string(unicode.ToLower(r[0])) + string(r[1:])
}

// heldAsk is an open user decision a live Operator is still working toward:
// the ask on it, and the step the Operator holds.
type heldAsk struct{ ask, op SnapStep }

func (h *heldAsk) line() line {
	return line{kind: "working", text: "Operator working on " + lower(plainTitle(h.op.Title)), why: h.why()}
}

func (h *heldAsk) why() string { return "An Operator is still on it; the ask waits for its report" }

// heldByLive splits decisions into those that are the user's now and the
// first one a live Operator covers (nil when none): the Operator is on the
// decision's own work (the step or under it) or on what it waits on.
func (b board) heldByLive(decisions []SnapStep) ([]SnapStep, *heldAsk) {
	var out []SnapStep
	var held *heldAsk
	for _, d := range decisions {
		op, ok := b.liveBeforeDecision(d)
		if !ok {
			out = append(out, d)
			continue
		}
		if held == nil {
			held = &heldAsk{ask: d, op: op}
		}
	}
	return out, held
}

// liveBeforeDecision keeps prerequisite work ahead of a decision, but an open
// card already belongs to the user even while its own Operator task stays alive.
func (b board) liveBeforeDecision(s SnapStep) (SnapStep, bool) {
	if b.needsIDs[s.BeadID] {
		s.Owner = ""
		s.Children = slices.DeleteFunc(slices.Clone(s.Children), func(c SnapStep) bool { return c.Task })
	}
	return b.liveOn(s)
}

// liveOn finds an open step a live Operator claims among s, the steps under
// it and the open work it waits on (followed through blockers).
func (b board) liveOn(s SnapStep) (SnapStep, bool) {
	seen := map[string]bool{}
	var find func(s SnapStep) (SnapStep, bool)
	find = func(s SnapStep) (SnapStep, bool) {
		if seen[s.BeadID] {
			return SnapStep{}, false
		}
		seen[s.BeadID] = true
		if stepClosed(s) || parked(s) {
			return SnapStep{}, false
		}
		if strings.HasPrefix(s.Owner, "operator-") && stepAlive(s) {
			return s, true
		}
		for _, c := range s.Children {
			if hit, ok := find(c); ok {
				return hit, true
			}
		}
		for _, id := range s.BlockedBy {
			if bl, ok := b.steps[id]; ok {
				if hit, ok := find(bl); ok {
					return hit, true
				}
			}
		}
		return SnapStep{}, false
	}
	return find(s)
}

// hiddenOperator is a live Operator of ws's Manager whose bead the board does
// not show open (its step was closed under it): the work still runs.
func (b board) hiddenOperator(ws SnapWorkstream) (string, bool) {
	for _, o := range b.s.Watch[ws.ManagerRole].Operators {
		if s, ok := b.steps[o.BeadID]; !ok || stepClosed(s) {
			return o.BeadID, true
		}
	}
	return "", false
}

// ownWaitLine is the line for a live wait on the workstream (or track) bead itself.
func (b board) ownWaitLine(ws SnapWorkstream) (line, bool) {
	self := SnapStep{WaitSince: ws.WaitSince, WaitUntil: ws.WaitUntil, WaitUntilAs: ws.WaitUntilAs, WaitUntils: ws.WaitUntils}
	var waits []string
	for _, w := range strings.Split(ws.Waiting, " · ") {
		// A time wait whose time has passed holds nothing: its owner was woken.
		// operator waits are judged by liveness elsewhere (a dead Operator holds nothing).
		if w != "" && w != "you" && !strings.HasPrefix(w, "operator") && (w == beads.ManagerPlanWait || !self.waitExpired(b.now, w)) {
			waits = append(waits, w)
		}
	}
	return b.waitLine(waits, slices.Repeat([]SnapStep{self}, len(waits)))
}

// parkedReturn is the earliest future parked-until among the parked steps under steps, 0 for none.
func (b board) parkedReturn(steps []SnapStep) int64 {
	var min int64
	for _, s := range steps {
		if parked(s) && !stepClosed(s) && s.ParkedUntil > b.now && (min == 0 || s.ParkedUntil < min) {
			min = s.ParkedUntil
		}
		if c := b.parkedReturn(s.Children); c != 0 && (min == 0 || c < min) {
			min = c
		}
	}
	return min
}

// waitExpired: w is a time wait whose time has passed, so it holds nothing (its owner was woken).
func (s SnapStep) waitExpired(now int64, w string) bool {
	if u, ok := s.WaitUntils[w]; ok {
		return u <= now
	}
	return s.WaitUntil != 0 && s.WaitUntil <= now && s.WaitUntilAs == w
}

// userStep is s, or the step above it when s is an Operator task bead (agent-internal: the user's step is the one it serves).
func userStep(steps []SnapStep, s SnapStep) SnapStep {
	for s.Task {
		var up *SnapStep
		var find func(ss []SnapStep, parent *SnapStep)
		find = func(ss []SnapStep, parent *SnapStep) {
			for i := range ss {
				if ss[i].BeadID == s.BeadID {
					up = parent
					return
				}
				find(ss[i].Children, &ss[i])
			}
		}
		find(steps, nil)
		if up == nil {
			break
		}
		s = *up
	}
	return s
}

// DeliveryMilestones counts planned stages, never internal Operator attempts.
// Legacy leaf progress remains available separately; neither count estimates effort.
func DeliveryMilestones(ws SnapWorkstream) Progress {
	var p Progress
	var walk func([]SnapStep)
	walk = func(steps []SnapStep) {
		for _, st := range steps {
			if parked(st) || st.Superseded || st.Task || st.Gate {
				continue
			}
			if st.Track {
				walk(st.Children)
				continue
			}
			p.Total++
			if !stepClosed(st) {
				continue
			}
			passed := true
			for _, g := range st.Gates {
				if g.Status != "passed" {
					passed = false
				}
			}
			if passed {
				p.Done++
			}
		}
	}
	walk(ws.Steps)
	return p
}
