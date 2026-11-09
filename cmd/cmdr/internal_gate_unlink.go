package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
	"github.com/example-user/commander/internal/wsmgr"
)

// gateUnlink is `internal-gate unlink --bead G --waiter S[,S2] --reason R`: it
// removes the edge "S waits on gate G" when S is a step the plan dropped
// (superseded or retired). internal-dep refuses every gate edge and
// internal-gate link only adds, so such an obsolete edge had no supported way
// out. The verb is narrow on purpose:
//
//   - G must be an open gate bead; it stays open and unapproved (only the one
//     edge is removed, nothing about G changes);
//   - each S must be superseded/retired, not a gate or track, and really wait
//     on G (fresh reads, never the display cache);
//   - S and G must belong to the same workstream (real parent edges);
//   - only that workstream's Manager, Commander or the user may run it; any
//     other caller gets the exact command for the owning Manager.
//
// Edges of live steps, and the gate's own waits, are never touched. Reopening
// an unlinked step does not restore its wait; link it again with cmdr
// internal-gate link.
func gateUnlink(ctx context.Context, st *store.Store, projectName, gateID string, waiters []string, reason string) (any, error) {
	if len(waiters) == 0 {
		return nil, errors.New("unlink: name the obsolete steps that wait on the gate with --waiter")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("unlink: say why the edge is obsolete with --reason (it is recorded on the gate)")
	}
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return nil, errors.New("refused: Operators do not change the gate chain; tell your Manager which edge is obsolete (cmdr internal-mail --to <manager> --subject ... --body ...)")
	}
	p, err := st.GetProjectByName(ctx, projectName)
	if err != nil || p == nil {
		return nil, fmt.Errorf("project %q not added", projectName)
	}
	bc := freshReader(newBeadsClientFn())
	gate, err := beads.FreshShow(ctx, bc, p.Path, gateID)
	if err != nil {
		return nil, fmt.Errorf("could not read %s (%v); nothing written, retry in a minute", gateID, err)
	}
	if gate == nil {
		return nil, fmt.Errorf("no bead %s", gateID)
	}
	if !isGateBead(*gate) {
		return nil, fmt.Errorf("%s is not a gate bead: unlink removes only an obsolete step's wait on a gate. For an edge between ordinary steps use cmdr internal-dep --project %s rm <step> <depends-on>", gateID, projectName)
	}
	if gate.Status == "closed" || gate.Status == "tombstone" || beads.Superseded(*gate) {
		return nil, fmt.Errorf("%s is %s: unlink only clears obsolete waits on an open gate (a closed gate holds nothing)", gateID, statusWord(*gate))
	}
	root, err := gateWorkstream(ctx, bc, p.Path, *gate)
	if err != nil {
		return nil, fmt.Errorf("could not read the workstream of %s (%v); nothing written, retry in a minute", gateID, err)
	}
	cmd := unlinkCommand(projectName, gateID, waiters, reason)
	owners, err := gateOwnerRoles(ctx, bc, p.Path, *gate)
	if err != nil {
		return nil, fmt.Errorf("could not read the owner of %s (%v); nothing written, retry in a minute", gateID, err)
	}
	if role != "" && role != "commander" && !roleAmong(role, owners) {
		return nil, fmt.Errorf("refused: %s belongs to workstream %s, whose Manager is %s. Nothing written. Ask it to run:\n  %s", gateID, strings.TrimPrefix(owners[0], "manager-"), owners[0], cmd)
	}
	// Judge every waiter before writing anything, so one bad id leaves all edges.
	// A waiter whose edge is gone counts as done only when an earlier unlink
	// note on the gate names it (a repeat after a partial failure or a full
	// success); any other missing edge is a wrong id and refuses.
	var toRemove, gone []string
	var noted map[string]bool
	for _, w := range waiters {
		present, err := checkObsoleteWaiter(ctx, bc, p.Path, projectName, gateID, root, w)
		if err != nil {
			return nil, err
		}
		if role != "" && role != "commander" {
			// The waiter's own owners too: a nested Manager over the gate acts
			// only on waiters in its subtree.
			wowners, err := ownerRoles(ctx, bc, p.Path, w)
			if err != nil {
				return nil, fmt.Errorf("could not read the owner of %s (%v); nothing written, retry in a minute", w, err)
			}
			if !roleAmong(role, wowners) {
				return nil, fmt.Errorf("refused: %s belongs to %s's workstream, not %s's. Nothing written. Ask %s to run:\n  %s", w, wowners[0], role, wowners[0], cmd)
			}
		}
		if present {
			toRemove = append(toRemove, w)
			continue
		}
		if noted == nil {
			ds, err := beads.FreshDetails(ctx, bc, p.Path, []string{gateID})
			if err != nil {
				return nil, fmt.Errorf("could not read the notes on %s (%v); nothing written, retry in a minute", gateID, err)
			}
			noted = unlinkNoted(ds[gateID].Comments)
		}
		if !noted[w] {
			return nil, fmt.Errorf("refused: %s has no wait on %s, and no earlier unlink note on %s names it; check the id. Nothing written", w, gateID, gateID)
		}
		gone = append(gone, w)
	}
	by := role
	if by == "" {
		by = "user"
	}
	var removed []string
	var removeErr error
	for _, w := range toRemove {
		if err := newBeadsClientFn().RemoveDep(ctx, p.Path, w, gateID); err != nil {
			removeErr = fmt.Errorf("removing the edge %s -> %s failed after %d of %d were removed (%v)", w, gateID, len(removed), len(toRemove), err)
			break
		}
		removed = append(removed, w)
	}
	// The note records what this run removed even when a later edge failed,
	// so no removal goes unrecorded. A repeat counts a gone edge as done only
	// by this note, so a failed note says how to write it by hand.
	var warnings []string
	var noteErr error
	var note string
	if len(removed) > 0 {
		note = fmt.Sprintf("gate unlink by %s: removed the obsolete wait of %s on this gate (%s). The gate stays open and unapproved.", by, strings.Join(removed, ", "), strings.TrimSpace(reason))
		noteErr = newBeadsClientFn().AddComment(ctx, p.Path, gateID, note)
	}
	noteCmd := fmt.Sprintf("cmdr internal-comment --project %s --bead %s --text %q", projectName, gateID, note)
	if removeErr != nil {
		msg := removeErr.Error()
		if noteErr != nil {
			msg += fmt.Sprintf("; the note on %s failed too (%v). Write it first, or the repeat refuses the removed edges: %s.", gateID, noteErr, noteCmd)
		}
		return nil, fmt.Errorf("%s. Repeat to finish (edges an unlink note names count as done): %s", msg, cmd)
	}
	if noteErr != nil {
		warnings = append(warnings, fmt.Sprintf("the edges are removed but the note on %s failed: %v; write it: %s", gateID, noteErr, noteCmd))
	}
	out := map[string]any{"bead_id": gateID, "workstream": root, "removed": nonNil(removed), "already_removed": nonNil(gone), "gate_status": gate.Status}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// unlinkNoted is the set of waiters an earlier unlink note on the gate names
