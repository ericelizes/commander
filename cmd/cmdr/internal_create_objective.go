package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/wsmgr"
)

var newBeadsClientFn = beads.New

type internalCreateObjectiveResult struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Title   string `json:"title"`
	Parent  string `json:"parent,omitempty"`
	Track   bool   `json:"track,omitempty"`
	// Proposed is set when a Manager's new gate or review step waits for Commander's review.
	Proposed string `json:"proposed,omitempty"`
	// Warnings lists follow-up steps that failed after the bead was created;
	// the bead exists, so never create it again.
	Warnings []string `json:"warnings,omitempty"`
	// Complete is false when a wait the caller asked for is missing (Unlinked);
	// the exit code is then exitUnlinked.
	Complete bool           `json:"complete"`
	Unlinked []unlinkedEdge `json:"unlinked,omitempty"`
	// Custody is set when the new step carries its Manager's "Manager to start"
	// receipt (nothing else held it).
	Custody []string `json:"custody,omitempty"`
}

// unlinkedEdge is a wait the verb could not write: Waiter does not yet wait on
// On. Finish is the command that writes it.
type unlinkedEdge struct {
	Waiter string `json:"waiter"`
	On     string `json:"on"`
	Finish string `json:"finish"`
}

// exitUnlinked is the exit code of a create whose bead exists but lacks a wait
// it was asked for: a gate may guard nothing yet, so `... && spawn` must stop,
// and the bead must not be created again.
const exitUnlinked = 3

// linkRetryWaits are the pauses before each repeat of a link write that failed
// for a passing reason.
var linkRetryWaits = []time.Duration{2 * time.Second, 5 * time.Second}

// landedLookupWaits are the pauses before each look for a create whose answer
// was lost: the door may still be writing it.
var landedLookupWaits = []time.Duration{0, 3 * time.Second, 7 * time.Second, 10 * time.Second}

var approvalTitlePrefixes = []string{
	"greenlight", "approve", "approval", "workflow approved", "spec approved", "sign-off", "sign off", "signoff", "go/no-go", "go-no-go",
}

