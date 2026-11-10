package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/wsmgr"
)

type internalTrackResult struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Into    string `json:"into"`
	Track   bool   `json:"track"`
}

// runInternalTrack: `internal-track convert --project p --bead B --into W`
// moves bead B under workstream W as a nested workstream (parent edge + kind:workstream). The
// dotted id cannot change. Regrouping real beads is the Managers' and
// Commander's call; this only does the mechanics.
func runInternalTrack(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 || args[0] != "convert" {
		emitErr(stderr, "usage: internal-track convert --project <slug> --bead <id> --into <workstream bead>")
		return 1
	}
	fs := flag.NewFlagSet("internal-track convert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	beadID := fs.String("bead", "", "the bead that becomes a nested workstream")
	into := fs.String("into", "", "the workstream bead it moves under")
	if err := fs.Parse(args[1:]); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *beadID == "" || *into == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead, --into")
		return 1
	}
	if *beadID == *into {
		emitErr(stderr, "--bead and --into are the same bead")
		return 1
	}
	return editObjective(*projectName, stdout, stderr, func(ctx context.Context, dir string) (internalObjectiveEditResult, error) {
		bc := newBeadsClientFn()
		if err := convertToTrack(ctx, bc, dir, *beadID, *into); err != nil {
			return internalObjectiveEditResult{}, err
		}
		startNestedManager(ctx, bc, *projectName, dir, *beadID, func(w string) { fmt.Fprintln(stderr, "warning: "+w) })
		on := true
		return internalObjectiveEditResult{BeadID: *beadID, Track: &on}, nil
	})
}

// convertToTrack checks the bead may sit under the target and is not above it,
// then reparents it and labels it a workstream.
func convertToTrack(ctx context.Context, bc beads.Client, dir, beadID, into string) error {
	d, err := beads.FreshDetails(ctx, bc, dir, []string{beadID, into})
	if err != nil {
		return err
	}
	if _, ok := d[beadID]; !ok {
		return fmt.Errorf("no bead %s", beadID)
	}
	if _, ok := d[into]; !ok {
		return fmt.Errorf("no bead %s", into)
	}
	if slices.Contains(beads.Lineage(ctx, bc, dir, nil, into), beadID) {
		return fmt.Errorf("%s sits under %s: a bead cannot move under its own descendant", into, beadID)
	}
	if err := beads.CheckPlacement(ctx, bc, dir, into, beads.Placement{Workstream: true, Self: beadID}); err != nil {
		return err
	}
	if err := requireMoveHeld(ctx, bc, dir, beadID, into, "moving "+beadID+" under "+into); err != nil {
		return err
	}
	if err := bc.SetParent(ctx, dir, beadID, into); err != nil {
		return err
	}
	for _, l := range []string{"kind:objective", beads.WorkstreamLabel} {
		if err := bc.SetLabel(ctx, dir, beadID, l, true); err != nil {
			return err
		}
	}
	// wsmgr refuses kind:task targets, so a converted bead must not keep it.
	if err := bc.SetLabel(ctx, dir, beadID, "kind:task", false); err != nil {
		return err
	}
	return bc.SetLabel(ctx, dir, beadID, beads.TrackLabel, false)
}

// requireMoveHeld keeps a move from leaving work with no holder: a parent the move empties (its
// last open child walks away) is an empty scope, so it gets the Manager to plan wait (and its
// Manager's name), written BEFORE the move (make before break), exactly as a newborn scope does.
// Judged on fresh reads; a closure it cannot read is left as is (fail open).
func requireMoveHeld(ctx context.Context, bc beads.Client, dir, bead, parent, verb string) error {
	g, err := loadGraph(ctx, bc, dir, bead)
	if err != nil {
		return nil
	}
	for _, v := range holding.NewlyUnheld(g, bead, map[string]holding.Edit{bead: func(b *holding.Bead) { b.Parent = parent }}) {
		if err := holdWithManager(ctx, bc, dir, v.ID, wsmgr.Role(wsmgr.Owner(ctx, bc, dir, v.ID)), beads.ManagerPlanWait); err != nil {
			return fmt.Errorf("refused: %s would leave %s with no holder and its Manager to plan wait could not be written (%v). Nothing was moved", verb, v.ID, err)
		}
	}
	return nil
}
