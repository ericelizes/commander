package beads

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Shape checks keep a workstream tree buildable and readable: the Commander's
// plans are refused when they would leave a tree in a shape the board cannot
// show truthfully (duplicate steps, a later stage under way before an earlier
// one starts, the Commander's own check in Eric's column, ...). The rules run
// on a small in-memory model so the same code serves the write guard inside
// the transaction and the read-only `cmdr plan check` report.

// ShapeNode is one bead as the shape rules see it.
type ShapeNode struct {
	ID          string
	Title       string
	Status      string // open | in_progress | deferred | closed
	Labels      []string
	Parent      string
	Blockers    []string // ids this bead depends on (blocks edges)
	Responsible string
	Assigned    string
	HasReason   bool // description or a parked-from label explains a park
	// HasBrief: description and acceptance are both set.
	HasBrief bool
	// AnswerPending: the user's answer is recorded and not yet applied.
	AnswerPending bool
	// CloseReason is a closed bead's close reason (rule 27: a superseded one says why).
	CloseReason string
}

// ShapeViolation is one broken rule with the beads it names and the fix.
type ShapeViolation struct {
	Rule string `json:"rule"`
	// Number is the rule's number on the Rules tab (0 for a check with none).
	Number int      `json:"number,omitempty"`
	Beads  []string `json:"beads"`
	Why    string   `json:"why"`
	Fix    string   `json:"fix"`
}

// Key identifies a violation independent of wording, so a write is refused
// only for violations it introduces, not for ones the tree already had.
func (v ShapeViolation) Key() string {
	ids := append([]string(nil), v.Beads...)
	sort.Strings(ids)
	return v.Rule + "|" + strings.Join(ids, ",")
}

func (v ShapeViolation) String() string {
	n := v.Number
	if n == 0 {
		n = RuleNumber(v.Rule)
	}
	if n > 0 {
		return fmt.Sprintf("[rule %d] %s — fix: %s", n, v.Why, v.Fix)
	}
	return fmt.Sprintf("[%s] %s — fix: %s", v.Rule, v.Why, v.Fix)
}

type shapeTree struct {
	byID     map[string]*ShapeNode
	children map[string][]*ShapeNode
}

func newShapeTree(nodes []ShapeNode) shapeTree {
	t := shapeTree{byID: map[string]*ShapeNode{}, children: map[string][]*ShapeNode{}}
	for i := range nodes {
		n := &nodes[i]
		t.byID[n.ID] = n
	}
	for _, n := range t.byID {
		if n.Parent != "" {
			t.children[n.Parent] = append(t.children[n.Parent], n)
		}
	}
	for _, kids := range t.children {
		sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
	}
	return t
}

func (n *ShapeNode) has(label string) bool {
	for _, l := range n.Labels {
		if l == label {
			return true
		}
	}
	return false
}
func (n *ShapeNode) prefixed(p string) string {
	for _, l := range n.Labels {
		if v, ok := strings.CutPrefix(l, p); ok {
			return v
		}
	}
	return ""
}
func (n *ShapeNode) closed() bool { return n.Status == "closed" }
func (n *ShapeNode) parked() bool { return n.Status == "deferred" || n.has("parked") }
func (n *ShapeNode) gate() bool   { return n.has("gate") }
func (n *ShapeNode) waiting() bool {
	for _, l := range n.Labels {
		if strings.HasPrefix(l, "w:") || strings.HasPrefix(l, WaitLabelPrefix) {
			return true
		}
	}
	return false
}
func (n *ShapeNode) closing() bool {
	return n.has("closing-gate") || normTitle(n.Title) == "confirm done"
}
func (n *ShapeNode) started() bool {
	return n.Status == "in_progress" || n.closed() || n.waiting() || n.has("cmdr:executing")
}
func (n *ShapeNode) notStarted() bool {
	return n.Status == "open" && !n.waiting() && !n.has("cmdr:executing") && !n.parked()
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func normTitle(s string) string {
	return strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// Stages of a delivery workflow, in the order they must happen. 0 = unknown.
const (
	stageSpec = 1 + iota
	stageBuild
	stagePR
	stageCheck
	stageMerge
	stageLive
	stageDone
)

var stageNames = map[int]string{stageSpec: "spec", stageBuild: "build", stagePR: "PR green", stageCheck: "check", stageMerge: "merge greenlight", stageLive: "live", stageDone: "Confirm done"}

func shapeStage(n *ShapeNode) int {
	t := " " + normTitle(n.Title) + " "
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, " "+w+" ") {
				return true
			}
		}
		return false
	}
	switch {
	case n.closing():
		return stageDone
	case has("live check", "live", "confirm it s live", "watch"):
		if has("greenlight") && has("merge", "merging") {
			return stageMerge
		}
		return stageLive
	case has("greenlight") && has("merge", "merging", "override merge"):
		return stageMerge
	case n.reviewStep() || strings.HasPrefix(strings.TrimSpace(t), "commander check") || strings.HasPrefix(strings.TrimSpace(t), "commander review") || has("independent review"):
		return stageCheck
	case has("ci green", "get green", "get ci green", "open pr", "answer devin"):
		return stagePR
	case has("implement", "build"):
		return stageBuild
	case has("spec"):
		return stageSpec
	}
	return 0
}

