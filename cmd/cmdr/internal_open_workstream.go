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

// workstreamOpener is the slice of *wsmgr.Manager the CLI uses; tests replace
// newWorkstreamOpener.
type workstreamOpener interface {
	Open(ctx context.Context, projectName, beadID string, opt wsmgr.OpenOptions) (*wsmgr.OpenResult, error)
	OpenProject(ctx context.Context, projectName string) (*wsmgr.OpenResult, error)
	PrepareAll(ctx context.Context) ([]layout.WorkstreamWindow, error)
	Prepare(ctx context.Context, t wsmgr.Target) (layout.WorkstreamWindow, error)
	SyncNames(ctx context.Context) error
}

var newWorkstreamOpener = realWorkstreamOpener

func realWorkstreamOpener() (workstreamOpener, func(), error) {
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
	return &wsmgr.Manager{
		Store: st, Beads: beads.New(), Reg: reg, AmKey: resolveCommanderRepo(),
		Dir: layout.NewDirector(), Settle: 6 * time.Second,
		Native: cmdrdChats{home: appCommanderHome()},
	}, func() { _ = st.Close() }, nil
}

// workstreamMode is on in the real binary (not in tests): per-project Manager
// windows no longer exist, so `cmdr --project X` focuses X's top workstream.
var workstreamMode bool

type internalOpenWorkstreamResult = wsmgr.OpenResult

// runInternalOpenWorkstream opens (creating or resuming) the Manager of the
// workstream containing --bead. With --step it also points the Manager at that
// step; with --focus it selects the Manager's window.
func runInternalOpenWorkstream(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-open-workstream", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	project := fs.String("project", "", "project slug")
	bead := fs.String("bead", "", "workstream bead id (or any step under it)")
	step := fs.String("step", "", "step to point the Manager at")
	focus := fs.Bool("focus", false, "select the Manager's window")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *project == "" || *bead == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	opener, cleanup, err := newWorkstreamOpener()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := opener.Open(ctx, *project, *bead, wsmgr.OpenOptions{Focus: *focus, StepID: *step})
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		emitErr(stderr, fmt.Sprintf("encode result: %v", err))
		return 1
	}
	return 0
}

// installWorkstreamManagerHook makes cmdr start/restart bring back a Manager
// window for every workstream. Called from main only, so tests never hit it.
func installWorkstreamManagerHook() {
	workstreamMode = true
	layout.SetWorkstreamWindowsFn(func(ctx context.Context) ([]layout.WorkstreamWindow, error) {
		opener, cleanup, err := newWorkstreamOpener()
		if err != nil {
			return nil, err
		}
		defer cleanup()
		return opener.PrepareAll(ctx)
	})
}
