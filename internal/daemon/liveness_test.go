package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example-user/commander/internal/store"
)

// The liveness fact is looked up at most every liveEvery; a failed look says
// it cannot tell, so the snapshot keeps the claims as recorded.
func TestLiveOperatorsCachesAndFailsOpen(t *testing.T) {
	now := time.Unix(1793214000, 0)
	calls := 0
	var fail error
	l := &liveOperators{now: func() time.Time { return now }, list: func(context.Context) ([]store.AgentRun, error) {
		calls++
		return []store.AgentRun{{AgentName: "operator-op-a-p-1.2"}}, fail
	}}
	names, at, ok := l.get()
	if !ok || len(names) != 1 || names[0] != "operator-op-a-p-1.2" || !at.Equal(now) {
		t.Fatalf("first: %v %v %v", names, at, ok)
	}
	now = now.Add(liveEvery - time.Second)
	if _, _, ok := l.get(); !ok || calls != 1 {
		t.Fatalf("cached look ran again: calls %d", calls)
	}
	now = now.Add(time.Second)
	fail = errors.New("tmux: no server")
	if _, _, ok := l.get(); ok || calls != 2 {
		t.Fatalf("failed look: ok %v calls %d", ok, calls)
	}
}
