package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// The checks at the door: a state write is refused when the state it leaves
// would read wrong on the user's board. Each refusal says what to write instead.

// checkBlockedNamesWait refuses blocked with nothing named to wait on: the row
// would read Stalled or State missing with nothing able to move it.
func checkBlockedNamesWait(ctx context.Context, bc beads.Client, dir, beadID string, newWaits []beads.Wait) error {
	if len(newWaits) > 0 {
		return nil
	}
	b, showErr := bc.Show(ctx, dir, beadID)
	if showErr == nil && b != nil && len(beads.WaitsOf(b.Labels)) > 0 {
		return nil
	}
	blocker, err := openBlocker(ctx, bc, dir, beadID)
	if blocker != "" {
		return nil
	}
	if err == nil && showErr != nil {
		err = showErr
	}
	if err != nil {
		return readBusyErr(beadID, err)
	}
	return fmt.Errorf("--state blocked on %s names nothing it waits on, so the user's board reads Stalled with nothing able to move it. Add --waiting: ci:#N, review:<login> (with --review <PR>), operator:<running role>, or until:<time>:<what happens then, in plain words>. If nothing holds it yet, use --state not-started", beadID)
}

// checkReviewWaitsHavePR refuses a review wait with no PR to watch: cmdrd can
// never see the review, so the wait would never clear.
func checkReviewWaitsHavePR(ctx context.Context, bc beads.Client, dir, beadID, reviewFlag string, waits []beads.Wait) error {
	var reviewers []string
	for _, w := range waits {
		if w.Kind == beads.WaitReview {
			reviewers = append(reviewers, w.Ref)
		}
	}
	if len(reviewers) == 0 || (reviewFlag != "" && reviewFlag != "none") {
		return nil
	}
	// --review none takes the PR away in this same command, whatever the bead carries now.
	if reviewFlag == "" {
		if b, err := bc.Show(ctx, dir, beadID); err == nil && b != nil {
			for _, l := range b.Labels {
				if strings.HasPrefix(l, "pr:") && l != "pr:" {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("--waiting review:%s on %s names no PR, so cmdrd can never see the review and the wait would never clear. Pass --review <PR number> with it (one step per PR)", strings.Join(reviewers, ","), beadID)
}

// untilAlternatives names the typed waits a slug label usually stands in for.
const untilAlternatives = "If it really waits on something else, wait on that: review:<login> (with --review <PR>), ci:#N, main-ci:<sha> or prod-tag:<sha> for a release, operator:<running role>, or --state needs-user with --decision when it is the user's call"

// checkUntilLabelsReadable refuses a timed wait whose label is not plain words:
// the user reads it as "Waiting for <label>", and a slug is usually a stand-in
// for a wait cmdrd could watch.
func checkUntilLabelsReadable(waits []beads.Wait) error {
	for _, w := range waits {
		if w.Kind != beads.WaitUntil {
			continue
		}
		if !beads.ReadableUntilLabel(w.Ref) {
			return fmt.Errorf("--waiting until:...:%q: the label is what the user reads (\"Waiting for <label>\"), so write it in plain words saying what happens then, e.g. until:<time>:the 6 AM quiet window opens. %s", strings.TrimSpace(w.Ref), untilAlternatives)
		}
		if beads.ReviewTimerLabel(w.Ref) {
			return fmt.Errorf("--waiting until:...:%q: a timer standing in for a PR review leaves the review unseen until it fires. cmdrd watches reviews itself, in any repo, and wakes you when one lands: --waiting review:<login> with --review <N> (this project's repo) or --review <owner>/<name>#N (another repo)", strings.TrimSpace(w.Ref))
		}
	}
	return nil
}

// checkUntilNotOnUserDecision refuses a timer on the user's decision (their
// approval gate, or a step they own): the clock can never make the decision,
// so the row would wake on time and still be waiting on them.
func checkUntilNotOnUserDecision(b *beads.Bead, finalOwner string, waits []beads.Wait) error {
	hasUntil := false
	for _, w := range waits {
		hasUntil = hasUntil || w.Kind == beads.WaitUntil
	}
	if !hasUntil || b == nil {
		return nil
	}
	what := ""
	switch {
	case isUserGate(b.Labels):
		what = "the user's approval gate"
	case finalOwner == "user":
		what = "owned by the user"
	default:
		return nil
	}
	return fmt.Errorf("--waiting until:... on %s: it is %s, and a timer cannot make the user's decision, so the row would read waiting with nothing able to clear it. Write --state needs-user with --decision '<card>' instead (that is the wait)", b.ID, what)
}

// checkUserOwnsOnlyAsks refuses owner user on a plain step in any state but
// needs-user: the user's part of the board is cards and gates, so the row
// would read as the user's with nothing asking them anything.
func checkUserOwnsOnlyAsks(b *beads.Bead, finalOwner, finalState string) error {
	if finalOwner != "user" || finalState == beads.StateNeedsUser || finalState == beads.StateDone || (b != nil && hasLabel(b.Labels, beads.GateLabel)) {
		return nil
	}
	id := ""
	if b != nil {
		id = " on " + b.ID
	}
	return fmt.Errorf("--owner user%s with state %s: a step is the user's only while it asks them something, so the row reads as theirs with nothing to answer. Use --state needs-user with --decision if they must act; otherwise give it an agent owner or --owner none", id, finalState)
}

func isUserGate(labels []string) bool {
	if !hasLabel(labels, beads.GateLabel) {
		return false
	}
	for _, l := range labels {
		if a, ok := strings.CutPrefix(l, beads.ApproverLabelPrefix); ok && ownercompat.Normalize(a) == "user" {
			return true
		}
	}
	return false
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// checkGateLinksAcyclic refuses a new gate placed both after and before the
// same work: --after a and --before b make b wait on the gate and the gate on
// a, so if a already waits on b (or is b) nothing in the loop can ever start.
func checkGateLinksAcyclic(ctx context.Context, bc beads.Client, dir string, after, before []string) error {
	for _, a := range after {
		for _, b := range before {
			if a == b || dependsOn(ctx, bc, dir, a, b) {
				return fmt.Errorf("--after %s with --before %s: %s already waits on %s, so the gate would sit both before and after it (a cycle) and nothing could ever clear it. An approval goes after the work it approves: drop %s from --before", a, b, a, b, b)
			}
		}
	}
	return nil
}

// dependsOn reports whether from waits on target through blocks edges.
func dependsOn(ctx context.Context, bc beads.Client, dir, from, target string) bool {
	seen := map[string]bool{}
	queue := []string{from}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		deps, err := bc.Deps(ctx, dir, id)
		if err != nil {
			return true // unreadable graph cannot establish that a new edge is safe
		}
		for _, d := range deps {
			if d.Type != "blocks" || d.DependsOnID == "" {
				continue
			}
			if d.DependsOnID == target {
				return true
			}
			queue = append(queue, d.DependsOnID)
		}
	}
	return false
}

// closeDoorRole is the role checkCloseLeavesWorkHeld judges. An Operator closing
// its own task or something under it (a knowledge Operator's report, an
// operational step) is finishing its work, and its Manager hears when it ends,
// so it passes like the user's own close. Any other bead is judged by its role.
func closeDoorRole(ctx context.Context, bc beads.Client, dir, beadID string) string {
	role := os.Getenv("COMMANDER_ROLE")
	if !strings.HasPrefix(role, "operator-") {
		return role
	}
	fresh := freshReader(bc)
	for _, id := range beads.Lineage(ctx, bc, dir, nil, beadID) {
		b, err := fresh.Show(ctx, dir, id)
		if err != nil {
			return "" // unreadable: do not block the close
		}
		if b != nil && spawn.CanonicalRole(labelWithPrefix(*b, "owner:")) == spawn.CanonicalRole(role) {
			return ""
		}
	}
	return role
}

// checkOwnerLive refuses in-progress owned by an Operator that is not running:
// the row would read working with nobody working.
func checkOwnerLive(ctx context.Context, owner string) error {
	if owner == "user" {
		// The user's work is a needs-user step with a card; "in progress, owned
		// by the user" has nobody working it and reads State missing.
		return fmt.Errorf("--state in-progress with --owner user: the board has no way to show the user working, so the row reads State missing. If the user has to act, use --state needs-user with --decision; if an agent does the work, spawn its Operator and make it the owner")
	}
	if !strings.HasPrefix(owner, "operator-") {
		return nil
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		return nil // no store to check against: do not block the write
	}
	defer cleanup()
	projects, _ := st.ListProjects(ctx)
	for _, p := range projects {
		runs, err := st.ListAgentRunsByProject(ctx, p.ID, "running")
		if err != nil {
			continue
		}
		for _, r := range runs {
			if spawn.CanonicalRole(r.AgentName) == spawn.CanonicalRole(owner) {
				return nil
			}
		}
	}
	return fmt.Errorf("--state in-progress with --owner %s: that Operator is not running, so the row would read working with nobody working. Spawn it first (cmdr internal-spawn-operator), then set the step in progress", owner)
}

// checkAskReady refuses needs-user while the step's prerequisites are still
// open: the card would ask the user before there is anything to decide.
func checkAskReady(ctx context.Context, bc beads.Client, dir, beadID string) error {
	blocker, err := openBlocker(ctx, bc, dir, beadID)
	if err != nil {
		return readBusyErr(beadID, err)
	}
	if blocker != "" {
		return fmt.Errorf("--state needs-user on %s: it still waits on %s, which is open, so the card would ask the user before there is anything to decide. Ask when %s is done", beadID, blocker, blocker)
	}
	return nil
}

// readBusyErr is the retryable refusal for a read that failed: never read as
// "open" or "no blocker", the write is simply not decided yet.
func readBusyErr(beadID string, err error) error {
	return fmt.Errorf("could not read %s's blockers (busy): %v. Nothing was written; retry in a minute", beadID, err)
}

// freshReader is the client under any cache, so the status of a blocker is
// read from br itself, never from a cached or exported view.
func freshReader(bc beads.Client) beads.Client {
	if c, ok := bc.(*beads.CachedClient); ok && c.Client != nil {
		return c.Client
	}
	return bc
}

// openBlocker is the first open step beadID waits on by a blocks edge, or "".
// A failed read of the edges or of a blocker's own status is an error, never
// "no blocker" and never "open".
func openBlocker(ctx context.Context, bc beads.Client, dir, beadID string) (string, error) {
	fresh := freshReader(bc)
	deps, err := fresh.Deps(ctx, dir, beadID)
	if err != nil {
		return "", err
	}
	for _, d := range deps {
		if d.Type != "blocks" || d.DependsOnID == "" || d.DependsOnID == closingBead {
			continue
		}
		b, err := fresh.Show(ctx, dir, d.DependsOnID)
		if err != nil {
			return "", err
		}
		if b != nil && b.Status != "closed" && b.Status != "tombstone" && b.Status != "deferred" {
			return d.DependsOnID, nil
		}
	}
	return "", nil
}

// closingBead is the bead internal-close-objective is about to close: the hold
// it writes first, on the work behind it, reads it as closed.
var closingBead string

// closeSnapshotFn reads cmdrd's snapshot for the close door; tests replace it.
var closeSnapshotFn = func() (*livedata.Snapshot, error) {
	body, err := loadSnapshot(appCommanderHome(), "")
	if err != nil {
		return nil, err
	}
	var s livedata.Snapshot
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	if s.Degraded != nil {
		return nil, fmt.Errorf("snapshot degraded: %s", s.Degraded.Reason)
	}
	return &s, nil
}

// thenHold is the next holder a close names (--then-*): what it writes on the
// work the close releases, before the close, so the board never sees it unheld.
type thenHold struct {
	step     string // "" = every step the close leaves unheld
	waiting  []string
	owner    string
	decision string
}

func (h thenHold) set() bool { return len(h.waiting) > 0 || h.owner != "" || h.decision != "" }

// edit is the hold as the snapshot would show it.
func (h thenHold) edit(st *livedata.SnapStep) {
	if len(h.waiting) > 0 {
		st.Waiting = strings.Join(h.waiting, ", ")
	}
	if h.owner != "" {
		st.Owner, st.OwnerState, st.State = h.owner, "", beads.StateInProgress
	}
	if h.decision != "" {
		st.State = beads.StateNeedsUser
	}
}

// args is the internal-set-objective write of the hold on step.
func (h thenHold) args(project, step string) []string {
	a := []string{"--project", project, "--bead", step}
	for _, w := range h.waiting {
		a = append(a, "--waiting", w)
	}
	if h.owner != "" {
		a = append(a, "--owner", h.owner, "--state", beads.StateInProgress)
	}
	if h.decision != "" {
		a = append(a, "--state", beads.StateNeedsUser, "--decision", h.decision)
	}
	return a
}

const thenFlags = "--then-waiting until:<RFC3339>:<what happens then, in plain words> (or ci:#N, review:<login>, operator:<running role>), --then-owner <running Operator>, or --then-decision '<card json>' when it is the user's call; --then-step <id> picks the step (default: every step left unheld)"

// checkCloseLeavesWorkHeld refuses a close that leaves open work nothing holds
// (no live agent, no recorded wait, not the user's): the user's board would read
// State missing until another write names the holder. The predicate is the
// board's own (livedata.UnheldAfter). It returns the steps the hold goes on.
// The user's own close (no role) and the workstream's Manager, whose turn is
// running and holds the work until it ends, are not refused: the steps they
// leave unheld come back as custody, which the close writes as the Manager's
// "Manager to start" receipt (holdForManager), so the board never reads State
// missing once the turn ends.
func checkCloseLeavesWorkHeld(snap *livedata.Snapshot, beadID, role string, hold thenHold) (targets []string, custody []livedata.Violation, err error) {
	if snap == nil {
		if hold.set() && hold.step == "" {
			return nil, nil, fmt.Errorf("cmdrd's board is not readable, so the steps this close releases are unknown: name the one to hold with --then-step <id>")
		}
		if hold.step != "" {
			return []string{hold.step}, nil, nil
		}
		return nil, nil, nil
	}
	closeStep := func(st *livedata.SnapStep) { st.Status, st.State = "closed", beads.StateDone }
	holes := livedata.UnheldAfter(*snap, map[string]func(*livedata.SnapStep){beadID: closeStep})
	targets = []string{hold.step}
	if hold.step == "" {
		targets = nil
		for _, v := range holes {
			targets = append(targets, v.BeadID)
		}
	}
	if hold.set() && len(targets) == 0 {
		return nil, nil, fmt.Errorf("closing %s leaves nothing unheld, so --then-* has no step to go on: name it with --then-step <id>", beadID)
	}
	left := holes
	if hold.set() {
		edits := map[string]func(*livedata.SnapStep){beadID: closeStep}
		for _, t := range targets {
			edits[t] = hold.edit
		}
		left = livedata.UnheldAfter(*snap, edits)
	}
	var unheld []string
	for _, v := range left {
		if role == "" || spawn.CanonicalRole(role) == spawn.CanonicalRole(v.ManagerRole) {
			custody = append(custody, v)
			continue
		}
		unheld = append(unheld, fmt.Sprintf("%s %q", v.BeadID, v.Title))
	}
	if len(unheld) == 0 {
		if !hold.set() {
			return nil, custody, nil
		}
		return targets, custody, nil
	}
	if hold.set() {
		return nil, nil, fmt.Errorf("closing %s with that hold still leaves %s with nothing holding it, so the user's board reads State missing. Hold it too: %s. Nothing was closed", beadID, strings.Join(unheld, ", "), thenFlags)
	}
	return nil, nil, fmt.Errorf("closing %s leaves %s open with nothing holding it (no running agent, no recorded wait, no user decision), so the user's board reads State missing until someone writes its state. Name the next holder in the same command: %s. Nothing was closed", beadID, strings.Join(unheld, ", "), thenFlags)
}

// boardSnapshot is cmdrd's board for the doors that read it; nil when it is
// not readable (each door says what it does then).
func boardSnapshot() *livedata.Snapshot {
	s, err := closeSnapshotFn()
	if err != nil {
		return nil
	}
	return s
}

// coveringApproval is an approved user gate whose approval covers step on the
// board (livedata.CoveringApprovals: what approving it started, through the
// next gate). "" when none, or when the board is not readable: the step then
// needs its own approval, as before.
func coveringApproval(step string) string {
	s := boardSnapshot()
	if s == nil {
		return ""
	}
	if gs := livedata.CoveringApprovals(*s, step); len(gs) > 0 {
		return gs[0]
	}
	return ""
}

// externalWait is who a bead's recorded waits name outside Commander and the
// user (a reviewer, a set time), "" when none is still pending. A Manager
// custody receipt (Manager to close, dispatch or start: beads.IsManagerHandoff)
// is the Manager's own next move, not a wait the user asked for, and a time
// already passed holds nothing.
func externalWait(labels []string, now time.Time) string {
	var who []string
	for _, w := range beads.WaitsOf(labels) {
		switch {
		case w.Kind == beads.WaitReview:
			who = append(who, "review by "+w.Ref)
		case w.Kind == beads.WaitUntil && !beads.IsManagerHandoff(w) && w.Until.After(now):
			what := w.Ref
			if what == "" {
				what = "a set time"
			}
			who = append(who, what+" ("+w.Until.UTC().Format(time.RFC3339)+")")
		}
	}
	return strings.Join(who, ", ")
}

// prRefs are the PR numbers a card names in its words, plus the bead's pr:
// labels: the same function the board uses to hold a gate's card.
func prRefs(d *store.Decision, labels []string) map[string]bool {
	return livedata.PRRefs(d, labels)
}

// approvalAsk: the card asks the user to approve (an approves option, or on a
// card without the flag an approving label).
func approvalAsk(d *store.Decision) bool {
	if d == nil {
		return false
	}
	flagged := false
	for _, o := range d.Options {
		flagged = flagged || o.Approves
	}
	if flagged {
		return true
	}
	for _, o := range d.Options {
		if legacyApprovingLabel(o.Label) {
			return true
		}
	}
	return false
}

const waitHeldTail = "A new card about it is refused until that wait clears (cmdrd clears it and mails you when it does); keep waiting"

// checkAskNotSettled refuses a new needs-user card on something the user
// already answered or said to wait on: the same step, a user gate over it
// (livedata.UserGatesOver, the board's own unlocks), or the same PR on
// another bead of the project. reopen is --status open in the same command:
// the explicit "the plan changed, ask again". The user's own write (no role)
// is not refused. A board that is not readable skips the gate rule only.
func checkAskNotSettled(ctx context.Context, bc beads.Client, dir, project, beadID string, d *store.Decision, reopen bool, differentAsk string, escaped *string) error {
	if os.Getenv("COMMANDER_ROLE") == "" {
		return nil
	}
	now := time.Now()
	fresh := freshReader(bc)
	self, err := fresh.Show(ctx, dir, beadID)
	if err != nil {
		return fmt.Errorf("could not read %s (busy): %v. Nothing was written; retry in a minute", beadID, err)
	}
	var labels []string
	if self != nil {
		labels = self.Labels
	}
	if who := externalWait(labels, now); who != "" {
		return fmt.Errorf("--state needs-user on %s: this is waiting on %s; the user already said wait. %s", beadID, who, waitHeldTail)
	}
	// An open gate still answered was sent back with Change: asking again with
	// the reworked write-up is that loop. Approve closes a gate, Drop parks it.
	reworkedGate := self != nil && isGateBead(*self) && !isClosedBead(*self)
	if slices.Contains(labels, beads.DecisionAnsweredLabel) && !reopen && !reworkedGate {
		return fmt.Errorf("--state needs-user on %s: the user already answered its card, so this asks again what was decided. Act on the answer, or update the existing card; if the plan really changed, reopen it explicitly in one command: --status open --state needs-user --decision '<card>'", beadID)
	}
	snap := boardSnapshot()
	if snap != nil {
		for _, g := range livedata.UserGatesOver(*snap, beadID) {
			if g.Gate == closingBead {
				continue // being closed by this command: the close's own door judges it
			}
			if gb, err := fresh.Show(ctx, dir, g.Gate); err == nil && gb != nil {
				if who := externalWait(gb.Labels, now); who != "" {
					return fmt.Errorf("--state needs-user on %s: its gate %s is waiting on %s; the user already said wait. %s", beadID, g.Gate, who, waitHeldTail)
				}
			}
			if g.Approved && approvalAsk(d) {
				return fmt.Errorf("--state needs-user on %s: the user already approved %s, and that approval covers %s (it carries over to everything the gate starts, through the next gate). Do not ask again: start the work. If the plan changed, reopen the gate explicitly: internal-set-objective --bead %s --status open --state needs-user --decision '<card>'", beadID, g.Gate, beadID, g.Gate)
			}
			if !g.Approved {
				return fmt.Errorf("--state needs-user on %s: it sits behind the user's gate %s, which is still open, so that gate's card is the decision. Update that card (internal-set-objective --bead %s --decision '<card>') instead of raising another", beadID, g.Gate, g.Gate)
			}
		}
	}
	prs := prRefs(d, labels)
	if len(prs) == 0 {
		return nil
	}
	all, err := fresh.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return fmt.Errorf("could not read the project's beads (busy): %v. Nothing was written; retry in a minute", err)
	}
	// A card on a gate may sit under an ancestor (track or step) that holds
	// the same PR: a settled ancestor card is no duplicate, an open one must
	// move down to the gate.
	var ancestors []string
	if self != nil && isGateBead(*self) {
		ancestors = beads.Lineage(ctx, fresh, dir, nil, beadID)
		if len(ancestors) > 0 {
			ancestors = ancestors[1:]
		}
	}
	for _, b := range all {
		if b.ID == beadID || isClosedBead(b) || b.Status == "tombstone" {
			continue
		}
		pr := ""
		for n := range prRefs(nil, b.Labels) {
			if prs[n] {
				pr = n
			}
		}
		if pr == "" {
			continue
		}
		if who := externalWait(b.Labels, now); who != "" {
			return fmt.Errorf("--state needs-user on %s: PR #%s is waiting on %s (%s); the user already said wait. %s", beadID, pr, who, b.ID, waitHeldTail)
		}
		state, _ := beads.StepState(b.Status, b.Labels)
		if slices.Contains(ancestors, b.ID) {
			if state == beads.StateNeedsUser && !slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
				if differentAsk == "" {
					return movedCardErr(project, beadID, b.ID, pr)
				}
				if escaped != nil {
					*escaped = b.ID
				}
			}
			continue // the ancestor's decision is settled: the gate may ask its own
		}
		if state == beads.StateNeedsUser || slices.Contains(b.Labels, beads.DecisionAnsweredLabel) {
			return fmt.Errorf("--state needs-user on %s: %s already has a decision about PR #%s (open, answered or parked), so this card would ask it twice. Update that card (internal-set-objective --bead %s --decision '<card>') instead of raising another", beadID, b.ID, pr, b.ID)
		}
	}
	if snap != nil {
		for _, n := range snap.NeedsYou {
			if n.Project != project || n.BeadID == beadID {
				continue
			}
			for pr := range prRefs(n.Decision, nil) {
				if prs[pr] {
					if slices.Contains(ancestors, n.BeadID) {
						// The snapshot can lag a write: the ancestor's bead says
						// whether its card is still open.
						if ancestorCardSettled(ctx, fresh, dir, n.BeadID) {
							continue
						}
						if differentAsk != "" {
							if escaped != nil {
								*escaped = n.BeadID
							}
							continue
						}
						return movedCardErr(project, beadID, n.BeadID, pr)
					}
					return fmt.Errorf("--state needs-user on %s: the open card on %s already asks about PR #%s, so this card would ask it twice. Update that card (internal-set-objective --bead %s --decision '<card>') instead of raising another", beadID, n.BeadID, pr, n.BeadID)
				}
			}
		}
	}
	return nil
}

// ancestorCardSettled: the ancestor's bead no longer holds an open card
// (answered, closed, parked or no longer needs-user), whatever a snapshot says.
func ancestorCardSettled(ctx context.Context, fresh beads.Client, dir, id string) bool {
	b, err := fresh.Show(ctx, dir, id)
	if err != nil || b == nil {
		return false
	}
	state, _ := beads.StepState(b.Status, b.Labels)
	return isClosedBead(*b) || b.Status == beads.StatusDeferred || state != beads.StateNeedsUser || slices.Contains(b.Labels, beads.DecisionAnsweredLabel)
}

// movedCardErr refuses a gate's card while an ancestor holds the open card
// for the same PR: the card belongs on the gate, so say how to move it (one
// verb), or how to say it is a different question.
func movedCardErr(project, gate, ancestor, pr string) error {
	return fmt.Errorf("--state needs-user on %s: the ancestor %s already has an open card about PR #%s; if it is the same question it belongs on this gate (the diagram would read the gate as not yet asked): cmdr internal-move-card --project %s --from %s --to %s. If it is a different question, say so: add --not-the-same-ask followed by a quoted sentence saying what differs, to this command. Nothing was written", gate, ancestor, pr, project, ancestor, gate)
}

// laneFlagValue is the lane label value --lane writes ("" for no flag, "none"
// to clear); anything that is not a lane is refused before any write.
func laneFlagValue(v string) (string, error) {
	if v == "" || v == "none" {
		return v, nil
	}
	lane, who, err := beads.ParseLane(v)
	if err != nil {
		return "", fmt.Errorf("--lane %q is not a lane, so the diagram would ignore it. Use --lane you, agents, elsewhere or elsewhere:<who in plain words> (e.g. elsewhere:the finance team), or --lane none", v)
	}
	return strings.TrimPrefix(beads.LaneLabel(lane, who), beads.LaneLabelPrefix), nil
}

// checkLaneNotOnGate refuses a lane on a gate: a gate's lane is who approves
// it, so a label could only contradict its approvers.
func checkLaneNotOnGate(ctx context.Context, bc beads.Client, dir, beadID string) error {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return readBusyErr(beadID, err)
	}
	if b != nil && hasLabel(b.Labels, beads.GateLabel) {
		return fmt.Errorf("--lane on %s: it is a gate, and a gate's lane is who approves it (approver:<who> labels), so the label would contradict them. Change who approves it instead; nothing was written", beadID)
	}
	return nil
}

// checkGreenlightIsUsers refuses a gate titled as a greenlight that the user
// does not approve: a greenlight is the user's approval by name, so the
// diagram would put the user's word on someone else's line.
func checkGreenlightIsUsers(title string, approvers []string) error {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "greenlight") {
		return nil
	}
	for _, a := range approvers {
		if ownercompat.Normalize(a) == "user" {
			return nil
		}
	}
	return fmt.Errorf("%q is titled as a greenlight, which is the user's approval, but --approver %s does not include user. Add user to --approver, or title it as what will be true, in the past tense (\"PR approved\")", title, strings.Join(approvers, ","))
}

