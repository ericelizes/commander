package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// custodyWithin is how long work a close or create left unheld waits on its
// Manager before the board flags it and cmdrd mails the Manager again.
const custodyWithin = beads.ManagerCloseWithin

// holdForManager records Manager custody (a "Manager to start" receipt) on
// each step before the write that would leave it with nothing holding it, so
// the board reads it as the Manager's next move, never State missing. A real
// successor (a spawn under it, a wait, a card, an Operator owner) clears the
// receipt. Each step is read fresh first: one that is closed, parked, the
// user's, already waiting (on anything but an Operator), claimed by an Operator or (not started) behind an
// open blocker other than closing is left alone (the board it was judged on
// may lag). closing is the bead the write is about to close ("" for none). It
// returns the steps it wrote.
func holdForManager(ctx context.Context, bc beads.Client, dir string, ids []string, closing string, now time.Time) ([]string, error) {
	var held []string
	err := beads.ReadCustodySnapshot(ctx, bc, dir, func(ctx context.Context) error {
		var err error
		held, err = holdForManagerInSnapshot(ctx, bc, dir, ids, closing, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return held, nil
}

func holdForManagerInSnapshot(ctx context.Context, bc beads.Client, dir string, ids []string, closing string, now time.Time) ([]string, error) {
	// Validate the complete set before queuing any receipt, including callers
	// that settled custody without the CLI closure preflight.
	if err := beads.RequireLocalCustody(ctx, bc, dir, ids); err != nil {
		return nil, err
	}
	writes := beads.NewStateWrite(bc)
	var held []string
	for _, id := range ids {
		b, err := beads.FreshShow(ctx, bc, dir, id)
		// Typed provenance failures must precede the legacy local absence exception.
		if beads.IsCrossProjectError(err) {
			return nil, err
		}
		if errors.Is(err, beads.ErrNotFound) {
			continue // the board lags a removal: nothing to hold
		}
		if err != nil {
			return nil, err
		}
		if b == nil || b.Status == "closed" || b.Status == beads.StatusDeferred {
			continue
		}
		state, owner := beads.StepState(b.Status, b.Labels)
		if state == beads.StateNeedsUser || holdingWait(b.Labels) || strings.HasPrefix(owner, "operator-") {
			continue
		}
		if b.Status != "in_progress" {
			behind, err := beads.BehindOpenBlocker(ctx, bc, dir, id, closing)
			if err != nil {
				return nil, err
			}
			if behind {
				continue
			}
		}
		if err := writes.SetLabel(ctx, dir, id, custodyLabel(now), true); err != nil {
			return nil, err
		}
		held = append(held, id)
	}
	if err := writes.Flush(ctx); err != nil {
		return nil, err
	}
	return held, nil
}

// custodyLabel is the "Manager to start" receipt holdForManager writes at now.
func custodyLabel(now time.Time) string {
	return beads.StartReceiptLabel(now)
}

// releaseCustody takes back the receipts holdForManager wrote at now on ids
// when the write they were for did not happen (a refused close): the steps
// are still behind that bead, and a receipt left there would go overdue and
// mail the Manager about work nobody released. It returns the ids it could not
// clear.
func releaseCustody(ctx context.Context, bc beads.Client, dir string, ids []string, now time.Time) []string {
	label := custodyLabel(now)
	var left []string
	for _, id := range ids {
		if err := bc.SetLabel(ctx, dir, id, label, false); err != nil {
			left = append(left, id)
		}
	}
	return left
}

// holdingWait: the labels record a wait other than on an Operator. A step's
// wait on an Operator holds only while that Operator runs, and the board that
// named the step unheld already judged it (livedata.MarkLiveOwners): the
// Operator whose task the close finishes is gone.
func holdingWait(labels []string) bool {
	for _, w := range beads.WaitsOf(labels) {
		if w.Kind != beads.WaitOperator {
			return true
		}
	}
	return false
}

// custodyIDs are the bead ids of the steps a permitted close leaves unheld.
func custodyIDs(vs []livedata.Violation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.BeadID)
	}
	return out
}

// custodyErr is why a close did not happen when its custody write failed.
func custodyErr(ids []string, closing string, err error) error {
	return fmt.Errorf("the Manager's hold on %s (left with nothing holding it) was not written, so %s was not closed: %w", strings.Join(ids, ", "), closing, err)
}
