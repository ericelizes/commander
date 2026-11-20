package execution

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/authpause"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

func TestAuthFailurePausesOnceRaisesOneCardAndResumesAfterSignIn(t *testing.T) {
	st, pid, ctx := fixture(t)
	home := t.TempDir()
	now := time.Now()
	if started, err := authpause.Mark(home, "default", "OAuth session expired", now); err != nil || !started {
		t.Fatal(started, err)
	}
	if started, _ := authpause.Mark(home, "default", "again", now); started {
		t.Fatal("a second failure restarted the pause")
	}
	launches := 0
	w := &Worker{Store: st, Capacity: 2, Now: func() time.Time { return now }, Spawner: fakeSpawner(func(context.Context, spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		launches++
		return nil, fmt.Errorf("spawn: %w", authpause.ErrPaused)
	})}
	for _, slot := range []string{"vm-0", "vm-1"} {
		if e := w.PassSlot(ctx, slot); e == nil {
			t.Fatal("a paused launch must report")
		}
	}
	rows, err := st.DB().Query(`SELECT execution_attempts,execution_error,status,execution_ready FROM bead_tasks WHERE project_id=?`, pid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var attempts int
		var detail, status string
		var ready bool
		if err = rows.Scan(&attempts, &detail, &status, &ready); err != nil {
			t.Fatal(err)
		}
		if attempts != 0 || !strings.HasPrefix(detail, authpause.WaitingPrefix) || status != "open" || !ready {
			t.Fatalf("step is not waiting for sign-in: %d %q %s %v", attempts, detail, status, ready)
		}
	}
	rows.Close()

	loggedIn := false
	watch := &AuthWatcher{Store: st, Home: home, Now: func() time.Time { return now }, Login: func(string) (bool, time.Time) { return loggedIn, time.Time{} }}
	for i := 0; i < 3; i++ {
		if err = watch.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cards, err := st.ListNeedsYou(ctx)
	if err != nil || len(cards) != 1 || cards[0].Ask != AuthCardAsk {
		t.Fatalf("want exactly one sign-in card, got %v %v", cards, err)
	}
	if !authpause.Paused(home, "default") {
		t.Fatal("pause lifted without a sign-in")
	}

	// Signing in again clears the card and the pause, and the steps are claimable.
	loggedIn = true
	if err = watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if authpause.Paused(home, "default") {
		t.Fatal("pause survived the sign-in")
	}
	if cards, _ = st.ListNeedsYou(ctx); len(cards) != 0 {
		t.Fatalf("card survived the sign-in: %v", cards)
	}
	w.Spawner = fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) { return recordRun(st, ctx, r) })
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal("steps did not return to ready:", e)
	}
	runs, _ := st.ListRunningRuns(ctx)
	if len(runs) != 1 || launches != 2 {
		t.Fatal(len(runs), launches)
	}
}

func TestAuthFailedRunGoesBackToReadyNotBlockedResult(t *testing.T) {
	st, pid, ctx := fixture(t)
	w := &Worker{Store: st, Capacity: 1, Spawner: fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) { return recordRun(st, ctx, r) })}
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	runs, _ := st.ListRunningRuns(ctx)
	if len(runs) != 1 {
		t.Fatal(len(runs))
	}
	r := runs[0]
	if e := st.FinishNativeExecutionExit(ctx, r.AgentName, r.ID, r.OpencodeSessionID, "native-error", "Failed to authenticate. API Error: 401 OAuth session expired · Please run /login", "exit code 1"); e != nil {
		t.Fatal(e)
	}
	var status, detail string
	var ready bool
	var result *string
	if e := st.DB().QueryRowContext(ctx, `SELECT status,execution_ready,execution_error,result_run_id::text FROM bead_tasks WHERE project_id=? AND id=?`, pid, r.TaskBeadID).Scan(&status, &ready, &detail, &result); e != nil {
		t.Fatal(e)
	}
	if status != "open" || !ready || result != nil || !strings.HasPrefix(detail, authpause.WaitingPrefix) {
		t.Fatalf("auth-failed step is not back to ready: %s %v %v %q", status, ready, result, detail)
	}
	if _, ok := interface{}(store.AuthWaiter{}).(store.AuthWaiter); !ok {
		t.Fatal()
	}
	if waiters, _ := st.AuthWaiters(ctx); len(waiters) != 1 {
		t.Fatal(waiters)
	}
}
