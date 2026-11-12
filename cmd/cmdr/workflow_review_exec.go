package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/reviewrun"
)

// runWorkflowReviewExec executes one bounded review with original provenance.
func runWorkflowReviewExec(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow review-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	req := reviewrun.Request{}
	fs.StringVar(&req.Repo, "repo", ".", "review checkout")
	fs.StringVar(&req.Base, "base", "", "actual reviewed base commit")
	fs.StringVar(&req.Task, "task", "", "review task ID")
	fs.Int64Var(&req.RunID, "run-id", 0, "actual task-bound agent_runs ID")
	fs.StringVar(&req.Actor, "actor", os.Getenv("COMMANDER_ROLE"), "actual Operator role")
	fs.StringVar(&req.PromptFile, "prompt-file", "", "original prompt file")
	fs.StringVar(&req.Dir, "run-dir", "", "new absolute private run directory")
	fs.BoolVar(&req.Full, "full", false, "explicit full-depth override")
	fs.Func("context-file", "exact required context file (repeatable; snapshot only, no action authority)", func(path string) error { req.ContextFiles = append(req.ContextFiles, path); return nil })
	fs.BoolVar(&req.PrivateGit, "private-git", false, "stage private Git object and temporary directories (sandbox permissions unchanged)")
	budget := fs.Duration("timeout", 10*time.Minute, "bounded execution time (max30m)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *budget <= 0 || *budget > 30*time.Minute {
		fmt.Fprintln(stderr, "review timeout must be greater than zero and at most30m")
		return 2
	}
	if !strings.HasPrefix(req.Actor, "operator-") {
		fmt.Fprintln(stderr, "an actual review Operator role is required")
		return 2
	}
	req.Command = fs.Args()
	req.Registry = filepath.Join(agentrt.CommanderHome(), "review-executions")
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	result, err := reviewrun.Run(ctx, req)
	if result.ManifestHash != "" {
		_ = json.NewEncoder(stdout).Encode(result)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
