package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// runPitches lists every saved version of the pitches on a workstream, track or
// step, newest last, with the answer each got. Open one with
// `cmdr tab add --decision <bead> --focus`.
func runPitches(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("pitches", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project name (required)")
	bead := fs.String("bead", "", "a workstream, track or step id; empty lists the whole project")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *project == "" {
		fmt.Fprintln(stderr, "cmdr pitches: --project is required")
		return 2
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		fmt.Fprintf(stderr, "cmdr pitches: %v\n", err)
		return 1
	}
	defer cleanup()
	ctx := context.Background()
	p, err := st.GetProjectByName(ctx, *project)
	if err != nil || p == nil {
		fmt.Fprintf(stderr, "cmdr pitches: unknown project %q\n", *project)
		return 1
	}
	list, err := st.ListPitches(ctx, p.ID, *bead)
	if err != nil {
		fmt.Fprintf(stderr, "cmdr pitches: %v\n", err)
		return 1
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(list)
		return 0
	}
	for _, x := range list {
		answer := "open"
		if x.Answer != "" {
			answer = "answered: " + x.Answer
		}
		fmt.Fprintf(stdout, "%s v%d  %s  %s  (%s)\n", x.BeadID, x.Version, time.Unix(x.CreatedAt, 0).Format("Jan 2 15:04"), x.Title, answer)
	}
	return 0
}