// checkGateHasCriteria refuses a new gate that does not say what counts as
// passed: the diagram shows it on the gate, and an agent cannot check a gate
// against nothing.
func checkGateHasCriteria(criteria string) error {
	if strings.TrimSpace(criteria) == "" {
		return fmt.Errorf("a gate needs --criteria: what counts as passed, in one sentence (\"Both reviewers approve the PR on GitHub\"); the diagram shows it on the gate")
	}
	return nil
}

// checkCardDrawsNoWorkflow refuses a decision card that draws its own
// workflow: the app draws the workstream's diagram from beads under every
// card, so a hand-written one would only drift from it.
func checkCardDrawsNoWorkflow(d *store.Decision) error {
	if d == nil || !strings.Contains(d.Context, "```cmdr-graph") {
		return nil
	}
	return fmt.Errorf("--decision: the card's context holds a ```cmdr-graph block, but the app now draws the workflow under every card from beads (cmdr board --diagram <bead>), so a hand-drawn one would drift. Drop the block and its Execution workflow section; shape the beads instead and run cmdr graph check <workstream or track> before asking")
}

// checkReached refuses what may only happen once a step's prerequisites are
// done (starting it, asking the user on it, waiting on the user) while one it
// waits on, itself or through a step above it, is still open: the board would
// read it as where the work is, or as the user's move, before it can be. The
// same rule cmdrd's health check enforces after the fact (started-early,
// asked-early). With no readable board it lets the write through. The board can
// lag (a slow load holds it for minutes), so a blocker it names refuses only if
// a fresh read confirms it; a failed fresh read refuses on the board's word.
func checkReached(ctx context.Context, bc beads.Client, dir, project, beadID, what string) error {
	ws, _ := livedata.StepIn(boardSnapshot(), project, beadID)
	if ws == nil {
		return nil
	}
	// The step this command is closing is done by the time the write lands
	// (a close hands the next step over first); an approval the user gave
	// ahead for the gate in the way carries to the steps it covers.
	done := []string{closingBead}
	for {
		b := livedata.OpenBlockerOf(*ws, beadID, done...)
		if b == "" || coveringApproval(beadID) != "" {
			return nil
		}
		if holds, err := blockerHolds(ctx, bc, dir, beadID, b); err == nil && !holds {
			done = append(done, b) // the board is behind on this one: judge the next
			continue
		}
		title := b
		if _, s := livedata.StepIn(boardSnapshot(), project, b); s != nil {
			title = fmt.Sprintf("%s (%q)", b, s.Title)
		}
		return fmt.Errorf("%s on %s refused: it waits on %s, which is still open, so the user's board would read it as reached before it can be. Do it once %s is done; if %s does not really wait on it, remove that dependency first (br dep remove %s %s). Nothing was written", what, beadID, title, b, beadID, beadID, b)
	}
}