var commanderOwnTitle = regexp.MustCompile(`(?i)^\s*(commander\b|my check\b)`)

// landsOnMainline: a step title that lands work on an app's mainline (its
// master branch or its git server), by merge, push or fast-forward.
var landsOnMainline = regexp.MustCompile(`\b(land|lands|landing|merge|merges|merging|push|pushes|pushing|fast forward|fast forwards|ff)\b.*\b(mainline|master|git server)\b`)

// releaseLandLabel is the app-release template's land-on-mainline step
// (harddeny.ReleaseLandLabel; beads cannot import harddeny).
const releaseLandLabel = "cmdr:release-land"

func (t shapeTree) isTrack(n *ShapeNode) bool {
	if n.has("kind:track") {
		return true
	}
	// Tracks are the workstream's direct non-gate children (they carry
	// kind:workstream themselves on most boards).
	p := t.byID[n.Parent]
	return p != nil && p.has("kind:workstream") && !n.gate()
}

// ShapeCheck runs every tree rule over the nodes (one or more workstream
// subtrees) and returns the violations in a stable order.
func ShapeCheck(nodes []ShapeNode) []ShapeViolation {
	t := newShapeTree(nodes)
	var out []ShapeViolation
	add := func(v ShapeViolation) { out = append(out, v) }

	parents := make([]string, 0, len(t.children))
	for p := range t.children {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		kids := t.children[p]
		// a. duplicates
		seen := map[string]*ShapeNode{}
		for _, k := range kids {
			if k.closed() {
				continue
			}
			key := normTitle(k.Title)
			if key == "" {
				continue
			}
			if prev, ok := seen[key]; ok {
				add(ShapeViolation{Rule: "duplicate-step", Beads: []string{prev.ID, k.ID},
					Why: fmt.Sprintf("%q appears twice under %s (%s and %s)", k.Title, p, prev.ID, k.ID),
					Fix: fmt.Sprintf("keep one; supersede the other (close %s with reason \"superseded by %s\") in this batch", k.ID, prev.ID)})
				continue
			}
			seen[key] = k
		}
		// b. stage order among siblings
		for _, late := range kids {
			ls := shapeStage(late)
			// A closed later step belongs to a finished cycle; only a later
			// step that is working or waiting now can be out of order.
			if ls == 0 || late.closing() || late.parked() || late.closed() || !late.started() {
				continue
			}
			for _, early := range kids {
				es := shapeStage(early)
				if es == 0 || es >= ls || !early.notStarted() {
					continue
				}
				// A review checks the work it waits on; it is out of order only
				// against work it actually waits on.
				if late.reviewStep() && !t.reaches(late.ID, early.ID, map[string]bool{}) {
					continue
				}
				add(ShapeViolation{Rule: "impossible-order", Beads: []string{early.ID, late.ID},
					Why: fmt.Sprintf("%q (%s) is under way while the earlier %s step %q has not started", late.Title, stageNames[ls], stageNames[es], early.Title),
					Fix: fmt.Sprintf("finish or close %s first and make %s wait on it (dep add %s %s); if %s is left from an earlier cycle, close or supersede it; if %s is no longer needed, supersede it", early.ID, late.ID, late.ID, early.ID, late.ID, early.ID)})
			}
		}
		// d. Confirm done asks only once siblings are finished
		for _, c := range kids {
			if !c.closing() || c.closed() || !c.has("needs-user") {
				continue
			}
			var open []string
			for _, s := range kids {
				if s != c && !s.closed() && !s.parked() {
					open = append(open, s.ID)
				}
			}
			if len(open) > 0 {
				add(ShapeViolation{Rule: "early-confirm-done", Beads: append([]string{c.ID}, open...),
					Why: fmt.Sprintf("%s asks Eric to confirm done while %d sibling step(s) are still open", c.ID, len(open)),
					Fix: fmt.Sprintf("remove needs-user from %s until %s are finished", c.ID, strings.Join(open, ", "))})
			}
		}
		// f/g. a merge greenlight sits behind a PR-green or Commander check step
		for _, g := range kids {
			if g.closed() || !g.gate() || shapeStage(g) != stageMerge {
				continue
			}
			var checks []string
			for _, s := range kids {
				if st := shapeStage(s); s != g && (st == stagePR || st == stageCheck) {
					checks = append(checks, s.ID)
				}
			}
			if len(checks) == 0 {
				add(ShapeViolation{Rule: "merge-without-check", Beads: []string{g.ID},
					Why: fmt.Sprintf("merge greenlight %s has no PR-green or review step before it", g.ID),
					Fix: "add a \"get the PR green and answer Devin\" step and a review step (lane:agents,review) before the merge greenlight"})
				continue
			}
			behind := false
			for _, b := range g.Blockers {
				for _, c := range checks {
					if b == c {
						behind = true
					}
				}
			}
			if !behind {
				add(ShapeViolation{Rule: "merge-not-gated", Beads: append([]string{g.ID}, checks...),
					Why: fmt.Sprintf("merge greenlight %s does not wait on its PR-green/check step (%s), so the card can show while the PR is red", g.ID, strings.Join(checks, ", ")),
					Fix: fmt.Sprintf("make the greenlight wait on its check: dep add %s %s", g.ID, checks[len(checks)-1])})
			}
		}
	}
	ids := make([]string, 0, len(t.byID))
	for id := range t.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := t.byID[id]
		if n.closed() {
			continue
		}
		// j. no hidden review: an agent's result reaches the user only through an
		// independent review step (run by a review agent), never directly. A
		// review already between them (done or not) counts; the gate's blockers
		// that are the user's own steps or in another track are not agent work.
		if n.gate() && n.prefixed("approver:") == "user" && !n.closing() {
			for _, bid := range n.Blockers {
				w, ok := t.byID[bid]
				if !ok || !w.agentWork() || w.Parent != n.Parent {
					continue
				}
				if t.reviewedBefore(n, w) {
					continue
				}
				add(ShapeViolation{Rule: "hidden-review", Beads: []string{w.ID, n.ID},
					Why: fmt.Sprintf("Eric's gate %q follows the agent step %q directly, with no independent review of its result", n.Title, w.Title),
					Fix: fmt.Sprintf("add a review step (labels lane:agents,review; title \"Review: <what to check>\"; description and acceptance saying what to check) that waits on %s, and make %s wait on it instead of on %s", w.ID, n.ID, w.ID)})
			}
		}
		// l. the Commander runs no steps: a check is an independent review step.
		if !n.closing() && (n.prefixed("lane:") == "commander" || (n.gate() && (n.prefixed("approver:") == "commander" || strings.HasPrefix(n.prefixed("approver:"), "manager")))) {
			add(ShapeViolation{Rule: "commander-step", Beads: []string{n.ID},
				Why: fmt.Sprintf("%q is a Commander step, but the Commander runs no steps: checks are done by an independent review agent", n.Title),
				Fix: fmt.Sprintf("make %s a review step: labels lane:agents,review (drop lane:commander, gate and approver:commander), title \"Review: ...\", with a description and acceptance saying what to check", n.ID)})
		}
		// m. a review checks agent work: it waits on an agent step in its track,
		// and no other review already checks that work.
		if n.reviewStep() {
			var work []string
			for _, bid := range n.Blockers {
				if w, ok := t.byID[bid]; ok && w.agentWork() && w.Parent == n.Parent {
					work = append(work, w.ID)
				}
			}
			if len(work) == 0 {
				add(ShapeViolation{Rule: "review-without-work", Beads: []string{n.ID},
					Why: fmt.Sprintf("review step %q waits on no agent step in its track, so it has no result to check", n.Title),
					Fix: fmt.Sprintf("make %s wait on the agent step whose result it checks, or supersede it", n.ID)})
			} else if other := t.otherReview(n, work); other != "" {
				add(ShapeViolation{Rule: "duplicate-review", Beads: []string{n.ID, other},
					Why: fmt.Sprintf("review step %q checks the same work as %s, which already reviews it", n.Title, other),
					Fix: fmt.Sprintf("supersede %s and keep its gate waiting on %s", n.ID, other)})
			}
		}
		// n. every step has an owner: lane:agents, lane:you (or a user gate),
		// lane:elsewhere[:who], or a typed wait. Nothing defaults to the Commander.
		if !n.gate() && !n.container() && len(t.children[n.ID]) == 0 && !n.ownerTagged() && !n.waiting() && !n.has("kind:gate") && !n.has("kind:task") {
			add(ShapeViolation{Rule: "no-owner-tag", Beads: []string{n.ID},
				Why: fmt.Sprintf("%q has no owner tag, so the board cannot say whose move it is", n.Title),
				Fix: fmt.Sprintf("label %s lane:agents (an agent runs it), lane:you (Eric does it), or lane:elsewhere:<who> (someone outside)", n.ID)})
		}
		// o. an agent step carries its brief: no agent starts without one.
		if !n.gate() && !n.closed() && !n.container() && len(t.children[n.ID]) == 0 && n.agentWork() && !n.HasBrief && n.prefixed("lane:") == "agents" {
			add(ShapeViolation{Rule: "no-brief", Beads: []string{n.ID},
				Why: fmt.Sprintf("agent step %q has no description and acceptance, so no agent can be started on it", n.Title),
				Fix: fmt.Sprintf("give %s a description (what to do) and acceptance (what counts as done)", n.ID)})
		}
		// c. the Commander's own steps stay in the Commander column
		if commanderOwnTitle.MatchString(n.Title) {
			lane := n.prefixed("lane:")
			approver := n.prefixed("approver:")
			if lane == "you" || strings.HasPrefix(lane, "elsewhere") || (n.gate() && approver != "" && approver != "commander") {
				add(ShapeViolation{Rule: "commander-lane", Beads: []string{n.ID},
					Why: fmt.Sprintf("%q is the Commander's own step but is placed in %s", n.Title, map[bool]string{true: "Eric's You column", false: "the Reviewers column"}[lane == "you" || approver == "user" || approver == "eric"]),
					Fix: fmt.Sprintf("use lane:commander (or approver:commander for a gate) on %s; \"you\" is Eric's column", n.ID)})
			}
		}
		// e. owner or wait; parked needs a reason
		if n.parked() {
			if !n.HasReason && n.prefixed("parked-from") == "" && n.prefixed("backlog:") == "" {
				add(ShapeViolation{Rule: "parked-without-reason", Beads: []string{n.ID},
					Why: fmt.Sprintf("%q is parked with no reason", n.Title),
					Fix: fmt.Sprintf("give %s a description saying why it is parked, or supersede it if it is stale", n.ID)})
			}
		} else if !n.gate() && n.Responsible == "" && n.Assigned == "" && n.prefixed("owner:") == "" && !n.waiting() && !n.has("kind:workstream") {
			add(ShapeViolation{Rule: "no-owner", Beads: []string{n.ID},
				Why: fmt.Sprintf("%q has no owner and no wait", n.Title),
				Fix: fmt.Sprintf("assign %s (owner or responsible agent) or record what it waits on", n.ID)})
		}
		// p. an app's mainline changes only through its release: a step that lands
		// work there is the app-release template's land-on-mainline step.
		if !n.container() && len(t.children[n.ID]) == 0 && !n.reviewStep() && !n.has(releaseLandLabel) && landsOnMainline.MatchString(normTitle(n.Title)) {
			add(ShapeViolation{Rule: "mainline-outside-release", Beads: []string{n.ID},
				Why: fmt.Sprintf("%q lands work on an app's mainline outside its release process", n.Title),
				Fix: fmt.Sprintf("supersede %s and stamp the release instead: cmdr plan template app-release --parent <its workstream> --subject \"<what ships>\"; only the release's land-on-mainline step (label %s) pushes to the mainline", n.ID, releaseLandLabel)})
		}
		// h. a live track has real steps, not only Confirm done
		if t.isTrack(n) && !n.parked() {
			kids := t.children[n.ID]
			if len(kids) > 0 {
				only := true
				for _, k := range kids {
					if !k.closing() {
						only = false
						break
					}
				}
				if only {
					add(ShapeViolation{Rule: "empty-track", Beads: []string{n.ID},
						Why: fmt.Sprintf("track %q has only Confirm done and no work or check steps", n.Title),
						Fix: fmt.Sprintf("add its first scoped step under %s, or park it with the reason", n.ID)})
				}
			}
		}
	}
	// b'. dependency cycles and steps started ahead of an unstarted blocker
	state := map[string]int{}
	var cycle []string
	var visit func(id string, path []string) bool
	visit = func(id string, path []string) bool {
		switch state[id] {
		case 1:
			cycle = append(path, id)
			return true
		case 2:
			return false
		}
		state[id] = 1
		if n := t.byID[id]; n != nil {
			for _, b := range n.Blockers {
				if _, ok := t.byID[b]; ok && visit(b, append(path, id)) {
					return true
				}
			}
		}
		state[id] = 2
		return false
	}
	for _, id := range ids {
		n := t.byID[id]
		// k. state: waits are well-formed, a clock never stands in for an
		// event, and a recorded answer is on a step that can still take it.
		for _, l := range n.Labels {
			if !strings.HasPrefix(l, WaitLabelPrefix) && !strings.HasPrefix(l, compactWaitPrefix) {
				continue
			}
			w, ok := ParseWaitLabel(l)
			if !ok {
				if n.closed() {
					continue
				}
				add(ShapeViolation{Rule: "bad-wait", Beads: []string{n.ID},
					Why: fmt.Sprintf("%q carries a wait label cmdrd cannot read (%s), so nothing will ever release it", n.Title, l),
					Fix: fmt.Sprintf("replace it with a typed wait: cmdr internal-set-objective --bead %s --waiting <kind>:<ref>", n.ID)})
				continue
			}
			if !n.closed() && w.Kind == WaitUntil && UserTimerLabel(w.Ref) {
				add(ShapeViolation{Rule: "time-wait-for-user", Beads: []string{n.ID},
					Why: fmt.Sprintf("%q waits on a clock for %q, something the user does, with no card for them", n.Title, w.Ref),
					Fix: fmt.Sprintf("put it on the user's list: cmdr internal-set-objective --bead %s --waiting none --state needs-user --decision '{question, context, input: true}'", n.ID)})
			}
			if !n.closed() && w.Kind == WaitUntil && EventTimerLabel(w.Ref) {
				add(ShapeViolation{Rule: "time-wait-for-event", Beads: []string{n.ID},
					Why: fmt.Sprintf("%q waits on a clock for %q, so the step sits idle after it happens", n.Title, w.Ref),
					Fix: "use the event wait cmdrd resolves itself: --waiting deploy:<sha>, pr:<merged|green|approved>:#N, or event:slack:<channel id>:<regexp> (optionally ,by=<RFC3339>)"})
			}
		}
	}
	for _, id := range ids {
		if state[id] == 0 && visit(id, nil) {
			add(ShapeViolation{Rule: "dependency-cycle", Beads: cycle,
				Why: "blocked-by chain loops: " + strings.Join(cycle, " → "),
				Fix: "remove one of the dependencies in the loop"})
			break
		}
	}
	return out
}