// looksLikeApproval reports whether a title reads as an the user approval, which
// belongs in a gate bead, not a plain task.
func looksLikeApproval(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, prefix := range approvalTitlePrefixes {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func runInternalCreateObjective(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-create-objective", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	projectName := fs.String("project", "", "project slug")
	title := fs.String("title", "", "objective title")
	parent := fs.String("parent", "", "parent objective bead id (creates a child objective)")
	gate := fs.Bool("gate", false, "make it a human approval gate: no work past it starts until it is approved (closed)")
	approver := fs.String("approver", "", "with --gate: who approves, comma-separated (user, a GitHub login, a person's name)")
	after := fs.String("after", "", "with --gate: the work before the gate, comma-separated bead ids (the gate waits on them)")
	before := fs.String("before", "", "with --gate: the work after the gate, comma-separated bead ids (they wait on the gate)")
	track := fs.Bool("workstream", false, "create a nested WORKSTREAM under the --parent workstream: a separate outcome with its own gate chain and status, same Manager")
	fs.BoolVar(track, "track", false, "old name of --workstream")
	takeSteps := fs.Bool("take-open-steps", false, "with --workstream: move the --parent's open steps into the new workstream, so it can split")
	criteria := fs.String("criteria", "", "with --gate (required): what counts as passed, in one sentence; stored as the bead's acceptance criteria and shown on the gate in the diagram")
	operational := fs.Bool("operational", false, "create an operational bead (kind:operational, not an objective) under --parent: an Operator's own sub-task")
	deps := fs.String("deps", "", "dependencies of the new bead, comma-separated: `blocked-by:ID` (or a bare ID) makes the new bead wait for ID, `blocks:ID` makes ID wait for the new bead")
	var waitings stringList
	fs.Var(&waitings, "waiting", "a plain step's holder: what it waits on (ci:#N, review:<who>, until:<time>[:label], operator:<running role>, ...); repeatable")
	owner := fs.String("owner", "", "a plain step's holder: the running Operator (operator-...) that has it")
	notAGate := fs.Bool("not-a-gate", false, "the title only looks like an approval (Greenlight, Approve...) but is ordinary work")

	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *title == "" {
		emitErr(stderr, "missing required flag(s): --project, --title")
		return 1
	}
	if !*gate && !*notAGate && looksLikeApproval(*title) {
		emitErr(stderr, fmt.Sprintf("%q reads like an approval by the user: an approval must be a gate bead: internal-create-objective --gate --approver user --criteria \"<what counts as passed>\" --before <work> (or --not-a-gate if it is ordinary work)", *title))
		return 1
	}
	labels := []string{"kind:objective"}
	if *operational {
		if *parent == "" || *gate || *track {
			emitErr(stderr, "--operational needs --parent <bead> and does not combine with --gate or --workstream")
			return 1
		}
		labels = []string{"kind:operational"}
	}
	if *takeSteps && !*track {
		emitErr(stderr, "--take-open-steps goes with --workstream: only a new workstream can take its parent's open steps")
		return 1
	}
	if *track {
		if *parent == "" {
			emitErr(stderr, "--workstream needs --parent <workstream bead> (a new top-level objective is already a workstream)")
			return 1
		}
		if *gate {
			emitErr(stderr, "--workstream and --gate do not combine: a gate is a child of the workstream, create it with --parent <workstream>")
			return 1
		}
		labels = append(labels, beads.WorkstreamLabel)
	} else if *parent == "" && !*gate {
		labels = append(labels, beads.WorkstreamLabel)
	}
	if *gate {
		approvers := strings.FieldsFunc(*approver, func(r rune) bool { return r == ',' || r == ' ' })
		if len(approvers) == 0 {
			emitErr(stderr, "a gate needs --approver: who approves it (user, a GitHub login, a name)")
			return 1
		}
		if err := checkGreenlightIsUsers(*title, approvers); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		if err := checkGateHasCriteria(*criteria); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		labels = append(labels, beads.GateLabel)
		for _, a := range approvers {
			labels = append(labels, beads.ApproverLabelPrefix+beads.OwnerLabelName(ownercompat.Normalize(a)))
		}
	} else if *approver != "" || *after != "" || *before != "" || *criteria != "" {
		emitErr(stderr, "--approver, --after, --before and --criteria go with --gate")
		return 1
	}

	waitsOn, blocksIDs, err := parseCreateDeps(*deps)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	birthLabels, err := birthHolder(birthKind(*gate, *operational, *track || *parent == ""), *title, waitings, *owner, waitsOn, time.Now())
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	labels = append(labels, birthLabels...)
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	started := time.Now()

	bc := newBeadsClientFn()
	if !*operational {
		if err := beads.CheckPlacement(ctx, bc, project.Path, *parent, beads.Placement{Workstream: *track, TakingSteps: *takeSteps}); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}
	if *gate && *parent != "" {
		if err := checkGateNotDuplicate(project.Name, *parent, *title); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}
	if !*gate {
		for _, id := range blocksIDs {
			if err := checkNotGateEdge(ctx, bc, project.Path, id, "--deps blocks:", false); err != nil {
				emitErr(stderr, err.Error())
				return 1
			}
		}
	}
	if err := checkGateLinksAcyclic(ctx, bc, project.Path, append(splitIDs(*after), waitsOn...), append(splitIDs(*before), blocksIDs...)); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	var proposed *proposal
	if *parent != "" && (*gate || reviewTitle.MatchString(*title)) {
		if err := checkStandingDecisions(ctx, bc, project.Path, *parent, *title); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		// A new track's own workflow gate sits under the track, whose workflow is not approved yet.
		if proposingManager() != "" && workflowApproved(ctx, bc, project.Path, *parent) {
			proposed = &proposal{kind: beads.ProposedReview, detail: fmt.Sprintf("review step %q under %s, after the workflow was approved", *title, *parent)}
			if *gate {
				proposed = &proposal{kind: beads.ProposedGate, detail: fmt.Sprintf("gate %q (approver %s) under %s, after the workflow was approved", *title, *approver, *parent)}
			}
			labels = append(labels, proposed.label(time.Now()))
		}
	}
	if strings.HasPrefix(*owner, "operator-") {
		if err := checkOwnerLive(ctx, *owner); err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}
	beadID, err := bc.Create(ctx, project.Path, *title, beads.CreateOptions{
		Parent:             *parent,
		Labels:             labels,
		AcceptanceCriteria: strings.TrimSpace(*criteria),
		Silent:             true,
	})
	var warnings []string
	warn := func(msg string) {
		warnings = append(warnings, msg)
		fmt.Fprintf(stderr, "warning: bead %s was created (do not create it again), but %s\n", beadID, msg)
	}
	if err != nil && beadID == "" && beads.MayHaveApplied(err) {
		beadID = findLandedCreate(bc, project.Path, *title, *parent, "", started)
		if beadID == "" {
			err = fmt.Errorf("%w; no such bead shows yet: look for it before creating it again", err)
		}
	}
	if err != nil {
		if beadID == "" {
			emitErr(stderr, err.Error())
			return 1
		}
		warn(fmt.Sprintf("finishing the create failed: %v", err))
	}

	// A lost answer can use up the whole budget: what follows the create gets
	// its own, or every step below would fail at once.
	ctx, cancelRest := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancelRest()

	// The first step is the plan the new scope was waiting for: its own holder is now on the board.
	if *parent != "" && beadID != "" && !*operational {
		if err := clearPlanWait(ctx, bc, project.Path, *parent); err != nil {
			warn(fmt.Sprintf("clearing the Manager to plan wait on %s failed: %v", *parent, err))
		}
	}

	if *takeSteps {
		steps, err := beads.OpenChildren(ctx, bc, project.Path, *parent)
		if err != nil {
			warn(fmt.Sprintf("reading the open steps of %s failed: %v; move them with cmdr internal-track convert", *parent, err))
		}
		for _, k := range steps {
			if k.ID == beadID || beads.IsWorkstream(k.Labels) {
				continue
			}
			if err := bc.SetParent(ctx, project.Path, k.ID, beadID); err != nil {
				warn(fmt.Sprintf("moving %s into it failed: %v", k.ID, err))
			}
		}
	}

	// A gate sits between work: it waits on what comes before it, and what
	// comes after it waits on the gate. Once the bead exists a failed link is
	// never "failed" (a caller that read that would create the bead again): the
	// result names the missing edge and the command that writes it, and the exit
	// code stops a `... && spawn` chain.
	var unlinked []unlinkedEdge
	link := func(waiter, on string) {
		if err := writeLink(ctx, bc, project.Path, waiter, on); err != nil {
			finish := linkCommand(project.Name, beadID, waiter, on)
			unlinked = append(unlinked, unlinkedEdge{Waiter: waiter, On: on, Finish: finish})
			warn(fmt.Sprintf("making %s wait for %s failed: %v; finish with: %s", waiter, on, err, finish))
		}
	}
	for _, id := range splitIDs(*after) {
		link(beadID, id)
	}
	for _, id := range splitIDs(*before) {
		link(id, beadID)
	}
	for _, id := range waitsOn {
		link(beadID, id)
	}
	for _, id := range blocksIDs {
		link(id, beadID)
	}

	// A plain step nothing holds yet (ready, no Operator, no wait) is the
	// Manager's to start: it carries that receipt from the start, so the board
	// never reads it State missing once the creating turn ends.
	var custody []string
	if bornWithReceipt(birthLabels) {
		custody = []string{beadID}
	} else if *parent != "" && !*gate && !*operational && !*track {
		// A named holder that did not hold (a closed or parked blocker, an edge that failed to
		// land): the step gets the receipt now.
		held, err := holdForManager(ctx, bc, project.Path, []string{beadID}, "", time.Now())
		if err != nil {
			warn(fmt.Sprintf("its Manager's hold was not written: %v; give it a holder (spawn, --waiting or a card)", err))
		}
		custody = held
	}

	if proposed != nil {
		if err := announceProposal(ctx, bc, project.Path, project.Name, beadID, proposingManager(), *proposed, stderr); err != nil {
			warnings = append(warnings, fmt.Sprintf("mailing Commander about the proposed %s failed: %v", proposed.kind, err))
		}
	}

	// Every workstream has a Manager: a new top-level objective gets one now, and
	// so does a nested one while the project's nested Managers are on.
	if *track && *parent != "" {
		startNestedManager(ctx, bc, project.Name, project.Path, beadID, warn)
	}
	if *parent == "" {
		if opener, cleanup, err := newWorkstreamOpener(); err != nil {
			warn(fmt.Sprintf("its Manager could not start: %v", err))
		} else {
			if _, err := opener.Open(ctx, project.Name, beadID, wsmgr.OpenOptions{}); err != nil {
				warn(fmt.Sprintf("its Manager could not start: %v", err))
			}
			cleanup()
		}
	}

	if err := json.NewEncoder(stdout).Encode(internalCreateObjectiveResult{
		Project:  project.Name,
		BeadID:   beadID,
		Title:    *title,
		Parent:   *parent,
		Track:    *track,
		Proposed: proposedKind(proposed),
		Warnings: warnings,
		Custody:  custody,
		Complete: len(unlinked) == 0,
		Unlinked: unlinked,
	}); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	if len(unlinked) > 0 {
		return exitUnlinked
	}
	return 0
}

// writeLink makes waiter wait on on, repeating a write that failed for a passing
// reason. A repeat first looks: an earlier try whose answer was lost may have
// written the edge.
func writeLink(ctx context.Context, bc beads.Client, dir, waiter, on string) error {
	for try := 0; ; try++ {
		err := bc.DependOn(ctx, dir, waiter, on)
		if err == nil {
			return nil
		}
		if try >= len(linkRetryWaits) || !(isBusyRead(err) || beads.MayHaveApplied(err)) {
			return err
		}
		time.Sleep(linkRetryWaits[try])
		if deps, derr := bc.Deps(ctx, dir, waiter); derr == nil && slices.ContainsFunc(deps, func(d beads.Dep) bool {
			return d.Type == "blocks" && d.DependsOnID == on
		}) {
			return nil
		}
	}
}

// linkCommand is the command that writes one missing edge of bead: internal-gate
// link, the verb that takes gate and track edges (internal-dep refuses them).
func linkCommand(project, bead, waiter, on string) string {
	if waiter == bead {
		return fmt.Sprintf("cmdr internal-gate link --project %s --bead %s --after %s", project, bead, on)
	}
	return fmt.Sprintf("cmdr internal-gate link --project %s --bead %s --before %s", project, bead, waiter)
}

func splitIDs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// parseCreateDeps reads --deps: `blocked-by:ID` (or a bare ID) = the new bead
// waits for ID; `blocks:ID` = ID waits for the new bead.
func parseCreateDeps(spec string) (waitsOn, blocks []string, err error) {
	for _, item := range splitIDs(spec) {
		kind, id, found := strings.Cut(item, ":")
		if !found {
			kind, id = "blocked-by", item
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, nil, fmt.Errorf("--deps %q names no bead", item)
		}
		switch strings.TrimSpace(kind) {
		case "blocked-by":
			waitsOn = append(waitsOn, id)
		case "blocks":
			blocks = append(blocks, id)
		default:
			return nil, nil, fmt.Errorf("--deps %q: use blocked-by:ID (the new bead waits for ID) or blocks:ID (ID waits for the new bead)", item)
		}
	}
	return waitsOn, blocks, nil
}

// findLandedCreate looks for the bead a create whose answer was lost made,
// looking again for a short while as the door may still be writing it. "" when
// it never shows. A non-empty label is the create's own marker: only a bead
// carrying it matches.
func findLandedCreate(bc beads.Client, dir, title, parent, label string, started time.Time) string {
	for _, wait := range landedLookupWaits {
		time.Sleep(wait)
		if id := lookForCreate(bc, dir, title, parent, label, started); id != "" {
			return id
		}
	}
	return ""
}

// lookForCreate finds the one bead titled title (under parent, carrying label
// when one is given) created since started. "" when none or more than one
// matches, or the read fails.
func lookForCreate(bc beads.Client, dir, title, parent, label string, started time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	list, err := bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	if err != nil {
		return ""
	}
	found := ""
	for _, b := range list {
		if b.Title != title {
			continue
		}
		if parent != "" && b.ParentID != parent && !strings.HasPrefix(b.ID, parent+".") {
			continue
		}
		if label != "" && !slices.Contains(b.Labels, label) {
			continue
		}
		if at, err := time.Parse(time.RFC3339, b.CreatedAt); err != nil || at.Before(started.Add(-time.Minute)) {
			continue
		}
		if found != "" {
			return ""
		}
		found = b.ID
	}
	return found
}

// bornWithReceipt: the create's own labels carry the "Manager to start" receipt.
func bornWithReceipt(labels []string) bool {
	for _, w := range beads.WaitsOf(labels) {
		if w.Ref == beads.ManagerStartWait {
			return true
		}
	}
	return false
}

// Kinds of bead a create can make, for the holder it must be born with.
const (
	birthStep = iota
	birthScope
	birthGate
	birthOperational
)

func birthKind(gate, operational, scope bool) int {
	switch {
	case gate:
		return birthGate
	case operational:
		return birthOperational
	case scope:
		return birthScope
	}
	return birthStep
}

// birthHolder is the labels that make a new bead held in the same create (atomic birth):
// a workstream or track is born waiting on its Manager to plan, a plain step carries the holder it
// names (a wait, an open blocker or a running Operator) or else its Manager's "Manager to start"
// receipt, a gate is held by definition. A named blocker that turns out closed or parked, or whose
// edge fails to land, is caught after the links by holdForManager.
func birthHolder(kind int, title string, waitings stringList, owner string, blockedBy []string, now time.Time) ([]string, error) {
	switch kind {
	case birthScope:
		w := beads.Wait{Kind: beads.WaitUntil, Since: now, Until: now.Add(2 * time.Minute), Ref: beads.ManagerPlanWait}
		return []string{w.Label()}, nil
	case birthGate, birthOperational:
		if len(waitings) > 0 || owner != "" {
			return nil, fmt.Errorf("--waiting and --owner give a plain step its holder; a gate or an operational bead needs neither")
		}
		return nil, nil
	}
	waits, err := parseWaitingFlags(waitings)
	if err != nil {
		return nil, err
	}
	if owner != "" && !strings.HasPrefix(owner, "operator-") {
		return nil, fmt.Errorf("--owner %s: a step is born held by a running Operator (operator-...), not by a Manager or the user; use --waiting <kind> for a wait, or --waiting operator:<running role>", owner)
	}
	if len(waits) == 0 && len(blockedBy) == 0 && owner == "" {
		// Nothing named: the step is its Manager's to start, and it is born carrying that
		// receipt (the same "Manager to start" a close writes), so it never reads State missing.
		return []string{custodyLabel(now)}, nil
	}
	var labels []string
	for _, w := range waits {
		w.Since = now
		if l := w.Label(); len(l) > beads.MaxLabelLen {
			return nil, fmt.Errorf("--waiting %s: too long to record (%d > %d characters)", w.Kind, len(l), beads.MaxLabelLen)
		} else {
			labels = append(labels, l)
		}
	}
	if owner != "" {
		labels = append(labels, beads.OwnerLabelPrefix+beads.OwnerLabelName(owner))
	}
	return labels, nil
}

// clearPlanWait removes the Manager to plan wait from a scope that now has its first step.
func clearPlanWait(ctx context.Context, bc beads.Client, dir, parent string) error {
	p, err := bc.Show(ctx, dir, parent)
	if err != nil || p == nil {
		return err
	}
	for _, l := range p.Labels {
		if w, ok := beads.ParseWaitLabel(l); ok && w.Kind == beads.WaitUntil && w.Ref == beads.ManagerPlanWait {
			if err := bc.SetLabel(ctx, dir, parent, l, false); err != nil {
				return err
			}
		}
	}
	return nil
}
