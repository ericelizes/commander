package beads

import (
	"context"
	"fmt"
)

// StateWrite collects idempotent state assignments until every precondition has
// passed. Reads still use the underlying client. Flush writes once per bead.
type StateWrite struct {
	Client
	changes []stateChange
}
type stateChange struct {
	dir, id, label, status string
	on                     bool
}

func NewStateWrite(c Client) *StateWrite { return &StateWrite{Client: c} }
func (w *StateWrite) SetLabel(_ context.Context, dir, id, label string, on bool) error {
	w.changes = append(w.changes, stateChange{dir: dir, id: id, label: label, on: on})
	return nil
}
func (w *StateWrite) SetStatus(_ context.Context, dir, id, status string) error {
	w.changes = append(w.changes, stateChange{dir: dir, id: id, status: status})
	return nil
}

// updateFields is optional so existing embedded/mock clients keep their usual
// setters. Production clients execute all assignments in one br update.
type fieldUpdater interface {
	updateFields(context.Context, string, string, []stateChange) error
}

func (w *StateWrite) Flush(ctx context.Context) error {
	for len(w.changes) > 0 {
		first := w.changes[0]
		var group, rest []stateChange
		for _, c := range w.changes {
			if c.dir == first.dir && c.id == first.id {
				group = append(group, c)
			} else {
				rest = append(rest, c)
			}
		}
		if u, ok := w.Client.(fieldUpdater); ok {
			if err := u.updateFields(ctx, first.dir, first.id, group); err != nil {
				return fmt.Errorf("state update %s failed (assignments may have applied; safe to retry): %w", first.id, err)
			}
		} else {
			for i, c := range group {
				var err error
				if c.status != "" {
					err = w.Client.SetStatus(ctx, c.dir, c.id, c.status)
				} else {
					err = w.Client.SetLabel(ctx, c.dir, c.id, c.label, c.on)
				}
				if err != nil {
					return fmt.Errorf("state update %s: %d/%d assignments applied: %w", first.id, i, len(group), err)
				}
			}
		}
		w.changes = rest
	}
	return nil
}
func (c *cmdClient) updateFields(ctx context.Context, dir, id string, changes []stateChange) error {
	// Last assignment wins, including a label removed and re-added by a helper.
	var args []string
	for i, change := range changes {
		later := false
		for _, next := range changes[i+1:] {
			if change.label == next.label {
				later = true
				break
			}
		}
		if later {
			continue
		}
		if change.status != "" {
			args = append(args, "--status", change.status)
		} else {
			flag := "--remove-label"
			if change.on {
				flag = "--add-label"
			}
			args = append(args, flag, change.label)
		}
	}
	if len(args) == 0 {
		return nil
	}
	_, stderr, err := c.runUpdate(ctx, dir, id, args...)
	if err != nil {
		return fmt.Errorf("br update: %w: %s", err, stderr)
	}
	return nil
}
func (c *CachedClient) updateFields(ctx context.Context, dir, id string, changes []stateChange) error {
	defer c.forget(dir)
	w := NewStateWrite(c.Client)
	w.changes = changes
	return w.Flush(ctx)
}
