package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ownercompat"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// gateConvert is `internal-gate convert --bead S --approver A --criteria C`: a plain step becomes a
// gate bead in place. Its card, owner, waits, edges and comments stay, so no step is left flagged
// (a needs-user step the plan later calls a gate had no supported way to become one). The write
// order never shows a gate with no approver: criteria, approvers, then the gate label.
// Owning Manager, Commander or the user only; Operators are refused.
func gateConvert(ctx context.Context, st *store.Store, projectName, id, approver, criteria string) (any, error) {
	role := os.Getenv("COMMANDER_ROLE")
	if callerIsOperator(role) {
		return nil, errors.New("refused: Operators do not change the gate chain; tell your Manager which step should be a gate (cmdr internal-mail --to <manager> --subject ... --body ...)")
	}
	approvers := strings.FieldsFunc(approver, func(r rune) bool { return r == ',' || r == ' ' })
	if len(approvers) == 0 {
		return nil, errors.New("convert: a gate needs --approver: who approves it (user, a GitHub login, a name)")
	}
	if err := checkGateHasCriteria(criteria); err != nil {
		return nil, err
	}
	p, err := st.GetProjectByName(ctx, projectName)
	if err != nil || p == nil {
		return nil, fmt.Errorf("project %q not added", projectName)
	}
	bc := freshReader(newBeadsClientFn())
	b, err := beads.FreshShow(ctx, bc, p.Path, id)
	if err != nil {
		return nil, fmt.Errorf("could not read %s (%v); nothing written, retry in a minute", id, err)
	}
	if b == nil {
		return nil, fmt.Errorf("no bead %s", id)
	}
	switch {
	case isGateBead(*b):
		return nil, fmt.Errorf("%s is already a gate", id)
	case b.Status == "closed" || b.Status == "tombstone" || isRetiredBead(*b):
		return nil, fmt.Errorf("%s is %s: only an open step can become a gate", id, statusWord(*b))
	case beads.IsWorkstream(b.Labels) || strings.Contains(strings.Join(b.Labels, " "), beads.TrackLabel) || strings.Contains(strings.Join(b.Labels, " "), "kind:operational"):
		return nil, fmt.Errorf("%s is a workstream, track or operational bead, not a plain step: it cannot become a gate", id)
	}
	if err := checkGreenlightIsUsers(b.Title, approvers); err != nil {
		return nil, err
	}
	root, err := wsmgr.StrictOwner(ctx, bc, p.Path, id) // the nearest owning workstream (nested Managers)
	if err != nil {
		return nil, fmt.Errorf("could not read the workstream of %s (%v); nothing written, retry in a minute", id, err)
	}
	if role != "" && role != "commander" && role != wsmgr.Role(root) {
		return nil, fmt.Errorf("refused: %s belongs to workstream %s, whose Manager is %s. Nothing written. Ask it to run:\n  cmdr internal-gate convert --project %s --bead %s --approver %q --criteria %q", id, root, wsmgr.Role(root), projectName, id, approver, criteria)
	}
	w := beads.NewStateWrite(newBeadsClientFn())
	crit := strings.TrimSpace(criteria)
	if err := newBeadsClientFn().UpdateFields(ctx, p.Path, id, beads.Fields{Acceptance: &crit}); err != nil {
		return nil, err
	}
	for _, a := range approvers {
		if err := w.SetLabel(ctx, p.Path, id, beads.ApproverLabelPrefix+beads.OwnerLabelName(ownercompat.Normalize(a)), true); err != nil {
			return nil, err
		}
	}
	if err := w.SetLabel(ctx, p.Path, id, beads.GateLabel, true); err != nil {
		return nil, err
	}
	if err := w.Flush(ctx); err != nil {
		return nil, fmt.Errorf("%w (re-run the same command to finish: it only adds labels)", err)
	}
	return map[string]any{"bead_id": id, "gate": true, "approvers": approvers, "workstream": root, "kept": "card, owner, waits, edges"}, nil
}
