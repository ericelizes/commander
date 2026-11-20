package livedata

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/example-user/commander/internal/agentrt"
)

// Tree is the workstream board as the app's tree view draws it and the
// Commander reads it: every workstream nested to any depth (rows), every step
// (a card) with its owner, state, column and the lines its card shows, and the
// "blocked by" edges between steps. The app renders these fields as they are
// and `cmdr workstream-statuses` prints them, both from BuildTree over one
// snapshot, so what Eric sees and what the Commander reads cannot differ.
type Tree struct {
	Version  uint64      `json:"version"`
	AsOf     int64       `json:"asof"`      // unix s the snapshot was built
	Done     int         `json:"done"`      // top-level workstreams done
	Total    int         `json:"total"`     // top-level workstreams
	NeedsYou int         `json:"needs_you"` // your cards: active steps you own
	Rows     []TreeRow   `json:"rows"`      // depth first, each before its children
	Steps    []TreeStep  `json:"steps"`
	Edges    [][2]string `json:"edges"` // [blocker, blocked]
}

// TreeRow is one workstream.
type TreeRow struct {
	ID       string   `json:"id"`  // bead id
	Key      string   `json:"key"` // project:bead id
	Project  string   `json:"project"`
	Parent   string   `json:"parent,omitempty"`
	Label    string   `json:"label"` // "3.2.1": its place in the tree
	Depth    int      `json:"depth"`
	Title    string   `json:"title"`
	Tickets  []string `json:"tix,omitempty"`
	Criteria []string `json:"ac,omitempty"`   // acceptance criteria, as written on the bead
	Summary  string   `json:"sum,omitempty"`  // the describer's line for a workstream that holds workstreams
	Note     string   `json:"note,omitempty"` // "Paused"
	Done     bool     `json:"done"`
	// Progress is the share done: of its workstreams when it holds any, else of its steps.
	Progress int    `json:"pct"`
	Of       string `json:"of"` // "2 of 5 workstreams", "4 of 9 steps"
	NeedsYou int    `json:"needs_you"`
	// State is the panel's one word for it: done, "Needs you", "2 need you", "In progress" or its status.
	State string `json:"state"`
	// Status is what holds it now, by owner (the tree's status chips).
	Status []TreeLine `json:"status"`
	// Line is its one line in the Now column: the summary for a workstream of workstreams, else its lead step.
	Line TreeLine `json:"line"`
	// Now is what its Now cell says when no step of its own is current.
	Now string `json:"now,omitempty"`
}

// TreeLine is a line with the owner tone it is drawn in: you, agent, review,
// person, none (plain), bad (an invalid state) or sum (a summary).
type TreeLine struct {
	Tone string `json:"tone"`
	Text string `json:"text"`
}

// TreeStep is one step: one owner, one goal.
type TreeStep struct {
	ID    string `json:"id"`
	Row   string `json:"row"`
	Title string `json:"title"`
	Type  string `json:"type"`  // you | agent | review | person | automatic
	Owner string `json:"owner"` // You, Implementer agent, Review agent, or the person or team
	State string `json:"state"` // active | notstarted | done
	// Col is its column: 0 Now, 1 Next, n "n steps out"; done steps are -1.
	Col   int    `json:"col"`
	Run   string `json:"run,omitempty"`   // claude or codex while a live agent is on it
	Model string `json:"model,omitempty"` // that agent's model as shown ("Opus 5.5"), or its runtime ("Claude") when the model is not known
	// ModelID is the exact model id its run was launched with ("anthropic/claude-sonnet-5-5", "gpt-5-codex").
	ModelID  string `json:"model_id,omitempty"`
	Asleep   bool   `json:"asleep,omitempty"`
	Own      string `json:"own"`          // the card's owner line
	Wait     string `json:"wt,omitempty"` // the card's second line
	Ask      string `json:"ask,omitempty"`
	Text     string `json:"text,omitempty"` // the state text in the data
	Since    int64  `json:"since,omitempty"`
	Gate     bool   `json:"gate,omitempty"`
	Criteria string `json:"ac,omitempty"`
	// sentBack: what a review said when it sent its work back (its result is ready and not a pass).
	sentBack string
	// asksCommander: its agent waits on the Commander's answer to its question
	asksCommander bool
}

var (
	treeReviewTitle = regexp.MustCompile(`(?i)^\s*(review\b|independent(ly)? (re-)?review|commander (checks|review)|review of .* by a separate operator)`)
	treeWaitText    = regexp.MustCompile(`^Waiting (for|on) `)
	treeTicketTail  = regexp.MustCompile(`\s*\(((?:[A-Z][A-Z0-9]{1,9}-\d{1,6}(?:,\s*|\s+)?)+)\)\s*$`)
	treeTicketID    = regexp.MustCompile(`[A-Z][A-Z0-9]{1,9}-\d{1,6}`)
)

var runtimeName = map[string]string{"claude": "Claude", "codex": "Codex"}

