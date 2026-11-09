package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/example-user/commander/internal/beads"
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
	if err := bc.SetParent(ctx, dir, beadID, into); err != nil {
		return err
	}
	for _, l := range []string{"kind:objective", beads.WorkstreamLabel} {
		if err := bc.SetLabel(ctx, dir, beadID, l, true); err != nil {
			return err
		}
	}
	return bc.SetLabel(ctx, dir, beadID, beads.TrackLabel, false)
}
