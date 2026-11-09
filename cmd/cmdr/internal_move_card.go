package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// setObjectiveFn is the verb internal-move-card writes through, so every
// write meets the same door checks as a hand-typed one.
var setObjectiveFn = runInternalSetObjective

// runInternalMoveCard moves the open card an ancestor (track or step) holds
// onto a gate under it: the card is saved to a file, the ancestor goes back to
// in-progress under its workstream's Manager (never ownerless), then the gate
// takes the card. If the gate is refused the ancestor gets its card back.
func runInternalMoveCard(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("internal-move-card", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	from := fs.String("from", "", "ancestor bead holding the open card")
	to := fs.String("to", "", "gate bead that takes the card")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if *projectName == "" || *from == "" || *to == "" {
		emitErr(stderr, "missing required flag(s): --project, --from, --to")
		return 1
	}
	if err := moveCard(*projectName, *from, *to, stdout, stderr); err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}

func moveCard(project, from, to string, stdout, stderr *os.File) error {
	dir, err := resolveProjectPath(project)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bc := freshReader(newBeadsClientFn())
	lineage := beads.Lineage(ctx, bc, dir, nil, to)
	if len(lineage) < 2 {
		return fmt.Errorf("could not read the parents of %s (busy?); nothing was written", to)
	}
	under := false
	for _, a := range lineage[1:] {
		under = under || a == from
	}
	if !under {
		return fmt.Errorf("%s is not under %s; nothing was written", to, from)
	}
	gate, err := bc.Show(ctx, dir, to)
	if err != nil || gate == nil || !isGateBead(*gate) || isClosedBead(*gate) {
		return fmt.Errorf("%s is not an open gate; nothing was written", to)
	}
	snap := boardSnapshot()
	if snap == nil {
		return fmt.Errorf("the board is not readable, so the card on %s cannot be read; retry in a minute. Nothing was written", from)
	}
	var card *store.Decision
	for _, n := range snap.NeedsYou {
		if n.Project == project && n.BeadID == from && n.Decision != nil {
			card = n.Decision
		}
	}
	if card == nil {
		return fmt.Errorf("%s holds no open card on the board; nothing to move. Nothing was written", from)
	}
	cardDir := filepath.Join(commanderHomeDir(), "moved-cards")
	if err := os.MkdirAll(cardDir, 0o700); err != nil {
		return err
	}
	cardFile := filepath.Join(cardDir, from+".json")
	raw, _ := json.Marshal(card)
	if err := os.WriteFile(cardFile, raw, 0o600); err != nil {
		return err
	}
	// Back under its owner: the nearest open workstream's Manager (the root's
	// while nested Managers are off).
	mgr := store.WorkstreamManagerRole(beads.Owner(ctx, bc, dir, nil, nil, from))
	// The ancestor is settled for the move, not worked on: no workflow gate.
	if code := setObjectiveFn([]string{"--project", project, "--bead", from, "--state", beads.StateInProgress, "--owner", mgr, "--writes-workflow"}, stdout, stderr); code != 0 {
		return fmt.Errorf("could not clear the card on %s; nothing moved (the card is saved at %s)", from, cardFile)
	}
	if code := setObjectiveFn([]string{"--project", project, "--bead", to, "--state", beads.StateNeedsUser, "--decision", "@" + cardFile}, stdout, stderr); code != 0 {
		if code := setObjectiveFn([]string{"--project", project, "--bead", from, "--state", beads.StateNeedsUser, "--decision", "@" + cardFile}, stdout, stderr); code != 0 {
			return fmt.Errorf("the gate %s refused the card and %s could not take it back; the card is saved at %s: internal-set-objective --project %s --bead %s --state needs-user --decision @%s", to, from, cardFile, project, from, cardFile)
		}
		return fmt.Errorf("the gate %s refused the card; %s has it again", to, from)
	}
	fmt.Fprintf(stdout, "moved the card from %s to %s (saved at %s)\n", from, to, cardFile)
	return nil
}