// ModelName is how a model id shows: "Opus 5.5" for a Claude model, "GPT-5
// Codex" for a Codex one. An unknown model shows its runtime ("Claude").
func ModelName(runtime, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		if n := runtimeName[runtime]; n != "" {
			return n
		}
		return "Agent"
	}
	if strings.HasPrefix(id, "codex:") {
		runtime, id = "codex", strings.TrimPrefix(id, "codex:")
	}
	if runtime == "codex" || strings.HasPrefix(id, "gpt") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") {
		// gpt-5-codex reads "GPT-5 Codex": the family and its version joined, then words.
		parts := strings.Split(id, "-")
		name := strings.ToUpper(parts[0])
		rest := parts[1:]
		if len(rest) > 0 && rest[0] != "" && unicode.IsDigit(rune(rest[0][0])) {
			name, rest = name+"-"+rest[0], rest[1:]
		}
		for _, p := range rest {
			if p != "" {
				name += " " + strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return name
	}
	if d := agentrt.ModelDisplay(id); d != "" {
		return d
	}
	return id
}

var unknownModels sync.Map

// noteUnknownModel logs, once per step, a live run whose model is not recorded:
// the card shows its runtime instead, never a guess.
func noteUnknownModel(step, runtime string) {
	if _, seen := unknownModels.LoadOrStore(step+"|"+runtime, true); !seen {
		log.Printf("tree: step %s has a live %s agent with no recorded model (no agent_runs.execution_model for its run, or the run predates it): showing the runtime", step, runtime)
	}
}

// treeTitle splits a title's trailing bracket of ticket ids off: the ids show as links.
func treeTitle(t string) (string, []string) {
	t = strings.TrimSpace(t)
	m := treeTicketTail.FindStringSubmatch(t)
	if m == nil {
		return plainTitle(t), nil
	}
	return plainTitle(strings.TrimSpace(t[:len(t)-len(m[0])])), treeTicketID.FindAllString(m[1], -1)
}

// criteriaList splits acceptance criteria written as lines or bullets.
func criteriaList(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*•0123456789.) "))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

type treeBuild struct {
	t       *Tree
	diagram map[string]DiagramStep
	live    map[string]SnapExecution // bead id -> the live agent run on it
	rows    map[string]*TreeRow
	steps   map[string]*TreeStep
	kids    map[string][]string // row -> child rows
	own     map[string][]string // row -> its own steps
	leaves  map[string][]string // a container step or row -> the steps standing for it in edges
	blocked map[string][]string // bead id -> the bead ids it waits on, as written
	paused  map[string]bool     // rows marked Paused
	closed  map[string]bool     // rows whose bead is closed
	summary map[string]string
}

// BuildTree derives the tree from a snapshot.
func BuildTree(snap Snapshot, now int64) Tree {
	t := Tree{Version: snap.Version, Rows: []TreeRow{}, Steps: []TreeStep{}, Edges: [][2]string{}}
	if !snap.GeneratedAt.IsZero() {
		t.AsOf = snap.GeneratedAt.Unix()
	}
	b := treeBuild{t: &t, diagram: map[string]DiagramStep{}, live: map[string]SnapExecution{}, rows: map[string]*TreeRow{}, steps: map[string]*TreeStep{},
		kids: map[string][]string{}, own: map[string][]string{}, leaves: map[string][]string{}, blocked: map[string][]string{}, paused: map[string]bool{}, closed: map[string]bool{}, summary: map[string]string{}}
	if snap.Display != nil {
		var walk func([]DiagramStep)
		walk = func(ss []DiagramStep) {
			for _, s := range ss {
				if _, ok := b.diagram[s.ID]; !ok || !s.Track {
					b.diagram[s.ID] = s
				}
				walk(s.Children)
			}
		}
		for _, r := range snap.Display.Workstreams {
			if r.Diagram == nil {
				continue
			}
			walk(r.Diagram.Steps)
			for _, d := range r.Diagram.Tracks {
				walk(d.Steps)
			}
		}
		b.summary = snap.Display.Summaries
	}
	for _, x := range snap.Executions {
		if x.State == "" || x.State == "exited" || x.State == "finished" || x.State == "died" {
			continue
		}
		for _, id := range []string{x.BeadID, x.TaskBeadID} {
			if _, seen := b.live[id]; id != "" && !seen {
				b.live[id] = x
			}
		}
	}
	var order []string // rows in tree order
	for _, ws := range snap.Workstreams {
		if ws.Withdrawn {
			continue
		}
		title, tix := treeTitle(ws.Title)
		row := &TreeRow{ID: ws.BeadID, Key: ws.Project + ":" + ws.BeadID, Project: ws.Project, Label: strconv.Itoa(ws.N), Title: title,
			Tickets: mergeTickets(ws.Tickets, tix), Criteria: criteriaList(ws.Criteria)}
		b.closed[ws.BeadID] = ws.Status == "closed"
		b.blocked[ws.BeadID] = ws.BlockedBy
		b.rows[row.ID] = row
		order = append(order, row.ID)
		order = b.level(ws, ws.Steps, row, order)
	}
	b.finish(order, now)
	return t
}

