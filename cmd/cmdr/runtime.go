package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/example-user/commander/internal/chat"
)

// runRuntime is `cmdr runtime <role> [claude|codex]`: print or switch the agent
// CLI a chat runs on. A switch goes through cmdrd, which restarts a running
// chat on the new CLI; with cmdrd down it only records the choice, which the
// chat's next start picks up. Either way the choice pins the role
// (chat.RuntimePinned): `cmdr runtime rebalance` never moves it. `cmdr runtime
// report` compares the runtimes.
func runRuntime(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "report" {
		return runRuntimeReport(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "rebalance" {
		return runRuntimeRebalance(args[1:], stdout, stderr)
	}
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(stderr, "usage: cmdr runtime <role> [claude|codex] | cmdr runtime report [--days N] | cmdr runtime rebalance [--dry-run]")
		return 2
	}
	home := commanderHomeDir()
	role := args[0]
	if !chat.ValidRole(role) {
		fmt.Fprintf(stderr, "cmdr runtime: invalid role %q (commander, manager-<bead>, operator-<name>)\n", role)
		return 2
	}
	if len(args) == 1 {
		fmt.Fprintln(stdout, chat.ReadRuntime(home, role))
		return 0
	}
	rt := args[1]
	if !chat.ValidRuntime(rt) {
		fmt.Fprintf(stderr, "cmdr runtime: unknown runtime %q (claude or codex)\n", rt)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := cmdrdChats{home: home}.post(ctx, role, "runtime", map[string]any{"runtime": rt})
	if err == nil {
		fmt.Fprintf(stdout, "%s runs on %s\n", role, rt)
		return 0
	}
	if !cmdrdDown(err) {
		fmt.Fprintf(stderr, "cmdr runtime: %v\n", err)
		return 1
	}
	if err := chat.WriteRuntime(home, role, rt); err != nil {
		fmt.Fprintf(stderr, "cmdr runtime: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s set to %s (cmdrd is not running: takes effect when the chat next starts)\n", role, rt)
	return 0
}

// cmdrdDown reports whether err means cmdrd could not be reached at all.
func cmdrdDown(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) || errors.Is(err, os.ErrNotExist)
}