// ("gate unlink by R: removed the obsolete wait of A, B on this gate ...").
func unlinkNoted(comments []beads.Comment) map[string]bool {
	noted := map[string]bool{}
	for _, c := range comments {
		rest, ok := strings.CutPrefix(c.Text, "gate unlink by ")
		if !ok {
			continue
		}
		for {
			var list string
			if _, rest, ok = strings.Cut(rest, "the obsolete wait of "); !ok {
				break
			}
			if list, rest, ok = strings.Cut(rest, " on this gate"); !ok {
				break
			}
			for _, w := range strings.Split(list, ", ") {
				noted[strings.TrimSpace(w)] = true
			}
		}
	}
	return noted
}

// checkObsoleteWaiter refuses a waiter unlink must not touch: a live step, a
// gate or track, one outside the gate's workstream, or the review block
// plugged in front of the gate. present says whether the edge still exists
// (false = already removed, nothing to do).
func checkObsoleteWaiter(ctx context.Context, bc beads.Client, dir, projectName, gateID, root, w string) (present bool, err error) {
	if w == gateID {
		return false, fmt.Errorf("%s cannot be its own waiter", w)
	}
	b, err := beads.FreshShow(ctx, bc, dir, w)
	if err != nil {
		return false, fmt.Errorf("could not read %s (%v); nothing written, retry in a minute", w, err)
	}
	if b == nil {
		return false, fmt.Errorf("no bead %s; nothing written", w)
	}
	if isGateBead(*b) || beads.IsWorkstream(b.Labels) {
		return false, fmt.Errorf("refused: %s is a gate or track bead, and its edges are the gate chain; unlink removes only a dropped step's wait. Nothing written", w)
	}
	if !beads.Superseded(*b) {
		return false, fmt.Errorf("refused: %s is not superseded (status %s, no superseded/retired label or superseded close reason), so its wait on %s is a live edge and stays. Nothing written. If the step was dropped, close it as superseded first: cmdr internal-close-objective --project %s --bead %s --reason \"superseded by <what>\"", w, b.Status, gateID, projectName, w)
	}
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{w})
	if err != nil {
		return false, fmt.Errorf("could not read the edges of %s (%v); nothing written, retry in a minute", w, err)
	}
	present = slices.ContainsFunc(ds[w].Deps, func(d beads.Dep) bool { return d.Type == "blocks" && d.DependsOnID == gateID })
	wroot, err := wsmgr.StrictRoot(ctx, bc, dir, w)
	if err != nil {
		return false, fmt.Errorf("could not read the workstream of %s (%v); nothing written, retry in a minute", w, err)
	}
	if wroot != root {
		return false, fmt.Errorf("refused: %s is in workstream %s but %s is in %s; unlink only clears an edge inside one workstream. Nothing written", w, wroot, gateID, root)
	}
	plugged, err := workflowtemplate.IsPluggedBlock(ctx, bc, dir, projectName, gateID, w)
	if err != nil {
		return false, fmt.Errorf("could not read %s to check the review block (%v); nothing written, retry in a minute", gateID, err)
	}
	if plugged {
		return false, fmt.Errorf("refused: %s is the review block plugged in front of %s; nothing written", gateID, w)
	}
	return present, nil
}

