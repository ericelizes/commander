package main

import (
	"flag"
	"io"
	"os"
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
	if err := fs.Parse(args); err != nil || !operatorguard.IsOperatorRole(*role) {
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
