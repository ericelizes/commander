// Package nextactor decides, from recorded facts only, who has to make the
// next move on a bead: the user, an agent, the planning Commander, someone
// external (CI, a review, a date), nobody (paused or done), or nobody although
// an agent owes it (stuck). The sidebar and supervision both use it, so what
// the user sees and what gets nudged cannot disagree.
package nextactor

import (
	"fmt"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
)

// Actor is who makes the next move.
type Actor string

const (
	User      Actor = "user"      // the user decides, approves or answers
	Stuck     Actor = "stuck"     // an agent owes the move and is not making it
	Agent     Actor = "agent"     // an execution agent has it
	Commander Actor = "commander" // the planning Commander has it
	External  Actor = "external"  // CI, a review, a named person or a date
	Done      Actor = "done"      // everything finished: the user closes it with Complete
	None      Actor = "none"      // paused, withdrawn, done, or carried by its children
)

// Thresholds after which an owed move counts as stuck.
const (
	CommanderIdleStuck  = 10 * time.Minute // planning owed while Commander sits idle
	NoProgressStuck     = 20 * time.Minute // an executing agent, idle, recording no progress
	BusyNoProgressStuck = 60 * time.Minute // an executing agent mid-turn recording no progress
	UnverifiedStuck     = 5 * time.Minute  // a claim with no live process evidence
	maxChainDepth       = 32
)

// Bead is what the decision reads about one bead. Every field is a recorded
// fact; nothing here is inferred from chat text.
type Bead struct {
	ID, Title, Status string
	Superseded        bool // dropped from the plan
	Withdrawn         bool // a withdrawn or retired request
	Held              bool // the hold label: paused by recorded direction
	HoldReason        string
	ClosingGate       bool // the workstream's "Confirm done" step: nobody's move until the rest is finished
	ReadyToClose      bool // a closing gate whose workstream has nothing else open

	NeedsUser          bool   // needs-user state, a user gate or an open decision card
	UserAsk            string // what the user is asked, when known
	HumanAnswerPending bool   // the user answered; the planning Commander must apply it
	PendingQuestions   int    // agent questions waiting for the planning Commander
	QuestionWaiting    bool   // this bead's agent waits on a blocking answer

	Waits     []beads.Wait
	BlockedBy []string // ids of open blocking beads, resolved through Context.Lookup

	HasOpenChildren bool     // children carry the state unless the bead itself says more
	Children        []string // ids of its open children, resolved through Context.Lookup

	Executing           bool   // an agent is verifiably running it
	ExecWorking         bool   // and is in a turn right now (not idle)
	ExecRole            string // that agent
	ExecSince           int64
	ProgressAt          int64 // last recorded progress on the bead
	Unverified          bool  // a claim with no positive process evidence yet
	UnverifiedSince     int64
	ExitedWithoutReport bool // its agent stopped and left no result
	ExitedAt            int64
	LaunchFailed        bool
	LaunchError         string
	ReadyQueued         bool // marked ready, waiting for capacity
	NoCapableSlot       bool // marked ready, but no slot can run it
	ResultReady         bool // a recorded result waits for review
	ResultSince         int64
	PlanningPending     bool

	PlanningCommander string
	UsageCapacity     string // eligible queued execution exhausted connected accounts
	PlanningUsage     string // recorded planner runtime cannot serve this move
}

// Context supplies what the decision needs beyond one bead.
type Context struct {
	Now time.Time
	// Commander reports whether a planning Commander is in a turn now and,
	// if not, since when it has been idle (unix s; 0 = unknown).
	Commander func(id string) (busy bool, idleSince int64)
	// Lookup resolves a blocking bead. Nil means chains are not followed:
	// a blocked bead then reads as Actor None, reason "dependency".
	Lookup func(id string) (Bead, bool)
	// Memo, when set, remembers each bead's chain decision for the life of the
	// context (one board build), so shared blockers are decided once.
	Memo map[string]Result
}