// gateWorkstream is the workstream root of a gate by real parent edges. A
// legacy parentless stage gate uses its stage (gate-of:) to resolve the
// workstream. New stage gates share the guarded stage’s parent.
func gateWorkstream(ctx context.Context, bc beads.Client, dir string, gate beads.Bead) (string, error) {
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{gate.ID})
	if err != nil {
		return "", err
	}
	if ds[gate.ID].Parent == "" {
		for _, l := range gate.Labels {
			if stage, ok := strings.CutPrefix(l, "gate-of:"); ok && stage != "" {
				return wsmgr.StrictRoot(ctx, bc, dir, beads.UnescapeLabel(stage))
			}
		}
	}
	return wsmgr.StrictRoot(ctx, bc, dir, gate.ID)
}

// gateOwnerRoles are the Managers allowed to act on a gate: the owners over it
// (over its stage for a legacy parentless stage gate), nearest first.
func gateOwnerRoles(ctx context.Context, bc beads.Client, dir string, gate beads.Bead) ([]string, error) {
	ds, err := beads.FreshDetails(ctx, bc, dir, []string{gate.ID})
	if err != nil {
		return nil, err
	}
	if ds[gate.ID].Parent == "" {
		for _, l := range gate.Labels {
			if stage, ok := strings.CutPrefix(l, "gate-of:"); ok && stage != "" {
				return ownerRoles(ctx, bc, dir, beads.UnescapeLabel(stage))
			}
		}
	}
	return ownerRoles(ctx, bc, dir, gate.ID)
}

func statusWord(b beads.Bead) string {
	if beads.Superseded(b) {
		return "superseded"
	}
	return b.Status
}

// unlinkCommand is the exact command the owning Manager runs.
func unlinkCommand(project, gate string, waiters []string, reason string) string {
	return fmt.Sprintf("cmdr internal-gate unlink --project %s --bead %s --waiter %s --reason %q", project, gate, strings.Join(waiters, ","), strings.TrimSpace(reason))
}