// blockerHolds reads fresh whether beadID, or a bead above it by its real
// parents, still waits on blocker by a blocks edge and blocker is still open.
func blockerHolds(ctx context.Context, bc beads.Client, dir, beadID, blocker string) (bool, error) {
	edge := false
	seen := map[string]bool{}
	for at := beadID; at != "" && !seen[at] && !edge; {
		seen[at] = true
		ds, err := beads.FreshDetails(ctx, bc, dir, []string{at})
		if err != nil {
			return false, err
		}
		d, ok := ds[at]
		if !ok {
			return false, fmt.Errorf("no details of %s", at)
		}
		edge = slices.ContainsFunc(d.Deps, func(dep beads.Dep) bool { return dep.Type == "blocks" && dep.DependsOnID == blocker })
		at = d.Parent
	}
	if !edge {
		return false, nil
	}
	b, err := beads.FreshShow(ctx, bc, dir, blocker)
	if err != nil {
		return false, err
	}
	if b == nil {
		return false, fmt.Errorf("no bead %s", blocker)
	}
	return !isClosedBead(*b) && !isDeferredBead(*b) && !beads.Superseded(*b), nil
}

// checkOneWaitPerReview refuses a review wait on a PR another open step of
// the workstream already waits on: one review drawn as two cards.
func checkOneWaitPerReview(project, beadID, review string, waits []beads.Wait) error {
	if review == "" || review == "none" || !slices.ContainsFunc(waits, func(w beads.Wait) bool { return w.Kind == beads.WaitReview }) {
		return nil
	}
	label, err := reviewLabel(review)
	if err != nil {
		return nil
	}
	_, num := livedata.ReviewRepo([]string{label}), livedata.ReviewPR([]string{"in-review", label})
	ws, _ := livedata.StepIn(boardSnapshot(), project, beadID)
	if ws == nil {
		return nil
	}
	for _, s := range flattenSnap(ws.Steps) {
		if s.BeadID == beadID || s.Review != num || s.Status == "closed" || s.State == "done" || s.Status == "deferred" || s.Superseded {
			continue
		}
		if s.ReviewFact != nil || strings.Contains(s.Waiting, "review") {
			return fmt.Errorf("--waiting review on %s refused: %s (%q) already waits on the review of PR #%s, so it would draw as two cards. Keep the wait on one step (the review gate, or the step that is the PR); clear the other's with --waiting none --review none. Nothing was written", beadID, s.BeadID, s.Title, num)
		}
	}
	return nil
}