// Result is the decision for one bead.
type Result struct {
	Actor  Actor    `json:"actor"`
	Reason string   `json:"reason"`        // stable machine code
	Who    string   `json:"who,omitempty"` // the person, CI target, agent or Commander
	Text   string   `json:"text"`          // deterministic one-liner; descriptions replace it
	Since  int64    `json:"since,omitempty"`
	Bead   string   `json:"bead,omitempty"` // where the actor comes from (a chain's root)
	Via    []string `json:"via,omitempty"`  // titles of the blocking beads in between
}

// Decide returns who makes the next move on b.
func Decide(b Bead, ctx Context) Result {
	r := decide(b, ctx, map[string]bool{}, 0)
	if len(r.Via) > 0 {
		r.Text = blockedText(r)
	}
	return r
}

// resolve is decide, descending into a parent whose children carry its state:
// the most urgent move among them.
func resolve(b Bead, ctx Context, seen map[string]bool, depth int) Result {
	if ctx.Memo != nil {
		if r, ok := ctx.Memo[b.ID]; ok {
			return r
		}
		r := resolveOnce(b, ctx, seen, depth)
		ctx.Memo[b.ID] = r
		return r
	}
	return resolveOnce(b, ctx, seen, depth)
}

func resolveOnce(b Bead, ctx Context, seen map[string]bool, depth int) Result {
	r := decide(b, ctx, seen, depth)
	if r.Actor != None || r.Reason != "children" || ctx.Lookup == nil || depth >= maxChainDepth || seen[b.ID] {
		return r
	}
	seen[b.ID] = true
	defer delete(seen, b.ID)
	var best Result
	found := false
	for _, id := range b.Children {
		child, ok := ctx.Lookup(id)
		if !ok || seen[id] {
			continue
		}
		c := resolve(child, ctx, seen, depth+1)
		if c.Actor == None && (c.Reason == "done" || c.Reason == "withdrawn" || c.Reason == "children" || c.Reason == "closing") {
			continue
		}
		if !found || Rank(c.Actor) > Rank(best.Actor) {
			best, found = c, true
		}
	}
	if !found {
		return r
	}
	return best
}

// childAsksUser: some open step below b has a question for the user.
func childAsksUser(b Bead, ctx Context, seen map[string]bool, depth int) bool {
	if depth >= maxChainDepth || seen[b.ID] {
		return false
	}
	seen[b.ID] = true
	defer delete(seen, b.ID)
	for _, id := range b.Children {
		if child, ok := ctx.Lookup(id); ok && !seen[id] && resolve(child, ctx, seen, depth+1).Actor == User {
			return true
		}
	}
	return false
}

func userWait(b Bead) *beads.Wait {
	for i := range b.Waits {
		if b.Waits[i].Kind == beads.WaitUser {
			return &b.Waits[i]
		}
	}
	return nil
}

// recordQuestion: b waits on the user but nothing recorded says what for, so
// writing it down is the planning Commander's move, not a decision the user
// can make.
func recordQuestion(b Bead, ctx Context) Result {
	r := commanderOwed(b, ctx, "record_question", "write down what "+quoted(b.Title)+" needs from you", unix(userWait(b).Since))
	r.Bead = b.ID
	return r
}

