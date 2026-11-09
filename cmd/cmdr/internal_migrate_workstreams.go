package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/example-user/commander/internal/beads"
)

type migrateWorkstreamsResult struct {
	Project string   `json:"project"`
	Applied bool     `json:"applied"`
	Label   []string `json:"label"`
	// Mixed are open workstreams whose open children are both kinds: its Manager
	// splits each with internal-create-objective --workstream --take-open-steps.
	Mixed []string `json:"mixed,omitempty"`
}

// runInternalMigrateWorkstreams labels a project's open top-level objectives and
// every kind:track bead kind:workstream, and lists what still breaks the shape.
// It is safe to repeat. Without --apply it only reports.
func runInternalMigrateWorkstreams(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-migrate-workstreams", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	apply := fs.Bool("apply", false, "write the labels (without it, only report)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" {
		emitErr(stderr, "missing required flag: --project")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*beadWriteTimeout)
	defer cancel()
	result, err := migrateWorkstreams(ctx, newBeadsClientFn(), project.Path, *apply)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	result.Project = project.Name
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

func migrateWorkstreams(ctx context.Context, bc beads.Client, dir string, apply bool) (migrateWorkstreamsResult, error) {
	result := migrateWorkstreamsResult{Applied: apply}
	all, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return result, err
	}
	ids := make([]string, len(all))
	for i, b := range all {
		ids[i] = b.ID
	}
	details, err := beads.FreshDetails(ctx, bc, dir, ids)
	if err != nil {
		return result, err
	}
	for _, b := range all {
		parent := details[b.ID].Parent
		open := b.Status != "closed" && b.Status != "tombstone"
		var relabel bool
		switch {
		case isGateBead(b) || slices.Contains(b.Labels, "kind:task"):
		case slices.Contains(b.Labels, beads.TrackLabel):
			// A kind:track bead with no parent is a leftover, never a row: relabelled it would become one.
			relabel = parent != ""
		case parent == "":
			relabel = open && !slices.Contains(b.Labels, beads.WorkstreamLabel)
		}
		if relabel {
			result.Label = append(result.Label, b.ID)
			if apply {
				if err := bc.SetLabel(ctx, dir, b.ID, beads.WorkstreamLabel, true); err != nil {
					return result, err
				}
				if err := bc.SetLabel(ctx, dir, b.ID, beads.TrackLabel, false); err != nil {
					return result, err
				}
			}
		}
		if !open || !(beads.IsWorkstream(b.Labels) || parent == "") {
			continue
		}
		children, err := beads.OpenChildren(ctx, bc, dir, b.ID)
		if err != nil {
			return result, err
		}
		// Parked is not open, and is checked when it comes back.
		nested := slices.ContainsFunc(children, func(k beads.Bead) bool { return k.Status != beads.StatusDeferred && beads.IsWorkstream(k.Labels) })
		steps := slices.ContainsFunc(children, func(k beads.Bead) bool { return k.Status != beads.StatusDeferred && !beads.IsWorkstream(k.Labels) })
		if nested && steps {
			result.Mixed = append(result.Mixed, b.ID)
		}
	}
	return result, nil
}