// checkGateNotDuplicate refuses a gate titled like an open gate under the
// same parent: one checkpoint would read as both passed and pending.
func checkGateNotDuplicate(project, parent, title string) error {
	ws, p := livedata.StepIn(boardSnapshot(), project, parent)
	var siblings []livedata.SnapStep
	switch {
	case p != nil:
		siblings = p.Children
	case ws != nil:
		siblings = ws.Steps
	default:
		snap := boardSnapshot()
		if snap == nil {
			return nil
		}
		for _, w := range snap.Workstreams {
			if w.Project == project && w.BeadID == parent {
				siblings = w.Steps
			}
		}
	}
	want := strings.ToLower(strings.TrimSpace(title))
	for _, s := range siblings {
		if s.Gate && !s.Superseded && s.Status != "closed" && s.State != "done" && strings.ToLower(strings.TrimSpace(s.Title)) == want {
			return fmt.Errorf("gate %q refused: %s under %s is an open gate with the same title, so one checkpoint would read as both. If the new one replaces it, supersede %s first; if it is a different decision, title it as one. Nothing was created", title, s.BeadID, parent, s.BeadID)
		}
	}
	return nil
}

func flattenSnap(steps []livedata.SnapStep) []livedata.SnapStep {
	var out []livedata.SnapStep
	for _, s := range steps {
		out = append(out, s)
		out = append(out, flattenSnap(s.Children)...)
	}
	return out
}

