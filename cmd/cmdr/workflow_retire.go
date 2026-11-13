package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/example-user/commander/internal/beads"
)

func runWorkflowRetireBlock(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow retire-block", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug")
	block := fs.String("block", "", "plugged pr-review block")
	reason := fs.String("reason", "", "why the block is superseded or withdrawn")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *project == "" || *block == "" || *reason == "" || fs.NArg() != 0 {
		emitErr(stderr, "required: --project P --block B --reason R")
		return 1
	}
	p, err := resolveProjectRecFn(*project)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	res, err := beads.Retire(ctx, p.Path, *block, "workflow-block", *reason, false)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if res.Exit != 0 || res.Error != "" {
		emitErr(stderr, res.Error+res.Stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(res)
	return 0
}