func mergeTickets(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, x := range l {
			if x != "" && !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// level places one level of a workstream's steps under row: nested
// workstreams become rows, the rest become its steps.
func (b *treeBuild) level(ws SnapWorkstream, steps []SnapStep, row *TreeRow, order []string) []string {
	k := 0
	// Sibling workstreams read in their stored rank (`cmdr internal-move-objective`), the same rule
	// as the sidebar; unranked ones follow in the order they were created (Phase 1, 2, 3, 4), not
	// "soonest to run". Steps keep their own order. Nov 20: Eric asked twice to reorder 1.2's
	// workstreams and the board could not show it, because it read creation order only.
	steps = slices.Clone(steps)
	sort.SliceStable(steps, func(i, j int) bool {
		a, c := steps[i], steps[j]
		if !a.Track || !c.Track {
			return false
		}
		if (a.Rank > 0) != (c.Rank > 0) {
			return a.Rank > 0
		}
		if a.Rank != c.Rank {
			return a.Rank < c.Rank
		}
		return naturalIDLess(a.BeadID, c.BeadID)
	})
	for _, s := range steps {
		if s.Superseded || s.Withdrawn || s.Task {
			continue
		}
		b.blocked[s.BeadID] = append(b.blocked[s.BeadID], s.BlockedBy...)
		if s.Track {
			k++
			title, tix := treeTitle(trackTitle(s.Title))
			child := &TreeRow{ID: s.BeadID, Key: ws.Project + ":" + s.BeadID, Project: ws.Project, Parent: row.ID, Label: row.Label + "." + strconv.Itoa(k),
				Depth: row.Depth + 1, Title: title, Tickets: mergeTickets(s.Tickets, tix), Criteria: criteriaList(s.Criteria)}
			// A parked (deferred) workstream, or one inside a paused parent, is paused: the watcher holds its
			// steps, so the board says "Paused" rather than "Stalled".
			if d, ok := b.diagram[s.BeadID]; (ok && strings.HasPrefix(d.Text, "Paused")) || s.Status == "deferred" || b.paused[row.ID] {
				b.paused[child.ID] = true
			}
			b.closed[child.ID] = s.Status == "closed"
			b.rows[child.ID] = child
			b.kids[row.ID] = append(b.kids[row.ID], child.ID)
			order = append(order, child.ID)
			order = b.level(ws, s.Children, child, order)
			continue
		}
		if s.ClosingGate || strings.EqualFold(strings.TrimSpace(s.Title), "confirm done") || placeholderStep(s) {
			continue
		}
		b.addSteps(ws, s, row)
	}
	return order
}

// placeholderStep: a stand-in for a wait on another workstream, with no goal of its own.
func placeholderStep(s SnapStep) bool {
	return strings.Contains(s.BeadID, "#xwait") || strings.Contains(s.BeadID, "#wait") || strings.HasPrefix(strings.TrimSpace(s.Title), "Waiting on workstream")
}

// addSteps adds s, or the steps under it when it holds some: a step has no children.
func (b *treeBuild) addSteps(ws SnapWorkstream, s SnapStep, row *TreeRow) []string {
	var kids []SnapStep
	for _, c := range s.Children {
		if !c.Superseded && !c.Withdrawn && !c.Task && !c.Track {
			kids = append(kids, c)
		}
	}
	if len(kids) > 0 {
		var ids []string
		for _, c := range kids {
			b.blocked[c.BeadID] = append(b.blocked[c.BeadID], c.BlockedBy...)
			if c.ClosingGate || placeholderStep(c) {
				continue
			}
			ids = append(ids, b.addSteps(ws, c, row)...)
		}
		b.leaves[s.BeadID] = ids
		return ids
	}
	b.own[row.ID] = append(b.own[row.ID], s.BeadID)
	b.steps[s.BeadID] = b.step(s, row.ID)
	return []string{s.BeadID}
}

// step reads one step's owner and state from the workflow diagram, falling back to the bead.
func (b *treeBuild) step(s SnapStep, row string) *TreeStep {
	d, drawn := b.diagram[s.BeadID]
	kind, actor, text, who := d.Kind, d.Actor, d.Text, d.Who
	if !drawn {
		actor = map[string]string{"you": "you", "agents": "operators", "commander": "manager"}[s.Lane]
		if strings.HasPrefix(s.Lane, "elsewhere") {
			actor, who = "reviewers", strings.TrimPrefix(strings.TrimPrefix(s.Lane, "elsewhere"), ":")
		}
		switch {
		case s.Status == "closed":
			kind, text = "done", "Done"
		case s.Gate || actor == "you":
			kind, text = "idle", "Not reached"
		default:
			kind, text = "idle", "Not started"
		}
	}
	title, _ := treeTitle(s.Title)
	if drawn && d.Title != "" {
		title, _ = treeTitle(d.Title)
	}
	// Time on the step: the diagram's own since, else when the step last moved (every active card shows a timer).
	since := d.Since
	if since == 0 {
		since = s.ProgressAt
	}
	st := &TreeStep{ID: s.BeadID, Row: row, Title: title, Text: text, Since: since, Gate: d.Gate || s.Gate, Criteria: s.Criteria}
	if s.ResultReadyAt > 0 && sentBackReport.MatchString(s.ResultLine) {
		st.sentBack = sentBackWhy(s.ResultLine)
	}
	// A step whose agent asked Eric something (its card) is his until he answers;
	// a question to the Commander keeps it the agent's card, waiting on that answer.
	asksYou := s.Status != "closed" && (s.State == "needs-user" || s.NeedsUser)
	st.asksCommander = s.QuestionWaiting && !asksYou
	switch {
	case s.Automatic:
		// Automatic: no person or agent; it waits for one event and closes itself.
		st.Type, st.Owner = "automatic", "Automatic"
	case asksYou || actor == "you":
		st.Type, st.Owner = "you", "You"
	case actor == "reviewers":
		st.Type, st.Owner = "person", personName(who)
	case actor == "manager" || s.Lane == "commander" || treeReviewTitle.MatchString(s.Title):
		st.Type, st.Owner = "review", "Review agent"
	default:
		st.Type, st.Owner = "agent", "Implementer agent"
	}
	switch kind {
	case "done":
		st.State = "done"
	case "idle", "quiet":
		st.State = "notstarted"
	default:
		st.State = "active"
	}
	// A parked step runs nothing. Under a parked workstream it is paused with
	// it; on its own it keeps the board's text, an invalid state, so its row
	// reads "Stalled: nothing active" in red, never a quiet pause.
	if s.Status == "deferred" && st.State != "done" {
		st.State = "notstarted"
		if b.paused[row] || text != ParkedStepText {
			st.Text = "Paused"
		}
	}
	if st.State != "done" {
		st.Run = d.Runtime
		if st.Run == "" {
			st.Run = s.LiveRuntime
		}
		if x, ok := b.live[s.BeadID]; ok {
			if st.Run == "" {
				st.Run = x.Runtime
			}
			st.ModelID = x.Model
		}
		if st.Run == "" && kind == "working" && actor == "operators" {
			st.Run = "agent"
		}
	}
	if asksYou {
		st.State, st.Run, st.ModelID = "active", "", ""
		if !strings.HasPrefix(st.Text, "Needs you") {
			st.Text = "Needs you"
		}
		st.Ask = strings.TrimSpace(s.Ask)
		if s.Decision != nil && strings.TrimSpace(s.Decision.Question) != "" {
			st.Ask = strings.TrimSpace(s.Decision.Question)
		}
		if st.Ask == "" {
			st.Ask = strings.TrimSpace(s.ResultLine) // what its agent last said when it stopped for him
		}
		if st.Ask != "" {
			return st
		}
	}
	if st.Type == "you" && st.State == "active" {
		st.Ask = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "Needs you"), ":"))
		st.Ask = strings.TrimPrefix(st.Ask, "· ")
		if strings.HasPrefix(st.Ask, "Asked ") {
			st.Ask = ""
		}
	}
	return st
}

