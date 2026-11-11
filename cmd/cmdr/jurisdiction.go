package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// Jurisdiction commands do not start a model or a chat process. They record
// planning authority; the thread/runtime adapter consumes that same identity.
func runJurisdiction(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "inventory" || args[0] == "consolidate") {
		return runJurisdictionConsolidation(args, stdout, stderr)
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cmdr jurisdiction status|register|transfer --project-id ID --bead ID; cmdr jurisdiction inventory --project-id ID; cmdr jurisdiction consolidate --file PATH")
		return 2
	}
	fs := flag.NewFlagSet("jurisdiction", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Int64("project-id", 0, "project id")
	bead := fs.String("bead", "", "scope root")
	expected := fs.String("expected-owner", "", "current Commander")
	revision := fs.Int64("revision", 0, "expected jurisdiction revision")
	next := fs.String("commander", "", "Commander conversation id")
	title := fs.String("title", "", "conversation title")
	reason := fs.String("reason", "", "reason for transfer")
	inherit := fs.Bool("inherit", false, "return to inherited ownership")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return 2
	}
	switch args[0] {
	case "register":
		if *next == "" || *title == "" {
			return 2
		}
	case "status", "transfer":
		if *pid <= 0 || *bead == "" {
			return 2
		}
	default:
		return 2
	}
	if args[0] != "status" && beads.HasExecutionAttribution(os.Environ()) {
		fmt.Fprintln(stderr, "execution sessions cannot change planning jurisdiction")
		return 1
	}
	actor := os.Getenv("COMMANDER_ROLE")
	if args[0] == "register" && (actor != "" || os.Getenv("COMMANDER_MANAGER_ROLE") != "") {
		fmt.Fprintln(stderr, "creating a Commander conversation requires the user interface")
		return 1
	}
	// Old host executors may carry only the inherited Manager role. They are
	// never treated as an interactive user by absence of COMMANDER_ROLE.
	if args[0] == "transfer" && actor == "" && os.Getenv("COMMANDER_MANAGER_ROLE") != "" {
		fmt.Fprintln(stderr, "executor cannot transfer planning jurisdiction")
		return 1
	}
	path, e := stateDBPath()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	st, e := store.Open(path)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result any
	switch args[0] {
	case "register":
		e = st.RegisterCommander(ctx, *next, *title)
		result = map[string]string{"commander_id": *next, "title": *title}
	case "status":
		result, e = st.Jurisdiction(ctx, *pid, *bead)
	case "transfer":
		result, e = st.TransferJurisdiction(ctx, store.JurisdictionTransfer{ProjectID: *pid, BeadID: *bead, Actor: actor, ExpectedOwner: *expected, ExpectedRevision: *revision, NextCommander: *next, Inherit: *inherit, Reason: *reason})
	}
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = json.NewEncoder(stdout).Encode(result); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	return 0
}
