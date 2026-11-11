package beads

import (
	"context"
	"errors"
)

// BehindOpenBlocker reports whether id waits (a blocks edge) on a blocker still
// in play (including parked unpassed approvals) other than except: it is not ready,
// so nothing has to hold it yet (livedata.OpenBeadInvariant's rule). Reads are
// fresh; a blocker that no longer exists holds nothing.
func BehindOpenBlocker(ctx context.Context, c Client, dir, id, except string) (bool, error) {
	details, err := FreshDetails(ctx, c, dir, []string{id})
	if err != nil {
		return false, err
	}
	for _, d := range details[id].Deps {
		if d.Type != "blocks" || d.DependsOnID == "" || d.DependsOnID == except {
			continue
		}
		blocker, err := FreshShow(ctx, c, dir, d.DependsOnID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if blocker != nil && !DependencyComplete(*blocker) {
			return true, nil
		}
	}
	return false, nil
}