// shapeAdvisory rules are reported by `cmdr plan check` but never refuse a
// write: parking a step is a normal move and its reason usually lands in a
// comment the shape model does not read.
var shapeAdvisory = map[string]bool{"parked-without-reason": true}

// ShapeNew returns the violations in after that before did not have: a write
// is refused only for problems it introduces.
func ShapeNew(before, after []ShapeViolation) []ShapeViolation {
	had := map[string]bool{}
	for _, v := range before {
		had[v.Key()] = true
	}
	var out []ShapeViolation
	for _, v := range after {
		if !had[v.Key()] && !shapeAdvisory[v.Rule] {
			out = append(out, v)
		}
	}
	return out
}

// ShapeReshape enforces rule i on a batch: when a batch creates two or more
// steps under a parent that already had open steps, every one of those old
// steps must be referenced, closed or superseded in the same batch.
func ShapeReshape(before []ShapeNode, creates map[string]int, referenced map[string]bool) []ShapeViolation {
	t := newShapeTree(before)
	var out []ShapeViolation
	parents := make([]string, 0, len(creates))
	for p, n := range creates {
		if n >= 2 {
			parents = append(parents, p)
		}
	}
	sort.Strings(parents)
	for _, p := range parents {
		var left []string
		for _, k := range t.children[p] {
			if !k.closed() && !referenced[k.ID] && !k.closing() {
				left = append(left, k.ID)
			}
		}
		if len(left) > 0 {
			out = append(out, ShapeViolation{Rule: "reshape-unaccounted", Beads: append([]string{p}, left...),
				Why: fmt.Sprintf("this batch restructures %s but leaves its existing open steps %s untouched", p, strings.Join(left, ", ")),
				Fix: "for each old step: keep it (reference it, e.g. dep a new step on it), close it, or supersede it — in the same batch"})
		}
	}
	return out
}

