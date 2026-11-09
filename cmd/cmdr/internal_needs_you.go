package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/tui"
)

// runInternalNeedsYou prints every open step waiting on the user (the ⚑ items) as
// JSON so Commander can keep pending sign-offs in front of him. With --bead it
// narrows to one step, and with --show it prints that step as plain text (the
// inspector pins this as a command tab when the user opens a ⚑ from the sidebar).
func runInternalNeedsYou(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-needs-you", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bead := fs.String("bead", "", "only this step")
	project := fs.String("project", "", "with --bead: read only this project (default: the bead id's project)")
	show := fs.Bool("show", false, "print the step as readable text (needs --bead)")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// One bead is asked about right after its card was stored: cmdrd's snapshot
	// lags that, so read br and state.db directly.
	list := tui.ListNeedsYou
	if *bead != "" {
		list = func(ctx context.Context) ([]tui.NeedsYou, error) {
			return tui.ListNeedsYouForBead(ctx, *project, *bead)
		}
	}
	rows, err := list(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if *bead != "" {
		var only []tui.NeedsYou
		for _, r := range rows {
			if r.BeadID == *bead {
				only = append(only, r)
			}
		}
		rows = only
	}
	if *show {
		if *bead == "" {
			emitErr(stderr, "--show needs --bead")
			return 1
		}
		writeNeedsYouText(stdout, *bead, rows)
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(rows); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// writeNeedsYouText renders one flagged step for a person: the ask, where it
// sits, the full note, and how to answer.
func writeNeedsYouText(w io.Writer, bead string, rows []tui.NeedsYou) {
	if len(rows) == 0 {
		fmt.Fprintf(w, "%s no longer needs you.\n", bead)
		return
	}
	r := rows[0]
	ask := r.Ask
	if ask == "" {
		ask = r.Title
	}
	fmt.Fprintf(w, "⚑ %s\n\n", ask)
	fmt.Fprintf(w, "workstream #%d  %s\nstep %s  %s\n\n", r.Rank, r.Workstream, r.BeadID, r.Title)
	if strings.TrimSpace(r.Note) != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(r.Note))
	}
	fmt.Fprintln(w, "In the sidebar: a = approve (marks it done), d = defer. Or just tell this Manager.")
}
