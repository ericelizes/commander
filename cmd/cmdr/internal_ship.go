package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/ship"
	"github.com/example-user/commander/internal/slot"
	"github.com/example-user/commander/internal/store"
)

// runInternalShip is `cmdr internal-ship <branch> [--project p] [--dry-run]`:
// ff-only merge of a branch into main of the project. --dry-run does all of it
// but the merge. It never installs.
func runInternalShip(args []string, stdout, stderr *os.File) int {
	var branch string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		branch, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("internal-ship", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug (default: the current directory's repo)")
	dryRun := fs.Bool("dry-run", false, "the same refusals, queue, slot, lock and checks as a ship, with no merge: says what it would have done")
	err := fs.Parse(args)
	if err == nil && branch == "" && fs.NArg() > 0 {
		// The parser stops at the first positional; flags after it still count.
		branch = fs.Arg(0)
		err = fs.Parse(fs.Args()[1:])
	}
	if err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	// A stray word would hide any flag behind it: a --dry-run read as an argument
	// must never become a real merge.
	if branch == "" || fs.NArg() > 0 {
		emitErr(stderr, "usage: cmdr internal-ship <branch> [--project <p>] [--dry-run]")
		return 1
	}
	root, err := os.Getwd()
	if *projectName != "" {
		root, err = resolveProjectPath(*projectName)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	// The checks run in process groups of their own, out of reach of a signal
	// to this one: a stopped ship has to end them itself.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	exclusive := func(ctx context.Context, out io.Writer) (func(), error) {
		return ship.HoldVerifyLock(ctx, commanderHomeDir(), root, out, holdBuildSlot)
	}
	if err := ship.Run(ctx, ship.Options{Root: root, Branch: branch, Exclusive: exclusive, MaxThreads: shipMaxThreads(), Hold: shipHoldCheck(root, branch), DryRun: *dryRun}, stdout); err != nil {
		label := "ship: "
		if *dryRun {
			label = dryRunLabel(err)
		}
		emitErr(stderr, label+err.Error())
		return 1
	}
	if !*dryRun {
		recordShipped(ctx, root, branch)
	}
	return 0
}

// dryRunLabel opens a failed dry run's line: only a refusal is a verdict on the branch.
func dryRunLabel(err error) string {
	var inconclusive ship.Inconclusive
	if errors.As(err, &inconclusive) {
		return "ship dry run, no verdict on the branch: "
	}
	return "ship dry run, a real ship would refuse: "
}

// recordShipped notes that the Operator whose worktree has branch checked out
// passed the ship check (for `cmdr runtime report`). Best effort.
func recordShipped(ctx context.Context, root, branch string) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return
	}
	agent := shippedAgent(string(out), branch)
	if agent == "" {
		return
	}
	path, err := stateDBPath()
	if err != nil {
		return
	}
	st, err := store.Open(path)
	if err != nil {
		return
	}
	defer st.Close()
	run, err := st.GetAgentRunByAgentName(ctx, agent)
	if err != nil || run == nil {
		return
	}
	_, _ = st.InsertCostEvent(ctx, store.CostEvent{ProjectID: run.ProjectID, AgentName: agent,
		TaskBeadID: sql.NullString{String: run.TaskBeadID, Valid: true}, EventType: store.EventOperatorShipped,
		Reason: sql.NullString{String: branch, Valid: true}, OccurredAt: time.Now().Unix()})
}

// shippedAgent is the Operator role whose worktree (under .worktrees/) has
// branch checked out, from `git worktree list --porcelain`; "" when none.
func shippedAgent(porcelain, branch string) string {
	var dir string
	for _, line := range strings.Split(porcelain, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			dir = p
		}
		if line == "branch refs/heads/"+branch && isWorktreesDir(filepath.Base(filepath.Dir(dir))) {
			return "operator-" + filepath.Base(dir)
		}
	}
	return ""
}

// isWorktreesDir is the Operators' worktree folder: .worktrees, or its real
// name .worktrees.noindex when .worktrees is a link to it (Spotlight skips a
// .noindex folder, and git records a worktree by its real path).
func isWorktreesDir(name string) bool {
	return name == ".worktrees" || name == ".worktrees.noindex"
}

// holdBuildSlot waits for the machine-wide build slot for ship's verify.
func holdBuildSlot(ctx context.Context, out io.Writer) (func(), error) {
	h, err := slot.Acquire(ctx, commanderHomeDir(), slot.Options{Cmd: "cmdr internal-ship verify", Out: out})
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}

// shipHoldCheck refuses a ship while the branch's task bead, or an ancestor up
// to its workstream, carries the hold label. The task comes from the Operator
// run whose worktree has the branch; a branch with no run has no task to hold.
func shipHoldCheck(root, branch string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		task, err := shipTaskBead(ctx, root, branch)
		if err != nil {
			return fmt.Errorf("cannot check whether the task is on hold (treated as held; retry): %w", err)
		}
		if task == "" {
			return nil
		}
		return checkTaskHold(ctx, newBeadsClientFn(), root, task, time.Second)
	}
}

// shipTaskBead is the task bead of the Operator run that owns branch, "" if none.
func shipTaskBead(ctx context.Context, root, branch string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	agent := shippedAgent(string(out), branch)
	if agent == "" {
		return "", nil
	}
	path, err := stateDBPath()
	if err != nil {
		return "", err
	}
	st, err := store.Open(path)
	if err != nil {
		return "", err
	}
	defer st.Close()
	return lookupRunTask(ctx, st.GetAgentRunByAgentName, agent, time.Second)
}

// lookupRunTask finds the task of the named run. A lookup ERROR is retried
// twice, then returned (the ship is refused); a run that is not found is no
// task ("" with no error: unmapped branches still ship).
func lookupRunTask(ctx context.Context, get func(context.Context, string) (*store.AgentRun, error), agent string, pause time.Duration) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(pause)
		}
		run, err := get(ctx, agent)
		if err != nil {
			lastErr = err
			continue
		}
		if run == nil {
			return "", nil
		}
		return run.TaskBeadID, nil
	}
	return "", lastErr
}

// checkTaskHold reads the hold state of task (3 attempts, pause apart); a hold
// that cannot be read is treated as held.
func checkTaskHold(ctx context.Context, bc beads.Client, dir, task string, pause time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(pause)
		}
		hold, err := beads.FindHold(ctx, bc, dir, task)
		if err != nil {
			lastErr = err
			continue
		}
		if hold != nil {
			return fmt.Errorf("%s (task %s, bead %s); clear with cmdr internal-set-objective --no-hold", hold, task, hold.Bead)
		}
		return nil
	}
	return fmt.Errorf("cannot read the hold state of %s (treated as held; retry): %w", task, lastErr)
}

// shipMaxThreads reads [ship] max_threads from config.toml; nil when unset or
// unreadable (the default applies).
func shipMaxThreads() *int {
	cfg, err := config.Load(filepath.Join(commanderHomeDir(), "config.toml"))
	if err != nil || cfg.Ship.MaxThreads == nil {
		return nil
	}
	return cfg.Ship.MaxThreads
}
