package main

import (
	"flag"
	"io"
	"os"
	"slices"
	"time"

	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/operatorguard"
)

// internal-operator-guard is the PreToolUse hook (matcher Bash) that
// agentrt.ClaudeHostOperatorSettings registers for Operators (and
// agentrt.CodexHookArgs on Codex). It refuses a
// full build or test suite that is not wrapped in `cmdr internal-slot run --`
// and any root-wide filesystem scan, and logs the refusal. It fails open: any parse or I/O error allows the command.
func runInternalOperatorGuard(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("internal-operator-guard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", "", "this Operator's role")
	placeMode := fs.String("place", "", "read-only: the run is on a paired place (the user's Mac); every tool is judged and the guard fails closed")
	err := fs.Parse(args)
	if *placeMode != "" || slices.Contains(args, "--place") || slices.Contains(args, "-place") {
		return runPlaceGuard(*role, *placeMode, err, stdin, stdout)
	}
	if err != nil || !operatorguard.IsOperatorRole(*role) {
		return 0
	}
	home := commanderHomeDir()
	now := time.Now()
	in := readHookInput(stdin)
	command := in.shellCommand()
	if command == "" {
		if in.ToolName == "" {
			operatorguard.LogError(home, *role, "no Bash command in hook input", now)
		}
		return 0
	}
	v := operatorguard.Classify(command)
	if !v.Deny {
		return 0
	}
	_, _ = stdout.Write(append(operatorguard.HookOutputFor(v), '\n'))
	if v.Beads {
		if err := managerguard.LogAgentWrite(home, *role, command, v.Verb, now); err != nil {
			operatorguard.LogError(home, *role, "refusal log: "+err.Error(), now)
		}
		return 0
	}
	if err := operatorguard.LogRefusal(home, *role, command, v.Why, now); err != nil {
		operatorguard.LogError(home, *role, "refusal log: "+err.Error(), now)
	}
	return 0
}

func runInternalOperatorGuardMain(args []string, stdout *os.File) int {
	return runInternalOperatorGuard(args, os.Stdin, stdout)
}

// runPlaceGuard is the guard on the Mac (registered with no matcher, so it sees
// every tool): read-only mode against the user's place policy. Unlike the host
// guard it fails closed: a bad flag, a missing run directory, an unreadable
// policy or hook input it cannot read refuses.
func runPlaceGuard(role, mode string, parseErr error, stdin io.Reader, stdout io.Writer) int {
	home := commanderHomeDir()
	now := time.Now()
	in := readHookInput(stdin)
	command := in.shellCommand()
	deny := func(why string) int {
		_, _ = stdout.Write(append(operatorguard.PlaceHookOutput(why), '\n'))
		_ = operatorguard.LogPlaceRefusal(home, role, in.ToolName, command, why, now)
		return 0
	}
	switch {
	case parseErr != nil || mode != "read-only":
		return deny("the place guard runs only as --place read-only")
	case !operatorguard.IsOperatorRole(role):
		return deny("no Operator role on this place run")
	case in.ToolName == "":
		return deny("no tool in the hook input")
	}
	userHome, _ := os.UserHomeDir()
	run := operatorguard.PlaceRun{Dir: os.Getenv("COMMANDER_PLACE_DIR"), ProdGate: os.Getenv("COMMANDER_PROD_GATE") != "", Home: userHome}
	if run.Dir == "" || userHome == "" {
		return deny("no run directory (COMMANDER_PLACE_DIR) or home")
	}
	policy, perr := operatorguard.LoadPlacePolicy(home, userHome)
	v := operatorguard.ClassifyPlaceTool(in.ToolName, in.ToolInput, command, policy, perr, run)
	if !v.Deny {
		return 0
	}
	return deny(v.Why)
}