// FormatShapeRefusal is the error text a refused write returns.
func FormatShapeRefusal(vs []ShapeViolation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "plan refused: %d tree shape problem(s); nothing was written.\n", len(vs))
	for _, v := range vs {
		b.WriteString("- ")
		b.WriteString(v.String())
		b.WriteString("\n")
	}
	b.WriteString("Fix these in the same batch and resubmit. `cmdr plan check --workstream <id>` lists a tree's problems.")
	return b.String()
}

// ShapeTransitions are the status changes a write may not make, comparing the
// tree before and after it. agent is true when an agent (any COMMANDER_ROLE)
// made the write: only the user closes their own gate, by answering it.
func ShapeTransitions(before, after []ShapeNode, agent bool) []ShapeViolation {
	was := map[string]ShapeNode{}
	for _, n := range before {
		was[n.ID] = n
	}
	var out []ShapeViolation
	for _, n := range after {
		old, ok := was[n.ID]
		if !ok || !agent || old.closed() || !n.closed() {
			continue
		}
		if !old.gate() || old.prefixed("approver:") != "user" || old.AnswerPending || n.has("superseded") || n.has("withdrawn") {
			continue
		}
		out = append(out, ShapeViolation{Rule: "gate-closed-without-answer", Beads: []string{n.ID},
			Why: fmt.Sprintf("an agent closed the user's gate %q, but the user has not answered it", n.Title),
			Fix: fmt.Sprintf("leave %s open for the user's card; if the user said yes in chat, record it: cmdr internal-resolve-needs --bead %s --action approve --relayed '<when, and their exact words>'", n.ID, n.ID)})
	}
	return out
}

