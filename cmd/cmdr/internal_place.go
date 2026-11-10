package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wsmgr"
)

type internalPlaceResult struct {
	Project  string   `json:"project"`
	BeadID   string   `json:"bead_id"`
	Parent   string   `json:"parent"`
	Wrapped  string   `json:"wrapped,omitempty"` // the workstream made to hold the parent's open steps
	Warnings []string `json:"warnings,omitempty"`
}

// runInternalPlace is one drop in the sidebar: bead moves under --parent ("" for
// the top level), then takes its place among its siblings: after --after, or at
// rank --to. Who owns the work follows the tree, so the Managers involved are told.
func runInternalPlace(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-place", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectName := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "the workstream bead to place")
	parent := fs.String("parent", "", "the workstream it goes under; empty for the top level")
	after := fs.String("after", "", "the sibling it goes after")
	ordinary := fs.Bool("ordinary", false, "reparent an ordinary step or existing gate without conversion")
	dry := fs.Bool("dry-run", false, "ordinary: validate without writing")
	to := fs.Int("to", 0, "its rank among its siblings (1 = first), when --after is not given")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *projectName == "" || *bead == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	if *ordinary {
		if *parent == "" || *after != "" || *to != 0 {
			emitErr(stderr, "--ordinary requires --parent and forbids ranking")
			return 1
		}
		return runOrdinaryPlace(*projectName, *bead, *parent, *dry, stdout, stderr)
	}
	if *dry {
		emitErr(stderr, "--dry-run requires --ordinary")
		return 1
	}
	if callerIsOperator(os.Getenv("COMMANDER_ROLE")) {
		emitErr(stderr, "an Operator cannot move a workstream: ask your Manager")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*beadWriteTimeout)
	defer cancel()
	bc := newBeadsClientFn()
	result := internalPlaceResult{Project: project.Name, BeadID: *bead, Parent: *parent}
	warn := func(msg string) { result.Warnings = append(result.Warnings, msg) }

	// The owner (nearest open workstream; the root while nested Managers are off).
	oldRoot, err := wsmgr.StrictOwner(ctx, bc, project.Path, *bead)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	moved, wrapped, err := placeBead(ctx, bc, project.Path, *bead, *parent)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	result.Wrapped = wrapped
	if wrapped != "" && *after == "" && *to <= 1 {
		*after = wrapped // the parent's own work stays first
	}
	if moved {
		settleOwnership(ctx, bc, project, *bead, oldRoot, warn)
	}
	if *after != "" || *to > 0 {
		if err := rankPlaced(ctx, project.Name, *bead, *parent, *after, *to); err != nil {
			warn(fmt.Sprintf("it moved, but its place among its siblings was not set: %v", err))
		}
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// placeBead moves bead under parent. A parent with open steps keeps its shape: those
// steps move into a new workstream of the same name, beside the bead. Moving away
// from such a parent undoes that. It reports whether bead moved, and the workstream
// it made.
func placeBead(ctx context.Context, bc beads.Client, dir, bead, parent string) (bool, string, error) {
	oldParent := ""
	if d, err := beads.FreshDetails(ctx, bc, dir, []string{bead}); err == nil {
		oldParent = d[bead].Parent
	}
	moved, wrapped, err := moveBead(ctx, bc, dir, bead, parent)
	if err == nil && moved && oldParent != "" {
		err = unwrapSteps(ctx, bc, dir, oldParent)
	}
	return moved, wrapped, err
}

// unwrapSteps folds a parent's one remaining child back in when it is only the
// holder a drop made for the parent's steps: same title, nothing else left beside it.
func unwrapSteps(ctx context.Context, bc beads.Client, dir, parent string) error {
	children, err := beads.OpenChildren(ctx, bc, dir, parent)
	if err != nil || len(children) != 1 || !beads.IsWorkstream(children[0].Labels) {
		return err
	}
	p, err := beads.FreshShow(ctx, bc, dir, parent)
	if err != nil || p == nil || p.Title != children[0].Title {
		return err
	}
	steps, err := beads.OpenChildren(ctx, bc, dir, children[0].ID)
	if err != nil {
		return err
	}
	edits := map[string]holding.Edit{children[0].ID: func(b *holding.Bead) { b.Status = holding.StatusClosed }}
	for _, k := range steps {
		edits[k.ID] = func(b *holding.Bead) { b.Parent = parent }
	}
	owed, err := requireHeldAfter(ctx, bc, dir, children[0].ID, "folding "+children[0].ID+" back into "+parent, moveFix, edits)
	if err != nil {
		return err
	}
	for _, k := range steps {
		if err := bc.SetParent(ctx, dir, k.ID, parent); err != nil {
			return err
		}
	}
	if err := bc.CloseWithReason(ctx, dir, children[0].ID, "Held its parent's steps while another workstream sat beside it; they are back under "+parent); err != nil {
		return err
	}
	_, err = owed.settle(ctx, "")
	return err
}

func moveBead(ctx context.Context, bc beads.Client, dir, bead, parent string) (bool, string, error) {
	d, err := beads.FreshDetails(ctx, bc, dir, []string{bead})
	if err != nil {
		return false, "", err
	}
	if _, ok := d[bead]; !ok {
		return false, "", fmt.Errorf("no bead %s", bead)
	}
	if d[bead].Parent == parent {
		return false, "", nil
	}
	if parent == "" {
		if err := requireMoveHeld(ctx, bc, dir, bead, "", "moving "+bead+" to the top level"); err != nil {
			return false, "", err
		}
		if err := bc.SetParent(ctx, dir, bead, ""); err != nil {
			return false, "", err
		}
		if err := bc.SetLabel(ctx, dir, bead, beads.WorkstreamLabel, true); err != nil {
			return true, "", err
		}
		return true, "", bc.SetLabel(ctx, dir, bead, beads.TrackLabel, false)
	}
	if slices.Contains(beads.Lineage(ctx, bc, dir, nil, parent), bead) {
		return false, "", fmt.Errorf("%s sits under %s: a bead cannot move under its own descendant", parent, bead)
	}
	if err := beads.CheckPlacement(ctx, bc, dir, parent, beads.Placement{Workstream: true, Self: bead, TakingSteps: true}); err != nil {
		return false, "", err
	}
	children, err := beads.OpenChildren(ctx, bc, dir, parent)
	if err != nil {
		return false, "", err
	}
	var steps []string
	for _, k := range children {
		if k.ID != bead && !beads.IsWorkstream(k.Labels) {
			steps = append(steps, k.ID)
		}
	}
	wrapped := ""
	if len(steps) > 0 {
		p, err := beads.FreshShow(ctx, bc, dir, parent)
		if err != nil || p == nil {
			return false, "", fmt.Errorf("could not read %s: %v", parent, err)
		}
		wrapped, err = bc.Create(ctx, dir, p.Title, beads.CreateOptions{Parent: parent, Labels: []string{"kind:objective", beads.WorkstreamLabel}, Silent: true})
		if err != nil {
			return false, "", err
		}
		for _, id := range steps {
			if err := bc.SetParent(ctx, dir, id, wrapped); err != nil {
				return false, wrapped, fmt.Errorf("moving %s into %s failed: %w; nothing else moved", id, wrapped, err)
			}
		}
	}
	return true, wrapped, convertToTrack(ctx, bc, dir, bead, parent)
}

// settleOwnership follows a move across owning workstreams: a workstream that
// stops owning itself (moved under a step, or under another workstream while
// nested Managers are off) gives up its registration (the session stays, so moving
// it back resumes it), one that becomes an owner gets a Manager, and both sides hear.
func settleOwnership(ctx context.Context, bc beads.Client, project *store.Project, bead, oldRoot string, warn func(string)) {
	newRoot, err := wsmgr.StrictOwner(ctx, bc, project.Path, bead)
	if err != nil {
		warn(fmt.Sprintf("could not read who owns it now: %v", err))
		return
	}
	if newRoot == oldRoot {
		return
	}
	// Asked before the row goes: cmdrd finds a terminal through the row's session file,
	// so afterwards a terminal-held Manager reads as stopped.
	oldListens := oldRoot != bead || formerManagerListens(ctx, wsmgr.Role(oldRoot))
	if oldRoot == bead {
		st, cleanup, err := needsYouStoreFn()
		if err != nil {
			warn(fmt.Sprintf("its old Manager stays registered: %v", err))
		} else {
			defer cleanup()
			if _, err := st.UnregisterNestedManager(ctx, project.ID, bead, newRoot, "user"); err != nil {
				warn(fmt.Sprintf("its old Manager stays registered: %v", err))
			}
		}
	}
	if newRoot == bead {
		opener, cleanup, err := newWorkstreamOpener()
		if err != nil {
			warn(fmt.Sprintf("its Manager could not start: %v", err))
		} else {
			defer cleanup()
			if _, err := opener.Open(ctx, project.Name, bead, wsmgr.OpenOptions{}); err != nil {
				warn(fmt.Sprintf("its Manager could not start: %v", err))
			}
		}
	}
	st, client, key, cleanup, err := mailDeps()
	if err != nil {
		warn(fmt.Sprintf("its Managers were not told: %v", err))
		return
	}
	defer cleanup()
	subject := "The user moved " + bead
	tell := func(role, body string) {
		if err := sendRoleMail(ctx, st, client, key, "commander", []string{role}, subject, body, agentmail.SendOptions{Importance: "high"}); err != nil {
			warn(fmt.Sprintf("%s was not told: %v", role, err))
		}
	}
	if oldListens {
		tell(wsmgr.Role(oldRoot), fmt.Sprintf("The user moved %s out of your workstream, in the sidebar. It now belongs to %s, so stop working on it. Its Operators report to that Manager.", bead, wsmgr.Role(newRoot)))
	}
	tell(wsmgr.Role(newRoot), fmt.Sprintf("The user moved %s into your workstream, in the sidebar. It came from %s. You own it now: read it with cmdr board --diagram %s and carry it on.", bead, wsmgr.Role(oldRoot), bead))
}

// formerManagerListens says whether a workstream about to give up its registration
// has a Manager to tell. Mail cannot start one with no registration (wsmgr.EnsureRole),
// so a notice to a stopped one sits unread and health reports the role for good; the
// new owner hears anyway. When cmdrd cannot say, send: a stray notice costs less than
// a Manager working on.
func formerManagerListens(ctx context.Context, role string) bool {
	ctx, cancel := context.WithTimeout(ctx, formerManagerReadTimeout)
	defer cancel()
	st, err := cmdrdChats{home: appCommanderHome()}.status(ctx, role)
	return err != nil || st.Running || st.OwnedBy != "" || st.OwnerUnknown
}

// formerManagerReadTimeout keeps a hung cmdrd from spending the time both notices need.
var formerManagerReadTimeout = 5 * time.Second

// rankPlaced sets bead's place among its new siblings.
func rankPlaced(ctx context.Context, project, bead, parent, after string, to int) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if parent == "" {
		if err := tui.PlaceWorkstreamAt(ctx, project, bead, max(to, 1)); err != nil {
			return err
		}
		if opener, cleanup, err := newWorkstreamOpener(); err == nil {
			_ = opener.SyncNames(ctx)
			cleanup()
		}
		return nil
	}
	return tui.MoveBeadAfter(ctx, project, bead, parent, after, to)
}
