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
	CommanderBusyStuck  = 20 * time.Minute // a move owed while the Commander is busy with other work
	NoProgressStuck     = 20 * time.Minute // an executing agent, idle, recording no progress
	BusyNoProgressStuck = 60 * time.Minute // an executing agent mid-turn recording no progress
	LoopStuck           = 3 * time.Hour    // an agent active all along that has recorded nothing (looping)
	UnverifiedStuck     = 5 * time.Minute  // a claim with no live process evidence
	CutoverStuck        = 15 * time.Minute // an agent asked to stop that is still running (cmdrd stops it at 10)
	maxChainDepth       = 32
)

// Bead is what the decision reads about one bead. Every field is a recorded
// fact; nothing here is inferred from chat text.
type Bead struct {
	ID, Title, Status string
	Superseded        bool // dropped from the plan
	Withdrawn         bool // a withdrawn or retired request
	// Held: an old hold label. cmdrd still starts nothing under it, and a step
	// is never paused, so it reads as an invalid state until it is cleared.
	Held bool
	// UnderParked: an ancestor is parked (status deferred), so the park is read
	// on that container, not on this step.
	UnderParked bool
	// ParkedUntil: a park with a time (unix s), which cmdrd watches and ends.
	ParkedUntil  int64
	ClosingGate  bool // the workstream's "Confirm done" step: nobody's move until the rest is finished
	ReadyToClose bool // a closing gate whose workstream has nothing else open

	NeedsUser          bool   // needs-user state, a user gate or an open decision card
	UserAsk            string // what the user is asked, when known
	HumanAnswerPending bool   // the user answered; the planning Commander must apply it
	// CheckStep is the title of the explicit Commander check step that follows
	// this step (lane commander, blocked on it): a finished result waiting on the
	// Commander points at that step, never at a hidden review.
	CheckStep string
	// Lane is the step's lane label (you, agents, commander, elsewhere[:who]).
	Lane string
	// Container: a workstream or track (it holds steps), never a step an agent runs.
	Container bool
	// ReviewStep: an independent review step, run by a review agent.
	ReviewStep bool
	// NoBrief: no description or acceptance, so no agent can be started on it.
	NoBrief bool
	// Gate: a gate (approved, never run by an agent); Answered: the user
	// answered its card and turning the answer into steps is owed.
	Gate, Answered bool
	// ResultClean: the result is a clean final report, which closes the step
	// itself; ResultLine is its first line.
	ResultClean bool
	ResultLine  string
	// CardsKnown: NeedsUser was computed from the user's real NEEDS YOU list, so a
	// user wait with no listed card is the Commander's move (put the card up), not the user's.
	CardsKnown       bool
	PendingQuestions int  // agent questions waiting for the planning Commander
	QuestionWaiting  bool // this bead's agent waits on a blocking answer

	Waits     []beads.Wait
	BlockedBy []string // ids of open blocking beads, resolved through Context.Lookup

	HasOpenChildren bool     // children carry the state unless the bead itself says more
	Children        []string // ids of its open children, resolved through Context.Lookup

	Executing           bool   // an agent is verifiably running it
	ExecWorking         bool   // and is in a turn right now (not idle)
	ExecRole            string // that agent
	ExecSince           int64
	ExecActiveAt        int64  // its agent's last hook or tool event (0 = unknown)
	MergeWait           string // a merge card held until its PR is green and mergeable: what it waits on
	ProgressAt          int64  // last recorded progress on the bead
	Unverified          bool   // a claim with no positive process evidence yet
	UnverifiedSince     int64
	ExitedWithoutReport bool // its agent stopped and left no result
	ExitedAt            int64
	StopReason          string // "Agent stopped: ..." when its agent crashed (store.AgentStopped)
	LaunchFailed        bool
	LaunchError         string
	ReadyQueued         bool // marked ready, waiting for capacity
	NoCapableSlot       bool // marked ready, but no slot can run it
	ResultReady         bool // a recorded result waits for review
	ResultSince         int64
	PlanningPending     bool

	WantRuntime  string // the runtime the step asks for: its runtime: label or its nearest ancestor's
	RunRuntime   string // the runtime its running agent is on
	Cutover      bool   // Commander asked its agent to save its work and stop
	CutoverTo    string // then continue on this runtime ("" = stay stopped)
	CutoverFrom  string
	CutoverSince int64
	Stopped      bool // Commander stopped its agent (cmdr execution stop): re-planning is owed

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
	// CommanderOn reports whether the Commander's current activity is about
	// this bead. Being in a turn is not that: one chat carries every workstream.
	CommanderOn func(who, bead string) bool
	// Lookup resolves a blocking bead. Nil means chains are not followed:
	// a blocked bead then reads as Actor None, reason "dependency".
	Lookup func(id string) (Bead, bool)
	// Memo, when set, remembers each bead's chain decision for the life of the
	// context (one board build), so shared blockers are decided once.
	Memo map[string]Result
}

