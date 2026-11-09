package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/holdread"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/spawn"
)

// maxClosure bounds the fresh reads one write makes; a larger closure is not judged (fail open).
const maxClosure = 60

// holdingRole is the agent a write runs as; the user (no role) is exempt from the closure check.
func holdingRole() string { return os.Getenv("COMMANDER_ROLE") }

// liveOwners answers the Graph's live question from the running agent runs. A Manager's or
// Commander's name never holds by itself: its durable form is a handoff wait.
func liveOwners(ctx context.Context) func(string) (string, bool) {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return nil // nothing to check against: every owner reads live
	}
	defer cleanup()
	running := map[string]bool{}
	projects, _ := st.ListProjects(ctx)
	for _, p := range projects {
		runs, err := st.ListAgentRunsByProject(ctx, p.ID, "running")
		if err != nil {
			continue
		}
		for _, r := range runs {
			running[spawn.CanonicalRole(r.AgentName)] = true
		}
	}
	return func(owner string) (string, bool) {
		if holding.IsManager(owner) {
			return "idle", true
		}
		if running[spawn.CanonicalRole(owner)] {
			return "working", true
		}
		return "", true
	}
}

// loadGraph reads, fresh and never from a snapshot, the beads a write to id can leave unheld
// (holdread.Load, shared with cmdrd's write door).
func loadGraph(ctx context.Context, bc beads.Client, dir, id string) (*holding.Graph, error) {
	return holdread.Load(ctx, bc, dir, id, liveOwners(ctx), maxClosure)
}

// requireHeldAfter judges, on fresh reads (invariant I4), a write that would leave a bead of id's
// closure with no holder. An agent is refused, with fix naming the flags that hold the work in the
// same command. The user (no role: the CLI or the app's /api/cmd) is never refused: the write goes
// through and owes the Manager's "Manager to start" receipt on what it leaves unheld, which the
// caller writes right after its write (custodyOwed.settle), or skips with the user's recorded
// --leave-unheld <reason>. A write whose closure cannot be read passes (the door fails open on a
// read error, as the others do).
func requireHeldAfter(ctx context.Context, bc beads.Client, dir, id, verb, fix string, edits map[string]holding.Edit) (custodyOwed, error) {
	return requireHeldAfterSkipping(ctx, bc, dir, id, verb, fix, edits, false)
}

// requireHeldAfterSkipping is requireHeldAfter that can leave id's ancestors out: closing a
// stage's last step leaves the stage to be closed next (the plan's stage-done prompt), not unheld work.
func requireHeldAfterSkipping(ctx context.Context, bc beads.Client, dir, id, verb, fix string, edits map[string]holding.Edit, skipAncestors bool) (custodyOwed, error) {
	g, err := loadGraph(ctx, bc, dir, id)
	if err != nil {
		return custodyOwed{}, nil
	}
	left := holding.NewlyUnheld(g, id, edits)
	if skipAncestors {
		up := map[string]bool{}
		for cur, n := g.Beads[id].Parent, 0; cur != "" && n < 32; n++ {
			up[cur] = true
			cur = g.Beads[cur].Parent
		}
		left = slices.DeleteFunc(left, func(v holding.Violation) bool { return up[v.ID] })
	}
	if len(left) == 0 {
		return custodyOwed{}, nil
	}
	var names, ids []string
	for _, v := range left {
		names = append(names, fmt.Sprintf("%s %q (%s)", v.ID, v.Title, v.Why))
		ids = append(ids, v.ID)
	}
	if ok, err := leaveUnheld(ctx, bc, dir, id, verb, names); ok || err != nil {
		return custodyOwed{}, err
	}
	if holdingRole() == "" {
		return custodyOwed{bc: bc, dir: dir, ids: ids, verb: verb}, nil
	}
	return custodyOwed{}, fmt.Errorf("refused: %s would leave %s with no holder. Hold it in the same command: %s. Nothing was written", verb, strings.Join(names, ", "), fix)
}

// verbIntent is the door intent a verb puts on its decisive write (beads.WithIntent): cmdrd
// judges the result on fresh reads with the one predicate (an agent's unheld result is refused;
// the user's gets the Manager's receipt, or the recorded --leave-unheld), orders holder-adding
// ops first and lays the write on the board at once. The verb's own check ran first, so the
// door agrees unless the beads moved between them (the race it closes).
func verbIntent(bead, verb string) beads.DoorIntent {
	in := beads.DoorIntent{Bead: bead, Verb: verb}
	if leaveUnheldRecorded {
		in.LeaveUnheld = leaveUnheldReason
	}
	return in
}

