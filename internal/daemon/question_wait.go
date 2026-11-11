package daemon

import (
	"context"
	"github.com/example-user/commander/internal/store"
)

func (s runSettler) questionWait(ctx context.Context, run store.AgentRun) (bool, error) {
	reader, ok := s.projects.(interface {
		RunQuestionWait(context.Context, int64) (bool, error)
	})
	if !ok {
		return false, nil
	}
	// A question is an intentional checkpoint, not a report or a crash. Even a
	// superseded question is returned to planning rather than auto-restarted.
	return reader.RunQuestionWait(ctx, run.ID)
}