// Result is the decision for one bead.
type Result struct {
	Active bool     `json:"active,omitempty"` // the Commander is doing this move right now, not only owing it
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
	return Result{Actor: Stuck, Reason: InvalidPrefix + "no-card", Since: unix(userWait(b).Since), Bead: b.ID,
		Text: "Invalid state: " + quoted(b.Title) + " waits on you but has no card"}
}

func decide(b Bead, ctx Context, seen map[string]bool, depth int) Result {
	r := own(b, ctx)
	// A card is the user's move only once what it waits on is done: until
	// then it waits on its blocker. The same holds for a finished or stopped
	// run with an open blocker: the blocker is the follow-up the Commander
	// planned in answer to that run, so the step waits on it, not on a review.
	// The same holds for steps under it: a step re-planned into child steps
	// that have newer work is carried by them, not by its old outcome.
	if answeredByBlocker(r) && supersededByChildren(b, r, ctx) {
		return Result{Actor: None, Reason: "children", Text: "Carried by its steps", Bead: b.ID}
	}
	if (r.Actor == User || answeredByBlocker(r)) && len(b.BlockedBy) > 0 {
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
		if b.Cutover && b.CutoverTo != "" {
			to := runtimeName(b.CutoverTo)
			return Result{Actor: Agent, Reason: "cutover_starting", Since: b.CutoverSince, Text: "Switching to " + to + ": starting a " + to + " agent", Bead: b.ID}
		}
		if b.StopReason != "" {
			return Result{Actor: Agent, Reason: "queued", Text: b.StopReason, Bead: b.ID}
		}
		return Result{Actor: Agent, Reason: "queued", Text: "Queued for the next free agent", Bead: b.ID}
	}
	return unowned(b)
}

// InvalidPrefix starts the reason of a state the bead structure does not allow:
// the board shows it red and the Commander fixes the beads.
const InvalidPrefix = "invalid:"

// StructurePrefix starts the reason of the Commander's one kind of move: turning
// the user's answer into bead structure.
const StructurePrefix = "structure:"

// unowned is an open step nothing else settles: its owner tag says who moves.
// An agent step is queued for an agent (cmdrd starts one); anything else with
// no move is an invalid state, never a made-up Commander line.
func unowned(b Bead) Result {
	r := Result{Bead: b.ID, Since: b.ProgressAt}
	lane := b.Lane
	switch {
	case b.Stopped:
		r.Actor, r.Reason, r.Text = Stuck, "agent_stopped", "Stopped: the agent on "+quoted(b.Title)+" was stopped"
	case b.Gate && b.Answered:
		r.Actor, r.Reason, r.Text = Commander, StructurePrefix+"answer", "Commander: turn your answer on "+quoted(b.Title)+" into steps"
	case b.Gate:
		r.Actor, r.Reason, r.Text = Stuck, InvalidPrefix+"no-card", "Invalid state: the gate "+quoted(b.Title)+" has no card for you"
	case b.Container:
		r.Actor, r.Reason, r.Text = Stuck, InvalidPrefix+"no-open-step", "Invalid state: nothing open under "+quoted(b.Title)
	case lane == "you":
		r.Actor, r.Reason, r.Text = Stuck, InvalidPrefix+"no-card", "Invalid state: "+quoted(b.Title)+" is yours but has no card"
	case strings.HasPrefix(lane, "elsewhere"):
		who := strings.TrimPrefix(strings.TrimPrefix(lane, "elsewhere"), ":")
		r.Actor, r.Reason, r.Text = External, "elsewhere", "Waiting on "+firstNonEmpty(who, "someone outside")+": "+quoted(b.Title)
	case b.NoBrief:
		r.Actor, r.Reason, r.Text = Stuck, InvalidPrefix+"no-brief", "Invalid state: "+quoted(b.Title)+" has no description and acceptance, so no agent can start"
	case b.ReviewStep:
		r.Actor, r.Reason, r.Text = Agent, "ready", "Queued for a review agent: "+quoted(b.Title)
	default:
		r.Actor, r.Reason, r.Text = Agent, "ready", "Queued for an agent: "+quoted(b.Title)
	}
	return r
}