// custodyOwed is the receipt the user's write owes the steps it leaves with no holder.
type custodyOwed struct {
	bc   beads.Client
	dir  string
	ids  []string
	verb string
}

// settle writes the owed "Manager to start" receipts: right before a write that only ends a
// blocker (closing names it: a gate pass, a dep rm; make before break) or once a write that edits
// the step itself has landed. holdForManager reads each step fresh, so one the write left held, or
// behind another open blocker, gets none. A failure is reported with the command that holds the step.
func (c custodyOwed) settle(ctx context.Context, closing string) ([]string, error) {
	if len(c.ids) == 0 {
		return nil, nil
	}
	held, err := holdForManager(ctx, c.bc, c.dir, c.ids, closing, time.Now())
	if err != nil {
		return held, fmt.Errorf("%s: the Manager's %q hold on %s was not written (%v): hold it with cmdr internal-set-objective --bead <id> --waiting until:<time>:<what happens then>", c.verb, beads.ManagerStartWait, strings.Join(c.ids, ", "), err)
	}
	return held, nil
}

// leaveUnheldReason is the user's --leave-unheld <reason> (stripped from any verb's arguments by run):
// the user's explicit way to leave work with no holder (no Manager receipt), recorded on the bead and in the log.
var leaveUnheldReason string

// leaveUnheldRecorded is set once this command recorded its --leave-unheld.
var leaveUnheldRecorded bool

// stripLeaveUnheld takes --leave-unheld <reason> (or --leave-unheld=<reason>) out of a verb's arguments.
func stripLeaveUnheld(args []string) ([]string, string) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "internal-") && args[0] != "workflow" {
		return args, ""
	}
	out := make([]string, 0, len(args))
	reason := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "--leave-unheld="); ok {
			reason = v
			continue
		}
		if a == "--leave-unheld" && i+1 < len(args) {
			reason = args[i+1]
			i++
			continue
		}
		out = append(out, a)
	}
	return out, strings.TrimSpace(reason)
}

// leaveUnheld lets the user's write through when it carries --leave-unheld, and records it: a
// comment on the bead and a line in $COMMANDER_HOME/logs/leave-unheld.jsonl. An agent with the flag is
// refused (it names the next holder instead). ok is false when the flag is absent.
func leaveUnheld(ctx context.Context, bc beads.Client, dir, id, verb string, names []string) (bool, error) {
	if leaveUnheldReason == "" {
		return false, nil
	}
	if leaveUnheldRecorded {
		return true, nil // one command, one record (a close judges twice)
	}
	if role := holdingRole(); role != "" {
		return false, fmt.Errorf("refused: --leave-unheld is the user's, not %s's: name the next holder instead (%s)", role, holdFlags)
	}
	if len([]rune(leaveUnheldReason)) < 10 {
		return false, fmt.Errorf("refused: --leave-unheld needs a reason of 10 characters or more (it is recorded on %s)", id)
	}
	note := fmt.Sprintf("left unheld by the user at %d: %s leaves %s with no holder. Reason: %s", time.Now().Unix(), verb, strings.Join(names, ", "), leaveUnheldReason)
	if err := bc.AddComment(ctx, dir, id, note); err != nil {
		return false, fmt.Errorf("--leave-unheld could not be recorded on %s, so nothing was written: %w", id, err)
	}
	leaveUnheldRecorded = true
	appendJSONL(filepath.Join(appCommanderHome(), "logs", "leave-unheld.jsonl"), map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339), "bead": id, "verb": verb, "left": names, "reason": leaveUnheldReason,
	})
	return true, nil
}

// appendJSONL appends one JSON line; a failure is not the write's (the bead comment is the record).
func appendJSONL(path string, v any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(v)
}

// depRmFix and moveFix name what to run before a verb that takes no holder flags (m8): the hold
// is written first with the verbs that do.
const depRmFix = "first give it a holder with cmdr internal-set-objective --bead <id> --waiting ci:#N|review:<who>|until:<time>:<label> or --owner <running operator-...>, or a card (--state needs-user --decision), or another open blocker (cmdr internal-dep add), then remove the edge"

