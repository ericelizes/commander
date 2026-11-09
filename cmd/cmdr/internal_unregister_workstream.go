package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// Default is diagnostic only. --confirm-root binds the explicit write to the
// freshly verified owner, protecting against an accidentally selected bead.
func runInternalUnregisterWorkstream(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-unregister-workstream", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "exact non-root registration")
	confirm := fs.String("confirm-root", "", "apply only if this is the fresh owning root")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *project == "" || *bead == "" || fs.NArg() != 0 {
		emitErr(stderr, "required: --project P --bead B [--confirm-root R]")
		return 1
	}
	// Operators may not inspect or change registrations through this command.
	if callerIsOperator(os.Getenv("COMMANDER_ROLE")) {
		emitErr(stderr, "Operators cannot unregister workstream Managers")
		return 1
	}
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := st.GetProjectByName(ctx, *project)
	if err != nil || p == nil {
		emitErr(stderr, fmt.Sprintf("project %q unavailable: %v", *project, err))
		return 1
	}
	// "root" is the owner: the nearest open workstream (the root while the
	// project's nested Managers are off). Any owner above it may confirm too.
	owners, err := wsmgr.OwnerChain(ctx, newBeadsClientFn(), p.Path, *bead)
	root := ""
	if err == nil {
		root = owners[0]
		err = authorizeUnregister(os.Getenv("COMMANDER_ROLE"), *bead, owners, *confirm)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	row, err := st.GetWorkstreamManager(ctx, p.ID, *bead)
	if err == nil && row != nil && row.AmRole != store.WorkstreamManagerRole(*bead) {
		err = fmt.Errorf("registration has unexpected role %q; refusing", row.AmRole)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	removed := false
	if *confirm != "" {
		actor := os.Getenv("COMMANDER_ROLE")
		if actor == "" {
			actor = "user"
		}
		removed, err = st.UnregisterNestedManager(ctx, p.ID, *bead, root, actor)
		if err != nil {
			emitErr(stderr, err.Error())
			return 1
		}
	}
	err = json.NewEncoder(stdout).Encode(map[string]any{"project": *project, "bead_id": *bead, "root": root, "manager_role": store.WorkstreamManagerRole(*bead), "registered": row != nil, "dry_run": *confirm == "", "removed": removed})
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}

func authorizeUnregister(role, bead string, owners []string, confirm string) error {
	root := ""
	if len(owners) > 0 {
		root = owners[0]
	}
	if root == "" || bead == root {
		return fmt.Errorf("%s is a root or an open nested workstream (it owns its work); refusing unregister", bead)
	}
	if role != "" && role != "commander" && !slices.ContainsFunc(owners, func(o string) bool { return role == wsmgr.Role(o) }) {
		return fmt.Errorf("only the owning Manager, Commander or user may unregister %s", bead)
	}
	if confirm != "" && confirm != root {
		return fmt.Errorf("--confirm-root must match fresh root %s", root)
	}
	return nil
}
