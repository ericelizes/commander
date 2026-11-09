package spawn

import (
	"context"
	"fmt"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

// acceptDispatchHandoff runs only after a real task-bound run was persisted.
// Open children now hold their ancestors, so a queued-dispatch receipt is stale.
func (s *Spawner) acceptDispatchHandoff(ctx context.Context, req SpawnRequest) error {
	seen := map[string]bool{}
	for id := req.TaskBeadID; id != ""; {
		if seen[id] {
			return fmt.Errorf("cycle in task ancestry at %s", id)
		}
		seen[id] = true
		details, err := beads.FreshDetails(ctx, s.beads, req.ProjectPath, []string{id})
		if err != nil {
			return err
		}
		d, ok := details[id]
		if !ok {
			return fmt.Errorf("missing task ancestry at %s", id)
		}
		b, err := beads.FreshShow(ctx, s.beads, req.ProjectPath, id)
		if err != nil {
			return err
		}
		if b != nil && b.Status != "closed" && b.Status != beads.StatusDeferred {
			var dispatch []string
			for _, label := range b.Labels {
				if w, ok := beads.ParseWaitLabel(label); ok && beads.IsManagerDispatch(w) {
					dispatch = append(dispatch, label)
				}
			}
			if len(dispatch) > 0 {
				writes := beads.NewStateWrite(s.beads)
				for _, label := range b.Labels {
					if strings.HasPrefix(label, "handoff-notified:") {
						if err = writes.SetLabel(ctx, req.ProjectPath, id, label, false); err != nil {
							return err
						}
					}
				}
				for _, label := range dispatch {
					if err = writes.SetLabel(ctx, req.ProjectPath, id, label, false); err != nil {
						return err
					}
				}
				if err = writes.Flush(ctx); err != nil {
					return err
				}
			}
		}
		id = d.Parent
	}
	return nil
}