func decide(b Bead, ctx Context, seen map[string]bool, depth int) Result {
	r := own(b, ctx)
	// A card is the user's move only once what it waits on is done: until
	// then it waits on its blocker.
	if r.Actor == User && len(b.BlockedBy) > 0 {
		if ctx.Lookup == nil {
			return Result{Actor: None, Reason: "dependency", Text: "Waiting on another step", Bead: b.ID}
		}
		if blocked, ok := blockers(b, ctx, seen, depth); ok {
			return blocked
		}
	}
	if r.Actor != "" {
		r.Bead = firstNonEmpty(r.Bead, b.ID)
		return r
	}
	if len(b.BlockedBy) > 0 {
		if ctx.Lookup == nil {
			return Result{Actor: None, Reason: "dependency", Text: "Waiting on another step", Bead: b.ID}
		}
		if blocked, ok := blockers(b, ctx, seen, depth); ok {
			return blocked
		}
	}
	if b.HasOpenChildren {
		if userWait(b) != nil && strings.TrimSpace(b.UserAsk) == "" && ctx.Lookup != nil && !childAsksUser(b, ctx, seen, depth) {
			return recordQuestion(b, ctx)
		}
		return Result{Actor: None, Reason: "children", Text: "Carried by its steps", Bead: b.ID}
	}
	if b.ReadyQueued {
		if b.UsageCapacity != "" {
			return Result{Actor: Stuck, Reason: "usage_capacity", Text: b.UsageCapacity, Bead: b.ID}
		}
		if b.NoCapableSlot {
			return Result{Actor: Stuck, Reason: "no_capable_slot", Text: "Stuck: no agent slot can run " + quoted(b.Title), Bead: b.ID}
		}
		return Result{Actor: Agent, Reason: "queued", Text: "Queued for the next free agent", Bead: b.ID}
	}
	r = commanderOwed(b, ctx, "plan", "plan the next step", 0)
	r.Bead = b.ID
	return r
}

// own is the decision from b's own facts, or Actor "" when they settle nothing.
func own(b Bead, ctx Context) Result {
	now := ctx.Now.Unix()
	switch {
	case b.Status == "closed":
		return Result{Actor: None, Reason: "done", Text: "Done"}
	case b.Superseded || b.Withdrawn:
		return Result{Actor: None, Reason: "withdrawn", Text: "Withdrawn"}
	case b.HumanAnswerPending:
		return commanderOwed(b, ctx, "apply_answer", "apply your recorded answer", 0)
	case b.Status == beads.StatusDeferred || b.Held:
		text := "Paused"
		if b.HoldReason != "" {
			text += ": " + b.HoldReason
		}
		return Result{Actor: None, Reason: "paused", Text: text}
	// A Confirm done step is the Complete button, never a card: a card left on
	// one (from before cards were refused there) does not replace it.
	case b.ClosingGate && b.ReadyToClose:
		return Result{Actor: Done, Reason: "ready_to_close", Text: "Complete"}
	case b.ClosingGate:
		return Result{Actor: None, Reason: "closing", Text: "Complete once the rest is finished"}
	case b.NeedsUser:
		return Result{Actor: User, Reason: "decision", Text: "Needs you: " + firstNonEmpty(b.UserAsk, b.Title)}
	case b.PendingQuestions > 0 || b.QuestionWaiting:
		return commanderOwed(b, ctx, "answer_question", "answer an agent's question", 0)
	case b.LaunchFailed:
		return Result{Actor: Stuck, Reason: "launch_failed", Who: b.ExecRole, Text: "Stuck: the agent could not start" + suffix(b.LaunchError)}
	case b.ExitedWithoutReport:
		return Result{Actor: Stuck, Reason: "exited_without_report", Who: b.ExecRole, Since: b.ExitedAt, Text: "Stuck: the agent stopped without a report"}
	case b.Executing:
		since := max(b.ExecSince, b.ProgressAt)
		limit := NoProgressStuck
		if b.ExecWorking {
			limit = BusyNoProgressStuck // a long build or test run is still a turn in progress
		}
		if since > 0 && now-since > int64(limit/time.Second) {
			return Result{Actor: Stuck, Reason: "no_progress", Who: b.ExecRole, Since: since, Text: fmt.Sprintf("Stuck: no progress for %s", ago(now-since))}
		}
		return Result{Actor: Agent, Reason: "executing", Who: b.ExecRole, Since: b.ExecSince, Text: "Working on " + quoted(b.Title)}
	case b.Unverified:
		if b.UnverifiedSince > 0 && now-b.UnverifiedSince > int64(UnverifiedStuck/time.Second) {
			return Result{Actor: Stuck, Reason: "unverified", Who: b.ExecRole, Since: b.UnverifiedSince, Text: "Stuck: claimed, but no agent process is running"}
		}
		return Result{Actor: Agent, Reason: "starting", Who: b.ExecRole, Since: b.UnverifiedSince, Text: "Starting an agent on " + quoted(b.Title)}
	case b.ResultReady:
		return commanderOwed(b, ctx, "review_result", "review the finished result", b.ResultSince)
	}
	return waits(b, ctx)
}

