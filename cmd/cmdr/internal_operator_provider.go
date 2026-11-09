package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/claudeacct"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/wsmgr"
)

// operatorProviderDeps is what internal-operator-provider touches; tests fake it.
type operatorProviderDeps struct {
	Runs interface {
		GetAgentRunByAgentName(ctx context.Context, name string) (*store.AgentRun, error)
		GetProjectByID(ctx context.Context, id int64) (*store.Project, error)
		MarkClosed(ctx context.Context, runID int64, status string) error
	}
	Spawner interface {
		Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error)
	}
	Beads beads.Client
	// EndSession ends the Operator's tmux session and waits until it is gone.
	EndSession   func(ctx context.Context, name string) error
	BranchOf     func(worktree string) (string, error)
	DefaultModel string
	Cleanup      func()
}

var operatorProviderDepsFn = realOperatorProviderDeps

// switchedStatus closes the run being replaced, so the reaper does not settle
// it as a died Operator (which would reopen its bead and mail the Manager).
const switchedStatus = "switched"

// runInternalOperatorProvider is `cmdr internal-operator-provider
// <operator-role> --account <a> | --runtime <r>`: respawn a running Operator on
// the same bead, branch and worktree with another Claude account or runtime,
// resuming its session where the new side can. A Manager (COMMANDER_ROLE
// manager-*) may switch only its own workstream's Operators.
func runInternalOperatorProvider(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("internal-operator-provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	account := fs.String("account", "", "Claude account to run on (cmdr account list)")
	runtime := fs.String("runtime", "", "agent CLI: claude or codex")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: cmdr internal-operator-provider <operator-role> [--account <name>] [--runtime claude|codex]")
		return 2
	}
	role := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !strings.HasPrefix(role, "operator-") || !chat.ValidRole(role) {
		fmt.Fprintf(stderr, "internal-operator-provider: %q is not an Operator role\n", role)
		return 2
	}
	if *account == "" && *runtime == "" {
		fmt.Fprintln(stderr, "internal-operator-provider: pass --account and/or --runtime")
		return 2
	}
	if *runtime != "" && !chat.ValidRuntime(*runtime) {
		fmt.Fprintf(stderr, "internal-operator-provider: unknown --runtime %q (claude or codex)\n", *runtime)
		return 2
	}
	home := commanderHomeDir()
	if *account != "" {
		if !claudeacct.Exists(home, *account) {
			fmt.Fprintf(stderr, "internal-operator-provider: no Claude account %q (cmdr account list)\n", *account)
			return 1
		}
	}
	// Before the old run is closed: a refused respawn must leave it running.
	// A named provider is checked here, before any store is opened; CheckUsage
	// below checks the one the respawn resolves to.
	if err := spawn.RequestedUsage(*runtime, *account); err != nil {
		fmt.Fprintf(stderr, "internal-operator-provider: %s: %v; nothing was changed\n", role, err)
		return 1
	}

	deps, err := operatorProviderDepsFn()
	if err != nil {
		fmt.Fprintf(stderr, "internal-operator-provider: %v\n", err)
		return 1
	}
	if deps.Cleanup != nil {
		defer deps.Cleanup()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run, err := deps.Runs.GetAgentRunByAgentName(ctx, role)
	if err != nil || run == nil || run.Status != "running" || !run.TmuxPaneID.Valid {
		fmt.Fprintf(stderr, "internal-operator-provider: %s is not a running host Operator (%v)\n", role, err)
		return 1
	}
	p, err := deps.Runs.GetProjectByID(ctx, run.ProjectID)
	if err != nil || p == nil {
		fmt.Fprintf(stderr, "internal-operator-provider: project of %s: %v\n", role, err)
		return 1
	}
	owner := spawn.ManagerRole(ctx, deps.Beads, p.Path, run.TaskBeadID)
	if caller := os.Getenv("COMMANDER_ROLE"); strings.HasPrefix(caller, "manager-") && caller != owner && !ownerAbove(ctx, deps.Beads, p.Path, run.TaskBeadID, caller) {
		fmt.Fprintf(stderr, "internal-operator-provider: %s belongs to %s, not %s: a Manager switches only its own Operators\n", role, owner, caller)
		return 1
	}
	flat := strings.TrimPrefix(role, "operator-")
	branch := ""
	if p.Type == "code" {
		if branch, err = deps.BranchOf(filepath.Join(p.Path, ".worktrees", flat)); err != nil || branch == "" {
			fmt.Fprintf(stderr, "internal-operator-provider: branch of %s's worktree: %v\n", role, err)
			return 1
		}
		// Refused before the old run is closed and its session ended below.
		if branch == spawn.DetachedHEAD {
			fmt.Fprintf(stderr, "internal-operator-provider: %s's worktree is on a detached HEAD, so there is no branch to respawn it on; nothing was changed\n", role)
			return 1
		}
	}

	// Closing the row would lose the stop: no settle, no mail, the run back up.
	if spawn.StopPending(*run) {
		fmt.Fprintf(stderr, "internal-operator-provider: %s was stopped and cmdrd has not settled it yet: wait for cmdrd's 'Operator stopped' mail, then spawn again\n", role)
		return 1
	}

	// Usage of what the respawn will really run on (the role's pin and account,
	// else its Manager's), checked while the old run still runs.
	if err := spawn.CheckUsage(spawn.SpawnRequest{ProjectPath: p.Path, TaskBeadID: run.TaskBeadID, Name: flat,
		Resume: true, Runtime: *runtime, Account: *account}, role, owner); err != nil {
		fmt.Fprintf(stderr, "internal-operator-provider: %s: %v; nothing was changed\n", role, err)
		return 1
	}

	// Close the old run first, then end its session, then respawn under the same names.
	if err := deps.Runs.MarkClosed(ctx, run.ID, switchedStatus); err != nil {
		fmt.Fprintf(stderr, "internal-operator-provider: %v\n", err)
		return 1
	}
	if err := deps.EndSession(ctx, run.TmuxPaneID.String); err != nil {
		fmt.Fprintf(stderr, "internal-operator-provider: end %s: %v\n", run.TmuxPaneID.String, err)
		return 1
	}
	res, err := deps.Spawner.Spawn(ctx, spawn.SpawnRequest{
		ProjectID: p.ID, ProjectName: p.Name, ProjectPath: p.Path, ProjectType: p.Type, RepoPath: p.RepoPath,
		TaskBeadID: run.TaskBeadID, Branch: branch, Name: flat, Model: deps.DefaultModel, Resume: true,
		Runtime: *runtime, Account: *account,
	})
	if err != nil {
		// The old process is gone: leave the bead open for its Manager, like a death.
		_ = deps.Beads.SetStatus(ctx, p.Path, run.TaskBeadID, "open")
		fmt.Fprintf(stderr, "internal-operator-provider: respawn %s: %v (its bead %s is open again)\n", role, err, run.TaskBeadID)
		return 1
	}
	_ = deps.Beads.SetStatus(ctx, p.Path, run.TaskBeadID, "in_progress")
	_ = deps.Beads.SetLabel(ctx, p.Path, run.TaskBeadID, beads.OwnerLabelPrefix+beads.OwnerLabelName(res.AgentName), true)
	prov := chat.ProviderOf(home, res.AgentName)
	out := map[string]any{"agent": res.AgentName, "task_bead_id": run.TaskBeadID, "branch": branch,
		"runtime": prov.Runtime, "account": prov.Account}
	if res.Moved != "" {
		out["moved"] = res.Moved
	}
	_ = json.NewEncoder(stdout).Encode(out)
	return 0
}

func realOperatorProviderDeps() (*operatorProviderDeps, error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	cfg, err := config.Load(filepath.Join(filepath.Dir(statePath), "config.toml"))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("load config: %w", err)
	}
	server, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("agent-mail client: %w", err)
	}
	repo := resolveCommanderRepo()
	tm := tmuxctl.New()
	return &operatorProviderDeps{
		Runs:    st,
		Spawner: spawn.New(st, opencode.New(), beads.New(), &agentmail.ServerRegistrar{Server: server, Store: st, Key: repo}, docker.New(), tm, repo),
		Beads:   newBeadsClientFn(),
		EndSession: func(ctx context.Context, name string) error {
			if err := tm.KillPane(ctx, name); err != nil {
				return err
			}
			for i := 0; i < 100; i++ {
				if ok, err := tm.SessionExists(ctx, name); err == nil && !ok {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return fmt.Errorf("session %s still running after 10s", name)
		},
		BranchOf: func(wt string) (string, error) {
			out, err := exec.Command("git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD").Output()
			return strings.TrimSpace(string(out)), err
		},
		DefaultModel: cfg.Models.DefaultOperator,
		Cleanup:      func() { _ = st.Close() },
	}, nil
}

// ownerAbove: caller is the Manager of an owner above the task's nearest one (a
// parent coordinating a nested workstream, which may respawn its Operators while
// the nested Manager is down). A failed read answers no.
func ownerAbove(ctx context.Context, bc beads.Client, dir, task, caller string) bool {
	if !beads.NestedManagersOn(dir) {
		return false
	}
	owners, err := wsmgr.OwnerChain(ctx, bc, dir, task)
	if err != nil || len(owners) < 2 {
		return false
	}
	return slices.ContainsFunc(owners[1:], func(o string) bool { return caller == wsmgr.Role(o) })
}
