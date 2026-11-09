package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"github.com/example-user/commander/internal/reviewpolicy"
)

func runWorkflowReviewPolicy(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow review-policy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "local git checkout")
	base := fs.String("base", "", "base commit or ref")
	head := fs.String("head", "HEAD", "head commit or ref")
	full := fs.Bool("full", false, "explicit full review override")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 || *base == "" {
		emitErr(stderr, "usage: workflow review-policy --repo PATH --base REF [--head REF] [--full]")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := reviewpolicy.Inspect(ctx, *repo, *base, *head, *full)
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