// waits decides from typed waits: the user first, then anything external.
func waits(b Bead, ctx Context) Result {
	var external, due *beads.Wait
	for i := range b.Waits {
		w := b.Waits[i]
		switch {
		case w.Kind == beads.WaitUser:
			if strings.TrimSpace(b.UserAsk) == "" {
				if b.HasOpenChildren {
					// The question may sit on a step below; decide looks there.
					continue
				}
				return recordQuestion(b, ctx)
			}
			return Result{Actor: User, Reason: "waiting_user", Since: unix(w.Since), Text: "Needs you: " + b.UserAsk}
		case beads.IsManagerHandoff(w):
			if due == nil {
				due = &b.Waits[i]
			}
		case w.Kind == beads.WaitUntil:
			if ctx.Now.Before(w.Until) {
				if external == nil {
					external = &b.Waits[i]
				}
			} else if due == nil {
				due = &b.Waits[i]
			}
		case w.Kind == beads.WaitOperator:
			return Result{Actor: Agent, Reason: "waiting_agent", Who: w.Ref, Since: unix(w.Since), Text: "Waiting on " + firstNonEmpty(w.Ref, "an agent")}
		default:
			if external == nil {
				external = &b.Waits[i]
			}
		}
	}
	if external != nil {
		return Result{Actor: External, Reason: "wait:" + external.Kind, Who: waitWho(*external), Since: unix(external.Since), Text: "Waiting on " + waitWho(*external)}
	}
	if due != nil {
		return commanderOwed(b, ctx, "wait_over", "act on "+quoted(due.Text()), unix(due.Since))
	}
	return Result{}
}

// blockers follows open blocking beads to the move that frees them.
func blockers(b Bead, ctx Context, seen map[string]bool, depth int) (Result, bool) {
	if depth >= maxChainDepth || seen[b.ID] {
		return Result{}, false
	}
	seen[b.ID] = true
	defer delete(seen, b.ID)
	var best Result
	found := false
	for _, id := range b.BlockedBy {
		dep, ok := ctx.Lookup(id)
		if !ok || dep.Status == "closed" || seen[id] {
			continue
		}
		r := resolve(dep, ctx, seen, depth+1)
		if r.Actor == None && (r.Reason == "done" || r.Reason == "withdrawn" || r.Reason == "children" || r.Reason == "closing") {
			continue
		}
		r.Via = append([]string{dep.Title}, r.Via...)
		if !strings.HasPrefix(r.Reason, "blocked:") {
			r.Reason = "blocked:" + r.Reason
		}
		if !found || Rank(r.Actor) > Rank(best.Actor) {
			best, found = r, true
		}
	}
	return best, found
}

// commanderOwed is a move the planning Commander owes: its own while it is in a
// turn or within the grace after going idle, stuck once it sits idle longer.
func commanderOwed(b Bead, ctx Context, reason, what string, since int64) Result {
	if b.PlanningUsage != "" && (reason == "plan" || reason == "wait_over") {
		return Result{Actor: Stuck, Reason: "planning_usage", Text: b.PlanningUsage, Bead: b.ID}
	}
	who := firstNonEmpty(b.PlanningCommander, "commander")
	busy, idleSince := false, int64(0)
	if ctx.Commander != nil {
		busy, idleSince = ctx.Commander(who)
	}
	if busy {
		return Result{Actor: Commander, Reason: reason, Who: who, Since: since, Text: "Commander: " + what}
	}
	waiting := max(since, idleSince)
	if waiting > 0 && ctx.Now.Unix()-waiting > int64(CommanderIdleStuck/time.Second) {
		return Result{Actor: Stuck, Reason: "commander_idle:" + reason, Who: who, Since: waiting, Text: fmt.Sprintf("Stuck: Commander has not started to %s (idle %s)", what, ago(ctx.Now.Unix()-waiting))}
	}
	return Result{Actor: Commander, Reason: reason, Who: who, Since: since, Text: "Commander to " + what}
}

