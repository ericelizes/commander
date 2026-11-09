package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
)

// runSidebarKey is `cmdr __sidebar-key <n> [--from-pane %id]` (tmux prefix 0-9,
// sidebar clicks) and `cmdr __sidebar-key --sync` (after-select-window hook).
// First press on workstream n expands it in every sidebar; the next one goes
// there. Focus is never taken from the chat.
func runSidebarKey(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("__sidebar-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sync := fs.Bool("sync", false, "point the shared expanded row at the visible window")
	fromPane := fs.String("from-pane", "", "the sidebar pane that was clicked (its chat gets keyboard focus)")
	// flags may come after the number
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	d := newDirectorFn()
	ctx := context.Background()
	if *sync {
		if err := d.SyncExpanded(ctx); err != nil {
			fmt.Fprintf(stderr, "cmdr __sidebar-key: %v\n", err)
			return 1
		}
		return 0
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: cmdr __sidebar-key <n> [--from-pane %id] | --sync")
		return 2
	}
	n, err := strconv.Atoi(pos[0])
	if err != nil || n < 0 {
		fmt.Fprintf(stderr, "cmdr __sidebar-key: bad number %q\n", pos[0])
		return 2
	}
	if _, err := d.SidebarKey(ctx, n, *fromPane); err != nil {
		fmt.Fprintf(stderr, "cmdr __sidebar-key: %v\n", err)
		return 1
	}
	return 0
}
