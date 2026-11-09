package daemon

import (
	"context"
	"time"

	"github.com/example-user/commander/internal/boardturn"
	"github.com/example-user/commander/internal/livedata"
)

// withBoards wraps the API's snapshot source so every snapshot also refreshes
// the board files the agents' hooks read (~/.commander/board/<role>.json).
// The API builds one snapshot at a time, and a door write settles one before
// it returns, so the files follow a state write before the writer's next turn.
// Commander's board also carries the user's Linear sprint (sprintEntries).
func withBoards(home string, src func(context.Context) livedata.Snapshot, sprint *sprintFeed) func(context.Context) livedata.Snapshot {
	w := &boardturn.Writer{Home: home}
	return func(ctx context.Context) livedata.Snapshot {
		snap := src(ctx)
		// A degraded snapshot is stale: stop the heartbeat so the hooks say
		// the board is unavailable rather than serve it as current.
		if snap.Display == nil || snap.Degraded != nil {
			return snap
		}
		now := time.Now()
		sp := sprint.read(now)
		snap.Wrong = wrongOnScreen(*snap.Display, snap.Workstreams, sp, home, now)
		commander := append(livedata.BoardEntries(*snap.Display, now.Unix(), "commander"), sprintEntries(*snap.Display, snap.Workstreams, sp)...)
		commander = append(commander, wrongEntries(snap.Wrong)...)
		boards := map[string][]boardturn.Entry{"commander": commander}
		for _, ws := range snap.Display.Workstreams {
			if ws.ManagerRole != "" {
				boards[ws.ManagerRole] = livedata.BoardEntries(*snap.Display, now.Unix(), ws.ManagerRole)
			}
		}
		snap.BoardVersions = w.Write(boards, now)
		return snap
	}
}