// Rank orders actors for a row: the user first, then stuck work, then work in
// hand, then external waits, then nothing.
func Rank(a Actor) int {
	switch a {
	case User:
		return 6
	case Stuck:
		return 5
	case Agent, Commander:
		return 4
	case External:
		return 3
	case Done:
		return 2
	case None:
		return 1
	}
	return 0
}

// Row is a subtree's state: the leading decision and how many beads each
// actor holds.
type Row struct {
	Lead   Result        `json:"lead"`
	Counts map[Actor]int `json:"counts"`
	Agents int           `json:"agents"` // distinct executing agents
}

// Rollup combines decisions into a row. The lead is the highest-ranked actor;
// among equal agents, one running a step itself leads over a step that only
// waits for one (the row is working, not waiting); otherwise the oldest.
// Beads carried by their children do not count.
func Rollup(results []Result) Row {
	row := Row{Counts: map[Actor]int{}}
	agents := map[string]bool{}
	for _, r := range results {
		if r.Actor == None && (r.Reason == "children" || r.Reason == "closing") {
			continue
		}
		row.Counts[r.Actor]++
		if r.Actor == Agent && r.Reason == "executing" {
			agents[firstNonEmpty(r.Who, r.Bead)] = true
		}
		running := func(x Result) bool { return x.Actor == Agent && len(x.Via) == 0 }
		same := Rank(r.Actor) == Rank(row.Lead.Actor)
		if row.Lead.Actor == "" || Rank(r.Actor) > Rank(row.Lead.Actor) ||
			(same && running(r) && !running(row.Lead)) ||
			(same && running(r) == running(row.Lead) && r.Since > 0 && (row.Lead.Since == 0 || r.Since < row.Lead.Since)) {
			row.Lead = r
		}
	}
	row.Agents = len(agents)
	if row.Lead.Actor == "" {
		row.Lead = Result{Actor: None, Reason: "empty", Text: "Nothing open"}
	}
	return row
}

// Summary is the row's deterministic line: the lead, plus what else is going on.
func (r Row) Summary() string {
	parts := []string{r.Lead.Text}
	if r.Lead.Actor != Agent && r.Agents > 0 {
		parts = append(parts, plural(r.Agents, "agent", "agents")+" working")
	}
	if n := r.Counts[External]; n > 0 && r.Lead.Actor != External {
		parts = append(parts, fmt.Sprintf("%d waiting outside", n))
	}
	if n := r.Counts[Stuck]; n > 0 && r.Lead.Actor != Stuck {
		parts = append(parts, fmt.Sprintf("%d stuck", n))
	}
	return strings.Join(parts, " · ")
}

func blockedText(r Result) string {
	nearest := quoted(firstNonEmpty(firstOf(r.Via), "another step"))
	switch r.Actor {
	case User, External, Stuck:
		return strings.TrimSuffix(r.Text, ".") + " (blocking " + nearest + ")"
	default:
		return "After " + nearest + ": " + lowerFirst(r.Text)
	}
}

func waitWho(w beads.Wait) string {
	switch w.Kind {
	case beads.WaitReview:
		if w.Ref != "" {
			return "review by " + w.Ref
		}
		return "review"
	case beads.WaitUntil:
		label := strings.TrimSpace(w.Ref)
		date := w.Until.UTC().Format("Jan 2 15:04") + " UTC"
		if label == "" {
			return date
		}
		return label + " (" + date + ")"
	default:
		return w.Text()
	}
}

func ago(s int64) string {
	d := time.Duration(s) * time.Second
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute", "minutes")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour", "hours")
	}
	return plural(int(d/(24*time.Hour)), "day", "days")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func quoted(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:79]) + "…"
	}
	return "“" + s + "”"
}

func suffix(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return " (" + s + ")"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToLower(string(r[0])) + string(r[1:])
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
