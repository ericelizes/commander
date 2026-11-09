package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/example-user/commander/internal/daemon"
	"github.com/example-user/commander/internal/store"
)

type checkinStore interface {
	Checkins(ctx context.Context) (map[string]store.Checkin, error)
	SetCheckin(ctx context.Context, role string, c store.Checkin) error
}

// runInternalCheckin sets when a Manager next checks in on its running
// Operators, or Commander on the workstreams (cmdrd's minute watch delivers
// it): --in 1m..30m for a Manager, 5m..60m for Commander.
func runInternalCheckin(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-checkin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "the Manager's role (manager-<workstream bead>)")
	in := fs.Duration("in", 0, fmt.Sprintf("how soon: 1m..%s for a Manager, %s..%s for Commander", daemon.CheckinMax, daemon.CommanderCheckinMin, daemon.CommanderCheckinMax))
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *agent == "" {
		emitErr(stderr, "missing required flag: --agent")
		return 1
	}
	statePath, err := stateDBPath()
	if err != nil {
		emitErr(stderr, fmt.Sprintf("resolve state dir: %v", err))
		return 1
	}
	st, err := store.Open(statePath)
	if err != nil {
		emitErr(stderr, fmt.Sprintf("open store: %v", err))
		return 1
	}
	defer st.Close()
	due, err := setCheckin(context.Background(), st, *agent, *in, time.Now())
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"agent": *agent, "due": due.Format(time.RFC3339)})
	return 0
}

func setCheckin(ctx context.Context, st checkinStore, role string, in time.Duration, now time.Time) (time.Time, error) {
	lo, hi, def := time.Minute, daemon.CheckinMax, daemon.CheckinDefault
	if role == "commander" {
		lo, hi, def = daemon.CommanderCheckinMin, daemon.CommanderCheckinMax, daemon.CommanderCheckinDefault
	}
	if in == 0 {
		in = def
	}
	if in < lo || in > hi {
		return time.Time{}, fmt.Errorf("--in must be between %s and %s", lo, hi)
	}
	cs, err := st.Checkins(ctx)
	if err != nil {
		return time.Time{}, err
	}
	c := cs[role]
	due := now.Add(in)
	c.Due = due.Unix()
	return due, st.SetCheckin(ctx, role, c)
}
