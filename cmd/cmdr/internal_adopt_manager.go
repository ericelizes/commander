package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// workstreamAdopter is the slice of *wsmgr.Manager adoption uses.
type workstreamAdopter interface {
	Adopt(ctx context.Context, projectName, workstreamBeadID, windowName string) (*wsmgr.AdoptResult, error)
}

var newWorkstreamAdopter = func() (workstreamAdopter, func(), error) {
	statePath, err := stateDBPath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve state dir: %w", err)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	reg, err := agentmail.NewServerClientFromEnv()
	if err != nil {
		_ = st.Close()
		return nil, nil, fmt.Errorf("agent-mail client: %w", err)
	}
	return &wsmgr.Manager{Store: st, Beads: beads.New(), Reg: reg, AmKey: resolveCommanderRepo(), Dir: layout.NewDirector()},
		func() { _ = st.Close() }, nil
}

// runInternalAdoptManager turns a running per-project Manager into the Manager
// of one workstream without restarting it: --window is its current tmux window
// name (e.g. "manager (example-app)").
func runInternalAdoptManager(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-adopt-manager", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "workstream bead id to adopt the Manager into")
	window := fs.String("window", "", "the Manager's current tmux window name")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *project == "" || *bead == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	adopter, cleanup, err := newWorkstreamAdopter()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := adopter.Adopt(ctx, *project, *bead, *window)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}

	// Tell the live Manager once what it owns now and how to be reached.
	subject := fmt.Sprintf("You now own workstream %q", res.Title)
	body := fmt.Sprintf("You are now the Manager of ONE workstream: %q (bead %s, rank #%d) in project %s.\n\n"+
		"Your mail role is now %s (it replaces %s, which no longer resolves; same inbox). "+
		"Use --from %s and `cmdr internal-inbox --agent %s`.\n"+
		"Other workstreams in this project have their own Managers; change only beads under %s.",
		res.Title, res.BeadID, res.Rank, res.Project, res.Role, res.OldRole, res.Role, res.Role, res.BeadID)
	if st, client, key, mailCleanup, err := mailDeps(); err != nil {
		fmt.Fprintf(stderr, "warning: adopted, but could not mail the Manager: %v\n", err)
	} else {
		if err := sendRoleMail(ctx, st, client, key, "commander", []string{res.Role}, subject, body, agentmail.SendOptions{}); err != nil {
			fmt.Fprintf(stderr, "warning: adopted, but could not mail the Manager: %v\n", err)
		}
		mailCleanup()
	}

	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}
