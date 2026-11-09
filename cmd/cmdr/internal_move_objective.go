package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example-user/commander/internal/tui"
)

// runInternalMoveObjective puts any bead at a priority rank (1 = top): a
// workstream among the workstreams, anything else among its siblings. It prints
// the numbered Workstreams list as JSON.
func runInternalMoveObjective(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-move-objective", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "bead id")
	to := fs.Int("to", 0, "priority rank to move to (1 = top)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *project == "" || *bead == "" || *to < 1 {
		emitErr(stderr, "missing required flag(s): --project, --bead, --to (>= 1)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if moved, err := tui.MoveBeadTo(ctx, *project, *bead, *to); err != nil {
		emitErr(stderr, err.Error())
		return 1
	} else if moved {
		rows, err := tui.ListNumberedWorkstreams(ctx)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(rows); err != nil {
			emitErr(stderr, fmt.Sprintf("encode result: %v", err))
			return 1
		}
		return 0
	}
	rows, err := tui.MoveWorkstreamTo(ctx, *project, *bead, *to)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	// Keep Manager window names ("#<rank> <title>") in line with the new ranks.
	if opener, cleanup, err := newWorkstreamOpener(); err == nil {
		_ = opener.SyncNames(ctx)
		cleanup()
	}
	if err := json.NewEncoder(stdout).Encode(rows); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}
