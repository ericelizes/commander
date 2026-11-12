package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/store"
)

// runWorkflowOwnRun has no caller-supplied run/actor/session overrides. The
// immutable launcher environment is required; no newest-run guessing is allowed.
func runWorkflowOwnRun(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("workflow own-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "registered project name")
	root := fs.String("project-dir", "", "registered project root (absolute)")
	task := fs.String("task", "", "own task ID")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	id, err := strconv.ParseInt(os.Getenv("COMMANDER_RUN_ID"), 10, 64)
	cwd, cwdErr := os.Getwd()
	want := store.OwnRunClaim{RunID: id, Task: *task, Actor: os.Getenv("COMMANDER_ROLE"), Session: os.Getenv("COMMANDER_RUN_SESSION"), Project: *project, ProjectDir: *root, Worktree: cwd}
	if err != nil || cwdErr != nil || id <= 0 || want.Actor == "" || want.Session == "" || *project == "" || *task == "" || !filepath.IsAbs(*root) || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "own-run requires immutable launcher identity, --project, --project-dir and --task")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := store.OpenOwnRunReader(ctx, filepath.Join(agentrt.CommanderHome(), "state.db"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer st.Close()
	claim, err := st.OwnRun(ctx, want)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(claim); err != nil {
		return 1
	}
	return 0
}