// gateByID: id is a gate (read from br).
func gateByID(ctx context.Context, bc beads.Client, dir, id string) bool {
	b, err := bc.Show(ctx, dir, id)
	return err == nil && b != nil && isGateBead(*b)
}

const maxNotSameAskReason = 500

// checkNotSameAskReason: --not-the-same-ask needs a real sentence, not blank
// and at most maxNotSameAskReason characters.
func checkNotSameAskReason(given bool, reason string) error {
	if !given {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--not-the-same-ask needs a sentence saying what differs from the ancestor's card; it was empty. Nothing was written")
	}
	if n := utf8.RuneCountInString(reason); n > maxNotSameAskReason {
		return fmt.Errorf("--not-the-same-ask is %d characters; keep it to %d. Nothing was written", n, maxNotSameAskReason)
	}
	return nil
}

// logSecondCard records the escape on the gate and on the ancestor, before
// the card is written: a failed note stops the command, so no card stands
// without its note.
func logSecondCard(ctx context.Context, bc beads.Client, dir, gate, ancestor, reason string) error {
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "user"
	}
	note := fmt.Sprintf("second card allowed: not the same ask as %s: %s (by %s at %s)", ancestor, strings.TrimSpace(reason), who, time.Now().UTC().Format(time.RFC3339))
	for _, id := range []string{gate, ancestor} {
		if err := bc.AddComment(ctx, dir, id, note); err != nil {
			return fmt.Errorf("could not record the second-card note on %s: %v. The card was not written", id, err)
		}
	}
	return nil
}
