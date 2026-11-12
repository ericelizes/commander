package execution

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
	"time"
)

func TestUsageCapacityDoesNotSpendLaunchAttempts(t *testing.T) {
	st, pid, ctx := fixture(t)
	now := time.Now()
	calls := 0
	w := &Worker{Store: st, Capacity: 1, Now: func() time.Time { return now }, Spawner: fakeSpawner(func(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		calls++
		return nil, fmt.Errorf("capacity: %w", chat.ErrNoUsage)
	})}
	for i := 0; i < 7; i++ {
		for j := 0; j < 2; j++ {
			if e := w.PassSlot(ctx, "vm-0"); e == nil {
				t.Fatal("missing capacity failure")
			}
		}
		now = now.Add(time.Minute)
	}
	rows, e := st.DB().Query(`SELECT execution_attempts,execution_error FROM bead_tasks WHERE project_id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var attempts int
		var detail string
		if e = rows.Scan(&attempts, &detail); e != nil {
			t.Fatal(e)
		}
		if attempts != 0 || !strings.HasPrefix(detail, store.UsageCapacityError) {
			t.Fatal(attempts, detail)
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	w.Spawner = fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		return recordRun(st, ctx, r)
	})
	if e = w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal("available capacity could not recover", e)
	}
	runs, e := st.ListRunningRuns(ctx)
	if e != nil || len(runs) != 1 || calls != 14 {
		t.Fatal(runs, calls, e)
	}
}
