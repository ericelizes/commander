package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/store"
)

// runProject is `cmdr project set-repo <name> <path>`: register the git
// checkout a project's Operator worktrees come from, for projects whose path
// (the beads root) is not itself a repo. `--clear` in place of <path> unsets it.
func runProject(args []string, stdout, stderr *os.File) int {
	if len(args) != 3 || args[0] != "set-repo" {
		emitErr(stderr, "usage: cmdr project set-repo <name> <path|--clear>")
		return 1
	}
	statePath, err := stateDBPath()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	st, err := store.Open(statePath)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open state db: %v", err))
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := setProjectRepo(ctx, st, args[1], args[2]); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "project %s repo_path set to %q\n", args[1], repoPathOrEmpty(args[2]))
	return 0
}

func repoPathOrEmpty(arg string) string {
	if arg == "--clear" {
		return ""
	}
	return arg
}

func setProjectRepo(ctx context.Context, st *store.Store, name, pathArg string) error {
	repo := ""
	if pathArg != "--clear" {
		abs, err := filepath.Abs(pathArg)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
			return fmt.Errorf("%s is not a git repo (.git missing): clone the code there first", abs)
		}
		repo = abs
	}
	return st.SetProjectRepoPath(ctx, name, repo)
}
