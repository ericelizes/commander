package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/example-user/commander/internal/brief"
	"github.com/example-user/commander/internal/store"
)

// internal-brief-read is the Brief writer's read-only window on the records:
// `sql "SELECT ..."` (one read query on a read-only connection) or
// `linear <TICKET-ID>` (one ticket, with the saved key).
func runInternalBriefRead(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, `usage: cmdr internal-brief-read sql "SELECT ..." | linear <TICKET-ID>`)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "sql":
		if err := brief.ReadOnlySQL(args[1]); err != nil {
			fmt.Fprintln(stderr, "internal-brief-read:", err)
			return 1
		}
		path, err := stateDBPath()
		if err != nil {
			fmt.Fprintln(stderr, "internal-brief-read: no state store")
			return 1
		}
		st, err := store.OpenOwnRunReader(ctx, path)
		if err != nil {
			fmt.Fprintln(stderr, "internal-brief-read:", err)
			return 1
		}
		defer st.Close()
		if err := brief.QueryRows(ctx, st.DB(), args[1], stdout); err != nil {
			fmt.Fprintln(stderr, "internal-brief-read:", err)
			return 1
		}
	case "linear":
		text, err := brief.ReadIssue(ctx, commanderHomeDir(), args[1], nil, "")
		if err != nil {
			fmt.Fprintln(stderr, "internal-brief-read:", err)
			return 1
		}
		fmt.Fprintln(stdout, text)
	default:
		fmt.Fprintln(stderr, `usage: cmdr internal-brief-read sql "SELECT ..." | linear <TICKET-ID>`)
		return 2
	}
	return 0
}

// internal-brief-guard is the Brief writer's PreToolUse hook: only the
// read-only commands run (brief.GuardCommand, with the hard deny-list) and are
// counted against the budget; the final answer waits for the minimum checks;
// every other tool is refused.
func runInternalBriefGuard(stdin io.Reader, stdout io.Writer) int {
	in := readHookInput(stdin)
	why := "The Brief's writer has only the Bash tool."
	switch in.ToolName {
	case "Bash":
		why = brief.Guard(in.shellCommand(), os.Getenv(brief.RunDirEnv))
	case "StructuredOutput":
		md, _ := in.ToolInput["markdown"].(string)
		why = brief.GuardAnswer(os.Getenv(brief.RunDirEnv), md)
	}
	if why != "" {
		b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": why,
		}})
		_, _ = stdout.Write(append(b, '\n'))
	}
	return 0
}