func personName(who string) string {
	who = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(who), "waiting on "))
	if who == "" || strings.EqualFold(who, "a timer") {
		return "Another person"
	}
	return who
}

// finish works out what needs the whole tree: edges, states, columns and lines.
func (b *treeBuild) finish(order []string, now int64) {
	t := b.t
	// the steps standing for a row in edges: its first steps (as a waiter) and last steps (as a blocker)
	under := func(id string) []string { return b.stepsUnder(id) }
	seen := map[[2]string]bool{}
	blockers := map[string][]string{}
	add := func(a, z string) {
		if a == z || b.steps[a] == nil || b.steps[z] == nil {
			return
		}
		e := [2]string{a, z}
		if seen[e] {
			return
		}
		seen[e] = true
		t.Edges = append(t.Edges, e)
		blockers[z] = append(blockers[z], a)
	}
	expand := func(id string, last bool) []string {
		if b.steps[id] != nil {
			return []string{id}
		}
		if l, ok := b.leaves[id]; ok {
			return l
		}
		if b.rows[id] != nil {
			return b.ends(under(id), last)
		}
		return nil
	}
	ids := make([]string, 0, len(b.blocked))
	for id := range b.blocked {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for pass := 0; pass < 2; pass++ { // a row's ends depend on the edges between its steps: those first
		for _, z := range ids {
			for _, a := range b.blocked[z] {
				if rowEdge := b.rows[a] != nil || b.rows[z] != nil; (pass == 1) != rowEdge {
					continue
				}
				for _, x := range expand(a, true) {
					for _, y := range expand(z, false) {
						add(x, y)
					}
				}
			}
		}
	}
	sort.Slice(t.Edges, func(i, j int) bool {
		if t.Edges[i][0] != t.Edges[j][0] {
			return t.Edges[i][0] < t.Edges[j][0]
		}
		return t.Edges[i][1] < t.Edges[j][1]
	})
	open := func(id string) bool { return b.steps[id].State != "done" }
	openBlockers := func(id string) []string {
		var out []string
		for _, x := range blockers[id] {
			if open(x) {
				out = append(out, x)
			}
		}
		return out
	}
	// A step under way never waits on an unfinished one (rule 21): it is upcoming.
	// Your card too: a card written ahead (needs-user label, decision) is put to you
	// only once nothing open holds it; until then it is a future step with no ask or timer.
	for _, id := range sortedKeys(b.steps) {
		s := b.steps[id]
		if s.State == "active" && len(openBlockers(id)) > 0 && s.Run == "" {
			s.State = "notstarted"
			if s.Type == "you" {
				s.Text, s.Ask = "Not reached", ""
			}
		}
	}
	// An unblocked agent step never sits unstarted (rule 21): the watcher starts an agent on it.
	// An unblocked step owned by a named person or team is theirs now: it is current, not upcoming.
	// A parked or held step is the exception the watcher skips: an invalid state, never shown as work.
	for _, id := range sortedKeys(b.steps) {
		s := b.steps[id]
		if s.Text == ParkedStepText || s.Text == HeldStepText {
			s.State = "notstarted"
			continue
		}
		if s.State == "notstarted" && (s.Type == "agent" || s.Type == "review" || s.Type == "automatic" || s.Type == "person") && !b.paused[s.Row] && !pausedText(s.Text) && len(openBlockers(id)) == 0 {
			s.State = "active"
			if s.Type == "person" && (s.Text == "" || s.Text == "Not started" || s.Text == "Not reached") {
				s.Text = "Waiting on " + s.Owner
			}
		}
	}
	// Numbers are places among the workstreams shown: open ones first, in rank
	// order, so the first one shown is 1 and its children 1.1, 1.2; done ones
	// (hidden by default) follow.
	order = b.renumber(order)
	// columns: distance from now, shared by every workstream
	rank := map[string]int{}
	var r func(string) int
	r = func(id string) int {
		if v, ok := rank[id]; ok {
			return v
		}
		rank[id] = 0
		v := 1
		if b.steps[id].State == "active" {
			v = 0
		}
		for _, x := range openBlockers(id) {
			v = max(v, r(x)+1)
		}
		rank[id] = v
		return v
	}
	for _, id := range sortedKeys(b.steps) {
		s := b.steps[id]
		if open(id) {
			s.Col = r(id)
		} else {
			s.Col = -1
		}
		b.cardLines(s, openBlockers(id))
	}
	// rows
	roots, rootsDone := 0, 0
	for _, id := range order {
		row := b.rows[id]
		all := under(id)
		row.Done = b.done(id)
		row.Note = map[bool]string{true: "Paused"}[b.paused[id]]
		row.Summary = b.summary[id]
		kids := b.kids[id]
		row.Progress, row.Of = b.progress(id)
		for _, s := range all {
			if st := b.steps[s]; st.State == "active" && st.Type == "you" && yourCard(st) {
				row.NeedsYou++
			}
		}
		row.Status = b.status(id)
		row.Line = b.line(id)
		switch {
		case row.Done:
			row.State = "done"
		case row.NeedsYou == 1:
			row.State = "Needs you"
		case row.NeedsYou > 1:
			row.State = fmt.Sprintf("%d need you", row.NeedsYou)
		case len(kids) > 0:
			row.State = "In progress"
		default:
			row.State = row.Status[0].Text
		}
		switch {
		case row.Done:
			row.Now = "✓ Completed"
		case len(kids) > 0:
			row.Now = row.Line.Text
		default:
			row.Now = row.Status[0].Text
		}
		if row.Parent == "" {
			roots++
			if row.Done {
				rootsDone++
			}
		}
		t.Rows = append(t.Rows, *row)
	}
	t.Total, t.Done = roots, rootsDone
	for _, id := range order {
		for _, sid := range b.own[id] {
			s := b.steps[sid]
			t.Steps = append(t.Steps, *s)
			if s.State == "active" && s.Type == "you" && yourCard(s) {
				t.NeedsYou++
			}
		}
	}
}

// renumber labels the rows by their place among their open siblings (done ones
// after), and returns the rows in that order, each before its children.
func (b *treeBuild) renumber(order []string) []string {
	var roots []string
	for _, id := range order {
		if b.rows[id].Parent == "" {
			roots = append(roots, id)
		}
	}
	var out []string
	var level func(ids []string, prefix string, depth int)
	level = func(ids []string, prefix string, depth int) {
		var open, done []string
		for _, id := range ids {
			if b.done(id) {
				done = append(done, id)
			} else {
				open = append(open, id)
			}
		}
		for i, id := range append(open, done...) {
			r := b.rows[id]
			r.Label, r.Depth = prefix+strconv.Itoa(i+1), depth
			out = append(out, id)
			level(b.kids[id], r.Label+".", depth+1)
		}
	}
	level(roots, "", 0)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pausedText(s string) bool {
	return strings.HasPrefix(s, "Paused") || strings.HasPrefix(s, "Parked")
}

// yourCard: an active step of yours that asks you something (not a wait dressed as yours).
func yourCard(s *TreeStep) bool {
	return !strings.HasPrefix(s.Text, "Waiting on ") && !strings.HasPrefix(s.Title, "Waiting on ") && !pausedText(s.Text)
}

func treePct(n, of int) int {
	if of == 0 {
		return 0
	}
	return n * 100 / of
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// stepsUnder is every step in the row and the rows under it.
func (b *treeBuild) stepsUnder(id string) []string {
	out := slices.Clone(b.own[id])
	for _, k := range b.kids[id] {
		out = append(out, b.stepsUnder(k)...)
	}
	return out
}

// done: a workstream is done exactly when all its items are: its child
// workstreams (each by this same rule) and its own steps (rule 25). One with
// no items is done when its own bead is closed. Progress counts the same items,
// so it reads 100% exactly when the row is done.
func (b *treeBuild) done(id string) bool {
	kids, own := b.kids[id], b.own[id]
	if len(kids)+len(own) == 0 {
		return b.closed[id]
	}
	for _, k := range kids {
		if !b.done(k) {
			return false
		}
	}
	for _, s := range own {
		if b.steps[s].State != "done" {
			return false
		}
	}
	return true
}

// progress is how many of a row's items (child workstreams and own steps) are done, and in words.
func (b *treeBuild) progress(id string) (int, string) {
	kids, own := b.kids[id], b.own[id]
	doneKids, doneSteps := 0, 0
	for _, k := range kids {
		if b.done(k) {
			doneKids++
		}
	}
	for _, s := range own {
		if b.steps[s].State == "done" {
			doneSteps++
		}
	}
	n, of := doneKids+doneSteps, len(kids)+len(own)
	switch {
	case of == 0:
		if b.done(id) {
			return 100, "done"
		}
		return 0, "nothing planned"
	case len(own) == 0:
		return treePct(n, of), fmt.Sprintf("%d of %d %s", n, of, plural(of, "workstream", "workstreams"))
	case len(kids) == 0:
		return treePct(n, of), fmt.Sprintf("%d of %d %s", n, of, plural(of, "step", "steps"))
	}
	return treePct(n, of), fmt.Sprintf("%d of %d %s and %d of %d %s", doneKids, len(kids), plural(len(kids), "workstream", "workstreams"), doneSteps, len(own), plural(len(own), "step", "steps"))
}

// ends are a set's first steps (nothing in the set before them) or last steps (nothing in it after them).
func (b *treeBuild) ends(set []string, last bool) []string {
	in := map[string]bool{}
	for _, s := range set {
		in[s] = true
	}
	inner := map[string]bool{}
	for _, e := range b.t.Edges {
		if in[e[0]] && in[e[1]] {
			if last {
				inner[e[0]] = true
			} else {
				inner[e[1]] = true
			}
		}
	}
	var out []string
	for _, s := range set {
		if !inner[s] {
			out = append(out, s)
		}
	}
	return out
}

// cardLines sets the card's owner line and second line.
func (b *treeBuild) cardLines(s *TreeStep, openBlockers []string) {
	if s.Type == "automatic" {
		s.Own, s.Run, s.Model, s.ModelID, s.Asleep = "Automatic", "", "", "", false
		if s.State == "done" {
			s.Own = "✓ Automatic"
		}
		s.Wait = ""
		switch {
		case s.State == "active" && !strings.HasPrefix(s.Title, "Waiting for"):
			// the title already says what it waits for; otherwise this line does
			s.Wait = waitingFor(s.Text, s.Title)
		case s.State == "notstarted":
			var other []string
			for _, x := range openBlockers {
				if o := b.steps[x]; o.Row != s.Row {
					if l := b.rows[o.Row].Label; !slices.Contains(other, l) {
						other = append(other, l)
					}
				}
			}
			if len(other) > 0 {
				s.Wait = "After " + strings.Join(other, ", ")
			}
		}
		return
	}
	agentish := s.Type == "agent" || s.Type == "review"
	rt := ""
	if s.State == "active" && agentish {
		rt = s.Run
	}
	if rt != "" {
		s.Model = ModelName(rt, s.ModelID)
		if s.ModelID == "" {
			noteUnknownModel(s.ID, rt)
		}
	} else {
		s.ModelID = ""
	}
	s.Asleep = rt == "" && s.State == "active" && s.Type == "agent" && treeWaitText.MatchString(s.Text)
	own := s.Owner
	if s.State == "done" {
		own = "✓ " + own
	}
	switch {
	case s.Model != "":
		own += " · " + s.Model
	case s.Asleep:
		own += " · asleep"
	}
	s.Own = own
	s.Wait = ""
	switch {
	case rt != "":
	case s.State == "active":
		switch {
		case s.Type == "you":
			s.Wait = s.Ask // the question, on the card
		case s.Asleep:
			s.Wait = "Asleep until " + treeWaitText.ReplaceAllString(s.Text, "")
		case agentish && s.asksCommander:
			s.Wait = "Waiting on the Commander's answer"
		case strings.HasPrefix(s.Text, "Waiting"), strings.HasPrefix(s.Text, "Stuck"), s.Text == "State missing", strings.HasPrefix(s.Text, "Invalid"):
			s.Wait = s.Text
		case agentish && s.sentBack != "":
			// It ran and sent the work back: the revision comes before it reruns.
			s.Wait = "Sent back: " + s.sentBack
		case agentish:
			// No live agent on an active agent step: say exactly that (the card must never look like work),
			// with the step's own status as the reason.
			s.Wait = "No agent running"
			if r := strings.TrimSpace(s.Text); r != "" && r != "Agent working" {
				s.Wait += ": " + r
			}
		}
	case s.State == "notstarted":
		var other []string
		for _, x := range openBlockers {
			if o := b.steps[x]; o.Row != s.Row {
				if l := b.rows[o.Row].Label; !slices.Contains(other, l) {
					other = append(other, l)
				}
			}
		}
		if len(other) > 0 {
			s.Wait = "After " + strings.Join(other, ", ")
		} else if pausedText(s.Text) {
			s.Wait = s.Text
		}
	}
}

var ownerOrder = []string{"you", "review", "agent", "person", "automatic"}

// waitingFor is an Automatic step's line: what it waits for, in plain words.
func waitingFor(text, title string) string {
	if strings.HasPrefix(title, "Waiting for") {
		return title
	}
	if treeWaitText.MatchString(text) {
		return treeWaitText.ReplaceAllString(text, "Waiting for ")
	}
	return "Waiting for " + lowerFirst(title)
}

// status is what holds the row now, one chip per owner of an active step under it.
func (b *treeBuild) status(id string) []TreeLine {
	all := b.stepsUnder(id)
	var act []*TreeStep
	for _, s := range all {
		if st := b.steps[s]; st.State == "active" {
			act = append(act, st)
		}
	}
	var out []TreeLine
	seen := map[string]bool{}
	you := 0
	for _, s := range act {
		if s.Type == "you" {
			you++
		}
	}
	for _, typ := range ownerOrder {
		for _, s := range act {
			if s.Type != typ || seen[typ+s.Owner] {
				continue
			}
			seen[typ+s.Owner] = true
			switch typ {
			case "you":
				if you > 1 {
					out = append(out, TreeLine{"you", fmt.Sprintf("%d need you", you)})
				} else {
					out = append(out, TreeLine{"you", "Needs you"})
				}
			case "review":
				out = append(out, TreeLine{"review", "In review"})
			case "agent":
				out = append(out, TreeLine{"agent", "Implementer working"})
			case "automatic":
				out = append(out, TreeLine{"automatic", waitingFor(s.Text, s.Title)})
			default:
				out = append(out, TreeLine{"person", "Waiting on " + s.Owner})
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	inside := map[string]bool{}
	for _, s := range all {
		inside[s] = true
	}
	var openSteps []string
	for _, s := range all {
		if b.steps[s].State != "done" {
			openSteps = append(openSteps, s)
		}
	}
	if len(openSteps) == 0 {
		return []TreeLine{{"none", "Done"}}
	}
	var after []string
	for _, e := range b.t.Edges {
		if inside[e[1]] && !inside[e[0]] && b.steps[e[0]].State != "done" {
			if l := b.rows[b.steps[e[0]].Row].Label; !slices.Contains(after, l) {
				after = append(after, l)
			}
		}
	}
	switch {
	case len(after) > 0:
		return []TreeLine{{"none", "Blocked by " + strings.Join(after, ", ")}}
	case b.paused[id]:
		return []TreeLine{{"none", "Paused"}}
	}
	// A workstream whose open work is all on hold (deferred steps, paused workstreams), or waits
	// only behind held work, is paused, not stalled: every open step is held or waits on an open step.
	waits := map[string]bool{}
	for _, e := range b.t.Edges {
		if b.steps[e[0]].State != "done" {
			waits[e[1]] = true
		}
	}
	allPaused := true
	for _, s := range openSteps {
		if !b.paused[b.steps[s].Row] && !pausedText(b.steps[s].Text) && !waits[s] {
			allPaused = false
			break
		}
	}
	if allPaused {
		return []TreeLine{{"none", "Paused"}}
	}
	return []TreeLine{{"bad", "Stalled: nothing active"}}
}

// line is the row's one line: the summary of a workstream of workstreams, else its lead step.
func (b *treeBuild) line(id string) TreeLine {
	if b.done(id) {
		return TreeLine{"none", "Done"}
	}
	if len(b.kids[id]) > 0 {
		if s := b.summary[id]; s != "" {
			return TreeLine{"sum", s}
		}
		return TreeLine{"sum", "In progress"}
	}
	var act []*TreeStep
	for _, s := range b.stepsUnder(id) {
		if st := b.steps[s]; st.State == "active" {
			act = append(act, st)
		}
	}
	pri := map[string]int{"you": 0, "review": 1, "agent": 2, "person": 3, "automatic": 4}
	sort.SliceStable(act, func(i, j int) bool { return pri[act[i].Type] < pri[act[j].Type] })
	if len(act) > 0 {
		return stepLine(act[0])
	}
	return b.status(id)[0]
}

func stepLine(s *TreeStep) TreeLine {
	t := lowerFirst(s.Title)
	switch s.Type {
	case "you":
		return TreeLine{"you", "Needs you: " + t}
	case "review":
		return TreeLine{"review", "Review agent checking: " + strings.TrimPrefix(t, "review: ")}
	case "agent":
		if treeWaitText.MatchString(s.Text) {
			t += " (" + treeWaitText.ReplaceAllString(s.Text, "waiting for ") + ")"
		}
		return TreeLine{"agent", "Implementer agent: " + t}
	}
	if s.Type == "automatic" {
		return TreeLine{"automatic", waitingFor(s.Text, s.Title)}
	}
	return TreeLine{"person", "Waiting on " + s.Owner + ": " + t}
}

// JSON is the tree as the app reads it.
func (t Tree) JSON() string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(t)
	return strings.TrimSpace(b.String())
}

// Text is the tree as the Commander reads it: every workstream with its line,
// and every open step by column with its card's lines and its bead id. Done
// workstreams are one line each (the board hides them by default).
func (t Tree) Text(now int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Board v%d: %d of %d workstreams done · %d need you\n", t.Version, t.Done, t.Total, t.NeedsYou)
	byRow := map[string][]TreeStep{}
	for _, s := range t.Steps {
		byRow[s.Row] = append(byRow[s.Row], s)
	}
	doneParent := map[string]bool{}
	for _, r := range t.Rows {
		if doneParent[r.Parent] {
			doneParent[r.ID] = true
			continue
		}
		ind := strings.Repeat("  ", r.Depth)
		fmt.Fprintf(&b, "%s%s %s · %s", ind, r.Label, r.Title, r.ID)
		if len(r.Tickets) > 0 {
			fmt.Fprintf(&b, " · %s", strings.Join(r.Tickets, ", "))
		}
		if r.Done {
			b.WriteString(" — Done\n")
			doneParent[r.ID] = true
			continue
		}
		fmt.Fprintf(&b, " — %d%% (%s)", r.Progress, r.Of)
		if r.Note != "" {
			fmt.Fprintf(&b, " · %s", r.Note)
		}
		var chips []string
		for _, l := range r.Status {
			chips = append(chips, l.Text)
		}
		fmt.Fprintf(&b, " — %s\n", strings.Join(chips, "; "))
		if r.Line.Tone == "sum" {
			fmt.Fprintf(&b, "%s  Summary: %s\n", ind, r.Line.Text)
		}
		for _, c := range r.Criteria {
			fmt.Fprintf(&b, "%s  Criterion: %s\n", ind, c)
		}
		steps := byRow[r.ID]
		sort.SliceStable(steps, func(i, j int) bool { return steps[i].Col < steps[j].Col })
		for _, s := range steps {
			if s.State == "done" {
				continue
			}
			fmt.Fprintf(&b, "%s  %s: [%s] %s", ind, ColumnName(s.Col), s.Own, s.Title)
			if s.Wait != "" {
				fmt.Fprintf(&b, " — %s", s.Wait)
			}
			if s.Ask != "" {
				fmt.Fprintf(&b, " — asks: %s", s.Ask)
			}
			if age := statusAge(s.Since, now); age != "" && s.State == "active" {
				fmt.Fprintf(&b, " · %s", age)
			}
			fmt.Fprintf(&b, " · %s\n", s.ID)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ColumnName is the board's heading for a column.
func ColumnName(col int) string {
	switch col {
	case -1:
		return "Done"
	case 0:
		return "Now"
	case 1:
		return "Next"
	}
	return fmt.Sprintf("%d steps out", col)
}

// TreeBlock is what rides along with a message to the Commander: the tree as text.
func TreeBlock(t Tree, now int64) string {
	return fmt.Sprintf("<workstream-statuses version=\"%d\">\n%s\n</workstream-statuses>", t.Version, t.Text(now))
}

// naturalIDLess orders bead ids by their dotted numeric parts (en1h.9 < en1h.17 < en1h.20).
func naturalIDLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil {
			return na < nb
		}
		return pa[i] < pb[i]
	}
	return len(pa) < len(pb)
}

var sentBackReport = regexp.MustCompile(`(?i)^\s*final report\W*(send back|sent back|sending back|changes requested|blocked|fail|reject|not ready)`)
var sentBackHead = regexp.MustCompile(`(?i)^\s*final report\W*(send back|sent back|sending back|changes requested|blocked|failed|fail|rejected|reject|not ready)\W*`)

// sentBackWhy is a sent-back report's first line without its verdict and markup.
func sentBackWhy(line string) string {
	why := strings.TrimSpace(strings.ReplaceAll(sentBackHead.ReplaceAllString(line, ""), "**", ""))
	if why == "" {
		return "the findings are in its report"
	}
	return why
}
