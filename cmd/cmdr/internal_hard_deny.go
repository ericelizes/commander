package main

import (
	"context"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/example-user/commander/internal/harddeny"
	"github.com/example-user/commander/internal/operatorguard"
	"github.com/example-user/commander/internal/store"
)

// internal-hard-deny is the Commander's PreToolUse hook on Bash: it refuses
// the commands on the hard deny-list (internal/harddeny) outright, so nothing
// is ever left waiting on an approval prompt. Operators get the same check
// inside internal-operator-guard.
func runInternalHardDeny(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("internal-hard-deny", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", "", "the chat's role")
	if fs.Parse(args) != nil {
		return 0
	}
	in := readHookInput(stdin)
	command := in.shellCommand()
	if command == "" {
		return 0
	}
	if v := hardDenyVerdict(*role, command, in.Cwd); v.Denied() {
		_, _ = stdout.Write(append(harddeny.HookOutput(v), '\n'))
		_ = operatorguard.LogRefusal(commanderHomeDir(), *role, command, "hard limit "+v.Rule+": "+v.Why, time.Now())
	}
	return 0
}

// hardDenyVerdict checks command for role. An Operator's own worktree (its
// cwd) is where it may delete freely; the deploy rule lifts only inside an
// approved release-deploy step, and the mainline-push rule only inside a
// release's land-on-mainline step.
func hardDenyVerdict(role, command, cwd string) harddeny.Verdict {
	ctx := harddeny.Context{DeployAllowed: true, MainlineAllowed: true}
	if operatorguard.IsOperatorRole(role) {
		ctx.Worktree = cwd
	}
	if v := harddeny.Check(command, ctx); v.Denied() {
		return v
	}
	ctx.DeployAllowed, ctx.MainlineAllowed = false, false
	v := harddeny.Check(command, ctx)
	// Each step-scoped rule lifts once, when the step carries its label; one
	// command may hit both, so check again after each lift.
	for v.Denied() {
		switch {
		case v.Rule == "prod-deploy" && !ctx.DeployAllowed && stepHasLabel(role, harddeny.ReleaseDeployLabel):
			ctx.DeployAllowed = true
		case v.Rule == "mainline-push" && !ctx.MainlineAllowed && stepHasLabel(role, harddeny.ReleaseLandLabel):
			ctx.MainlineAllowed = true
		default:
			return v
		}
		v = harddeny.Check(command, ctx)
	}
	return v
}

// stepHasLabel reports whether role is an Operator whose current step (or a
// step above it) carries label. Any error is no.
func stepHasLabel(role, label string) bool {
	if !operatorguard.IsOperatorRole(role) {
		return false
	}
	path, err := stateDBPath()
	if err != nil {
		return false
	}
	st, err := store.Open(path)
	if err != nil {
		return false
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := st.GetAgentRunByAgentName(ctx, role)
	if err != nil || run == nil || run.TaskBeadID == "" {
		return false
	}
	for id := run.TaskBeadID; id != ""; {
		if ok, err := st.BeadHasLabel(ctx, run.ProjectID, id, label); err == nil && ok {
			return true
		}
		i := strings.LastIndexByte(id, '.')
		if i < 0 {
			break
		}
		id = id[:i]
	}
	return false
}

func runInternalHardDenyMain(args []string, stdout *os.File) int {
	return runInternalHardDeny(args, os.Stdin, stdout)
}