// answeredByBlocker: a run outcome (a result to review, a stop, an exit with
// no report) that an open blocker on the same step supersedes.
func answeredByBlocker(r Result) bool {
	switch strings.TrimPrefix(r.Reason, "commander_idle:") {
	case "review_result", "agent_stopped", "exited_without_report", "result_accepting", "agent_blocked":
		return true
	}
	return false
}

// supersededByChildren: b's run outcome predates work on its open child steps
// (an agent on one, or progress or a wait recorded after the outcome), so the
// outcome was answered by re-planning the step into those children.
func supersededByChildren(b Bead, r Result, ctx Context) bool {
	if !b.HasOpenChildren || ctx.Lookup == nil {
		return false
	}
	for _, id := range b.Children {
		c, ok := ctx.Lookup(id)
		if !ok || c.Status == "closed" || c.Superseded || c.Withdrawn {
			continue
		}
		if c.Executing || c.Unverified || c.ReadyQueued {
			return true
		}
		newest := max(c.ProgressAt, c.ExecSince)
		for _, w := range c.Waits {
			newest = max(newest, unix(w.Since))
		}
		if newest > r.Since {
			return true
		}
	}
	return false
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
		return Result{Actor: Agent, Reason: "apply_answer", Who: "cmdrd", Text: "Applying your answer to " + quoted(b.Title)}
	// A parked workstream or track (and what sits under it) is out of play, and
	// a park with a time is a wait cmdrd ends. A parked or held step is not:
	// whether an agent runs follows from the tree alone, and what must happen
	// first is a bead the step is blocked by.
	case b.Status == beads.StatusDeferred && (b.Container || b.UnderParked || b.ParkedUntil != 0):
		return Result{Actor: None, Reason: "paused", Text: "Paused"}
	case b.Held:
		return Result{Actor: Stuck, Reason: InvalidPrefix + "hold", Text: "Invalid state: " + quoted(b.Title) + " is on hold, so nothing will start it: put what it waits for in front of it as its own bead (a card or a gate), then clear the hold"}
	case b.Status == beads.StatusDeferred:
		return Result{Actor: Stuck, Reason: InvalidPrefix + "parked", Text: "Invalid state: " + quoted(b.Title) + " is parked, so nothing will start it: put what it waits for in front of it as its own bead (a card or a gate), then unpark it"}
	// A Confirm done step is the Complete button, never a card: a card left on
	// one (from before cards were refused there) does not replace it.
	case b.ClosingGate && b.ReadyToClose:
		return Result{Actor: Done, Reason: "ready_to_close", Text: "Complete"}
	case b.ClosingGate:
		return Result{Actor: None, Reason: "closing", Text: "Complete once the rest is finished"}
	case b.MergeWait != "":
		return Result{Actor: External, Reason: "merge_wait", Text: "Waiting on " + b.MergeWait}
	case b.NeedsUser:
		return Result{Actor: User, Reason: "decision", Text: "Needs you: " + firstNonEmpty(b.UserAsk, b.Title)}
	case b.PendingQuestions > 0 || b.QuestionWaiting:
		return Result{Actor: Stuck, Reason: "agent_question", Who: b.ExecRole, Text: "The agent on " + quoted(b.Title) + " waits on an answer to its question"}
	case b.LaunchFailed:
		return Result{Actor: Stuck, Reason: "launch_failed", Who: b.ExecRole, Text: "Stuck: the agent could not start" + suffix(b.LaunchError)}
	case b.StopReason != "" && !b.Executing && !b.Unverified && !b.ReadyQueued:
		return Result{Actor: Stuck, Reason: "agent_stopped", Who: b.ExecRole, Since: b.ExitedAt, Text: b.StopReason}
	case b.ExitedWithoutReport:
		return Result{Actor: Stuck, Reason: "exited_without_report", Who: b.ExecRole, Since: b.ExitedAt, Text: "Stuck: the agent stopped without a report"}
	case b.Executing && b.Cutover:
		return cutoverResult(b, now)
	case b.Executing:
		since := max(b.ExecSince, b.ProgressAt)
		limit := NoProgressStuck
		if b.ExecWorking {
			limit = BusyNoProgressStuck // a long build or test run is still a turn in progress
		}
		if b.ExecActiveAt > 0 && !b.ExecWorking {
			// Between turns: a recorded wait is what it waits on; otherwise, once
			// it has sat idle past the limit, telling it what next is the
			// Commander's move (stuck only if the Commander leaves it there).
			if w := waits(b, ctx); w.Actor != "" {
				w.Bead = b.ID
				return w
			}
			if now-b.ExecActiveAt > int64(NoProgressStuck/time.Second) {
				return Result{Actor: Stuck, Reason: "idle_agent", Who: b.ExecRole, Since: b.ExecActiveAt, Text: fmt.Sprintf("Stuck: the agent on %s has been idle for %s", quoted(b.Title), ago(now-b.ExecActiveAt))}
			}
		} else if b.ExecActiveAt > 0 {
			// In a turn, its own events say whether it is working: quiet for the
			// idle limit is stuck; active but recording nothing for hours is a loop.
			if now-b.ExecActiveAt > int64(NoProgressStuck/time.Second) {
				return Result{Actor: Stuck, Reason: "no_progress", Who: b.ExecRole, Since: b.ExecActiveAt, Text: fmt.Sprintf("Stuck: the agent has done nothing for %s", ago(now-b.ExecActiveAt))}
			}
			if since > 0 && now-since > int64(LoopStuck/time.Second) {
				return Result{Actor: Stuck, Reason: "looping", Who: b.ExecRole, Since: since, Text: fmt.Sprintf("Stuck: the agent is busy but has recorded no progress for %s", ago(now-since))}
			}
		} else if since > 0 && now-since > int64(limit/time.Second) {
			return Result{Actor: Stuck, Reason: "no_progress", Who: b.ExecRole, Since: since, Text: fmt.Sprintf("Stuck: no progress for %s", ago(now-since))}
		}
		text := "Working on " + quoted(b.Title)
		if b.RunRuntime != "" && b.WantRuntime != "" && b.RunRuntime != b.WantRuntime {
			// The launch fell back (no account of the asked runtime was free), or
			// the run predates the step's runtime: either way, say it.
			text += fmt.Sprintf(" on %s, though the step asks for %s", runtimeName(b.RunRuntime), runtimeName(b.WantRuntime))
		}
		return Result{Actor: Agent, Reason: "executing", Who: b.ExecRole, Since: b.ExecSince, Text: text}
	case b.Unverified:
		if b.UnverifiedSince > 0 && now-b.UnverifiedSince > int64(UnverifiedStuck/time.Second) {
			return Result{Actor: Stuck, Reason: "unverified", Who: b.ExecRole, Since: b.UnverifiedSince, Text: "Stuck: claimed, but no agent process is running"}
		}
		return Result{Actor: Agent, Reason: "starting", Who: b.ExecRole, Since: b.UnverifiedSince, Text: "Starting an agent on " + quoted(b.Title)}
	// A step whose agent the Commander stopped holds that agent's save-and-stop
	// note, not a deliverable: re-planning is owed, never a review.
	// A finished agent step closes itself on a clean final report (cmdrd
	// accepts it); review happens only in an explicit review step.
	case b.ResultReady && !b.Stopped:
		if b.ResultClean {
			return Result{Actor: Agent, Reason: "result_accepting", Who: b.ExecRole, Since: b.ResultSince, Text: "Finished " + quoted(b.Title) + "; closing it"}
		}
		return Result{Actor: Stuck, Reason: "agent_blocked", Who: b.ExecRole, Since: b.ResultSince, Text: "The agent on " + quoted(b.Title) + " ended blocked: " + firstNonEmpty(b.ResultLine, "no clean final report")}
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
			if b.CardsKnown && !b.NeedsUser {
				// The row may only say "Needs you" when the card is on the user's list.
				return Result{Actor: Stuck, Reason: InvalidPrefix + "no-card", Since: unix(w.Since), Text: "Invalid state: " + quoted(b.UserAsk) + " waits on you but has no card"}
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
		return Result{Actor: Agent, Reason: "wait_over", Since: max(unix(due.Since), unix(due.Until)), Text: "Wait over (" + due.Text() + "): resuming " + quoted(b.Title)}
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
// Being in a turn does not mean it is doing this move (its one chat carries
// every workstream), so an owed move left that long while it is busy is stuck too.
func commanderOwed(b Bead, ctx Context, reason, what string, since int64) Result {
	if b.PlanningUsage != "" && (reason == "plan" || reason == "wait_over") {
		return Result{Actor: Stuck, Reason: "planning_usage", Text: b.PlanningUsage, Bead: b.ID}
	}
	who := firstNonEmpty(b.PlanningCommander, "commander")
	busy, idleSince := false, int64(0)
	if ctx.Commander != nil {
		busy, idleSince = ctx.Commander(who)
	}
	if busy && ctx.CommanderOn != nil && ctx.CommanderOn(who, b.ID) {
		return Result{Actor: Commander, Reason: reason, Who: who, Since: since, Text: "Commander: " + what, Active: true}
	}
	if busy {
		if since > 0 && ctx.Now.Unix()-since > int64(CommanderBusyStuck/time.Second) {
			return Result{Actor: Stuck, Reason: "commander_idle:" + reason, Who: who, Since: since, Text: fmt.Sprintf("Stuck: Commander has not got to %s in %s (busy with other work)", what, ago(ctx.Now.Unix()-since))}
		}
		return Result{Actor: Commander, Reason: reason, Who: who, Since: since, Text: "Commander to " + what}
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
		label := strings.TrimSpace(w.RefText())
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

// cutoverResult is a running agent Commander asked to save its work and stop:
// working while it does, stuck once it outlives cmdrd's stop.
func cutoverResult(b Bead, now int64) Result {
	from := runtimeName(firstNonEmpty(b.CutoverFrom, b.RunRuntime))
	doing := "Stopping the agent: it is saving its work"
	if b.CutoverTo != "" {
		doing = fmt.Sprintf("Switching to %s: %s agent saving its work", runtimeName(b.CutoverTo), from)
	}
	waited := now - b.CutoverSince
	if b.CutoverSince > 0 && waited > int64(CutoverStuck/time.Second) {
		return Result{Actor: Stuck, Reason: "cutover_stalled", Who: b.ExecRole, Since: b.CutoverSince, Text: fmt.Sprintf("Stuck: the %s agent asked to stop is still running (%s)", from, ago(waited))}
	}
	if b.CutoverSince > 0 {
		doing += " (" + ago(waited) + ")"
	}
	return Result{Actor: Agent, Reason: "cutover", Who: b.ExecRole, Since: b.CutoverSince, Text: doing}
}

// runtimeName is how a runtime reads on the board.
func runtimeName(rt string) string {
	switch rt {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	}
	return rt
}