var reviewTitle = regexp.MustCompile(`(?i)^\s*(review\b|independent review|review agent|commander checks?\b|my check\b)`)

// reviewStep: an independent review step (the old Commander checks included).
func (n *ShapeNode) reviewStep() bool {
	if n.gate() {
		return n.prefixed("approver:") == "commander"
	}
	// "review" is also an old Operator task's state label: a task is never a review step.
	if n.has("kind:task") || n.has("kind:gate") {
		return false
	}
	return n.prefixed("lane:") == "commander" || n.has("review") || reviewTitle.MatchString(n.Title)
}

// agentWork: a step an agent does whose result someone must be able to trust:
// not a gate, not a review, not the user's or someone outside's.
func (n *ShapeNode) agentWork() bool {
	if n.automaticStep() {
		return false
	}
	lane := n.prefixed("lane:")
	return !n.gate() && !n.reviewStep() && !n.closing() && (lane == "" || lane == "agents") && n.prefixed("approver:") == ""
}

// reviewedBefore: another blocker of gate g is a review step that waits,
// directly or through other steps, on w.
func (t shapeTree) reviewedBefore(g, w *ShapeNode) bool {
	for _, bid := range g.Blockers {
		c, ok := t.byID[bid]
		if !ok || c.ID == w.ID || !c.reviewStep() {
			continue
		}
		if t.reaches(c.ID, w.ID, map[string]bool{}) {
			return true
		}
	}
	return false
}

