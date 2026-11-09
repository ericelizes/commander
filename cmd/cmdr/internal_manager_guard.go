package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/managerguard"
	"github.com/example-user/commander/internal/store"
)

// internal-manager-guard is the PreToolUse hook (matcher Bash; on Codex also
// its file edits) that agentrt.ClaudeMailHookSettings / CodexHookArgs register
// for Manager roles. It reads the tool input JSON on stdin and, when the
// command does the work a Manager should hand to an Operator, prints the deny decision, logs the refusal, marks the role's
// agent_state note and mails the Commander (at most once per role per 30 min).
// It fails open: any parse or I/O error allows the command (and is logged).

func runInternalManagerGuard(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("internal-manager-guard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	role := fs.String("role", "", "this Manager's role")
	if err := fs.Parse(args); err != nil || *role == "" || !managerguard.IsManagerRole(*role) {
		return 0
	}
	home := commanderHomeDir()
	now := time.Now()
	in := readHookInput(stdin)
	command := in.shellCommand()
	var v managerguard.Verdict
	switch {
	case in.isEditTool():
		// Claude never offers these to a Manager (--disallowedTools); Codex
		// has no such flag, so its apply_patch is refused here.
		command = in.toolDetail()
		v = managerguard.Verdict{Deny: true, Why: "file edit (" + in.ToolName + ")"}
	case command == "":
		if in.ToolName == "" {
			managerguard.LogError(home, *role, "no Bash command in hook input", now)
		}
		return 0
	default:
		v = managerguard.Classify(command, in.Cwd)
	}
	if !v.Deny {
		return 0
	}
	out := managerguard.HookOutput()
	waitLabel := strings.HasPrefix(v.Why, managerguard.WaitLabelWhy)
	cockpit := strings.HasPrefix(v.Why, managerguard.CockpitWhy)
	if verb, ok := strings.CutPrefix(v.Why, managerguard.BeadsWriteWhy); ok {
		// A direct bd/br write is not an Operator's work being done: refuse,
		// log to agent-write-refusals.jsonl, no sidebar note, no mail.
		_, _ = stdout.Write(append(managerguard.BeadsWriteHookOutput(verb), '\n'))
		if err := managerguard.LogAgentWrite(home, *role, command, verb, now); err != nil {
			managerguard.LogError(home, *role, "refusal log: "+err.Error(), now)
		}
		return 0
	}
	switch {
	case strings.HasPrefix(v.Why, "kills tmux:"):
		out = managerguard.TmuxHookOutput()
	case waitLabel:
		out = managerguard.WaitLabelHookOutput()
	case cockpit:
		out = managerguard.CockpitHookOutput()
	}
	_, _ = stdout.Write(append(out, '\n'))
	if err := managerguard.LogRefusal(home, *role, command, v.Why, now); err != nil {
		managerguard.LogError(home, *role, "refusal log: "+err.Error(), now)
	}
	if !waitLabel && !cockpit {
		// The note and mail say the Manager tried to do an Operator's work; a
		// hand-written wait or a bare cmdr is not that, and the refusal tells it what to run.
		noteManagerRefusal(home, *role, command, now)
	}
	return 0
}

// noteManagerRefusal sets the sidebar note and mails the Commander; both are
// best effort.
func noteManagerRefusal(home, role, command string, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	if statePath, err := stateDBPath(); err == nil {
		if st, err := store.Open(statePath); err == nil {
			state := store.AgentWorking
			if states, err := st.ListAgentStates(ctx); err == nil {
				for _, a := range states {
					if a.Role == role {
						state = a.State
					}
				}
			}
			_ = st.SetAgentState(ctx, role, state, managerguard.Note)
			_ = st.Close()
		}
	}
	if !managerguard.ShouldMail(home, role, now) {
		return
	}
	st, client, amProjectKey, cleanup, err := mailDeps()
	if err != nil {
		managerguard.LogError(home, role, "mail deps: "+err.Error(), now)
		return
	}
	defer cleanup()
	if err := sendRoleMail(ctx, st, client, amProjectKey, role, []string{"commander"},
		"Manager "+role+" tried to do work: "+oneLineSubject(command),
		"Manager "+role+" tried to run a command that does work an Operator should do, and was refused:\n\n    "+command+"\n\nLog: "+filepath.Join(home, "logs", "manager-refusals.jsonl"),
		agentmail.SendOptions{Importance: "high"}); err != nil {
		managerguard.LogError(home, role, "mail: "+err.Error(), now)
	}
}

func commanderHomeDir() string {
	if home := os.Getenv("COMMANDER_HOME"); home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".commander")
}

func runInternalManagerGuardMain(args []string, stdout *os.File) int {
	return runInternalManagerGuard(args, os.Stdin, stdout)
}
