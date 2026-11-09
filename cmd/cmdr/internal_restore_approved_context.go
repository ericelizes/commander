package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/example-user/commander/internal/approvedcontext"
	"github.com/example-user/commander/internal/store"
)

func runInternalRestoreApprovedContext(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-restore-approved-context", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "closed user gate")
	generation := fs.Int("generation", 0, "exact stored approved pitch generation")
	answer := fs.String("answer-id", "", "exact stored answer ID")
	dry := fs.Bool("dry-run", false, "validate without writing")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *project == "" || *bead == "" || *generation < 1 || *answer == "" || fs.NArg() != 0 {
		emitErr(stderr, "required: --project P --bead B --generation N --answer-id ID [--dry-run]")
		return 1
	}
	path, err := stateDBPath()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	st, err := store.OpenProvenance(path)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	p, err := st.GetProjectByName(ctx, *project)
	if err != nil || p == nil {
		emitErr(stderr, fmt.Sprintf("project unavailable: %v", err))
		return 1
	}
	result, err := approvedcontext.Restore(ctx, st, newBeadsClientFn(), *p, *bead, os.Getenv("COMMANDER_ROLE"), *generation, *answer, *dry)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}