func (t shapeTree) reaches(from, to string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	n, ok := t.byID[from]
	if !ok {
		return false
	}
	for _, b := range n.Blockers {
		if b == to || t.reaches(b, to, seen) {
			return true
		}
	}
	return false
}

// otherReview is a review step other than r that already checks one of work:
// a done one, or an open one created before r.
func (t shapeTree) otherReview(r *ShapeNode, work []string) string {
	for _, n := range t.byID {
		if n.ID == r.ID || !n.reviewStep() || n.Parent != r.Parent || n.has("superseded") {
			continue
		}
		if !n.closed() && !idBefore(n.ID, r.ID) {
			continue
		}
		for _, b := range n.Blockers {
			for _, w := range work {
				if b == w {
					return n.ID
				}
			}
		}
	}
	return ""
}

// idBefore orders bead ids by their numeric suffixes (x.9 before x.12).
func idBefore(a, b string) bool {
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

// container: a workstream or track bead, which holds steps.
func (n *ShapeNode) container() bool {
	return n.has("kind:workstream") || n.has("kind:track")
}

// ownerTagged: a label says whose step it is: a lane, the user's card
// (needs-user), an agent runtime or a named owner.
func (n *ShapeNode) ownerTagged() bool {
	return n.prefixed("lane:") != "" || n.has("needs-user") || n.prefixed("runtime:") != "" || n.prefixed("owner:") != ""
}