const moveFix = "first give it a holder with cmdr internal-set-objective --bead <id> --waiting ci:#N|review:<who>|until:<time>:<label> or --owner <running operator-...>, or a card (--state needs-user --decision), then move it"

const holdFlags = "a running Operator (--owner operator-...), a recorded wait (--waiting ci:#N|review:<who>|until:<time>:<label>), an open blocker (cmdr internal-dep add), an open child, or the user's card (--state needs-user --decision); a Manager's name is not a holder"

// holdWithManager makes the bead's Manager its holder through a handoff wait (and the Manager owner
// label): kind is beads.ManagerDispatchWait for released work, beads.ManagerRecoverWait for work
// whose Operator could not run. Written before the write that drops the old holder (make before break).
func holdWithManager(ctx context.Context, bc beads.Client, dir, bead, manager, kind string) error {
	if manager != "" {
		if err := bc.SetLabel(ctx, dir, bead, beads.OwnerLabelPrefix+beads.OwnerLabelName(manager), true); err != nil {
			return err
		}
	}
	now := time.Now()
	within := 2 * time.Minute
	if kind != beads.ManagerDispatchWait {
		within = beads.ManagerCloseWithin
	}
	w := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(within), Ref: kind}
	return bc.SetLabel(ctx, dir, bead, w.Label(), true)
}

// holdReleased hands every bead of id's closure that edits would leave with no holder to its
// Manager (holdWithManager) BEFORE the write runs, for writes that give work back to its Manager by
// design (a proposal overruled, a spawn that failed): they are not refused, their result is held.
// It returns the beads it held. A closure it cannot read is left as is (fail open).
func holdReleased(ctx context.Context, bc beads.Client, dir, id string, edits map[string]holding.Edit, manager, kind string) []string {
	return holdReleasedLive(ctx, bc, dir, id, edits, manager, kind, liveOwners(ctx))
}

// holdReleasedLive is holdReleased judged with live as the Graph's live question. A bead that
// already carries a kind wait (an earlier run of the same write held it) and needs it is
// reported as held again, without a second wait. Never nil: an empty list when nothing is held.
func holdReleasedLive(ctx context.Context, bc beads.Client, dir, id string, edits map[string]holding.Edit, manager, kind string, live func(string) (string, bool)) []string {
	held := []string{}
	g, err := holdread.Load(ctx, bc, dir, id, live, maxClosure)
	if err != nil {
		return held
	}
	// Judge as if this write's own earlier receipts were not there, so a retry still names them.
	judged := map[string]holding.Edit{}
	for k, e := range edits {
		judged[k] = e
	}
	already := map[string]bool{}
	for _, x := range g.Closure(id) {
		n := handoffWaits(g.Beads[x].Labels, kind)
		if n == 0 {
			continue
		}
		already[x] = true
		prev := judged[x]
		judged[x] = func(h *holding.Bead) {
			if prev != nil {
				prev(h)
			}
			h.Waits -= n
		}
	}
	for _, v := range holding.NewlyUnheld(g, id, judged) {
		if already[v.ID] || holdWithManager(ctx, bc, dir, v.ID, manager, kind) == nil {
			held = append(held, v.ID)
		}
	}
	return held
}

// handoffWaits counts the labels' until: waits whose label is kind (a Manager handoff receipt).
func handoffWaits(labels []string, kind string) int {
	n := 0
	for _, w := range beads.WaitsOf(labels) {
		if w.Kind == beads.WaitUntil && w.Ref == kind {
			n++
		}
	}
	return n
}

// boardManagerTurns answers the live question as live does, except that a Manager the board shows
// mid-turn (cmdrd's snapshot agents) is working: its turn holds what it owns. Only for a write
// another role makes (an overrule is Commander's or the user's): a Manager's own write always runs
// mid-turn, so it never asks this. A nil board changes nothing.
func boardManagerTurns(snap *livedata.Snapshot, live func(string) (string, bool)) func(string) (string, bool) {
	if snap == nil {
		return live
	}
	return func(owner string) (string, bool) {
		if holding.IsManager(owner) && snap.Agents[owner].State == "working" {
			return "working", true
		}
		if live == nil {
			return "", false
		}
		return live(owner)
	}
}
