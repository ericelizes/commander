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
	watch := &AuthWatcher{Store: st, Home: home, Now: func() time.Time { return now }, Healthy: func(string) bool { return false }, Login: func(string) (bool, time.Time) { return loggedIn, time.Time{} }}
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
	w.Spawner = fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		return recordRun(st, ctx, r)
	})
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
	// Finding 3: two attempts are spent; an auth failure must not reset them.
	if _, e := st.DB().ExecContext(ctx, `UPDATE bead_tasks SET execution_attempts=2 WHERE project_id=?`, pid); e != nil {
		t.Fatal(e)
	}
	w := &Worker{Store: st, Capacity: 1, Spawner: fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		return recordRun(st, ctx, r)
	})}
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	runs, _ := st.ListRunningRuns(ctx)
	if len(runs) != 1 {
		t.Fatal(len(runs))
	}
	r := runs[0]
	if e := st.FinishNativeExecutionExit(ctx, r.AgentName, r.ID, r.OpencodeSessionID, "native-auth-failed", "Failed to authenticate. API Error: 401 OAuth session expired · Please run /login", "exit code 1"); e != nil {
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
	var attempts int
	if e := st.DB().QueryRowContext(ctx, `SELECT execution_attempts FROM bead_tasks WHERE project_id=? AND id=?`, pid, r.TaskBeadID).Scan(&attempts); e != nil || attempts < 2 {
		t.Fatalf("an auth failure reset the attempt counter: %d %v", attempts, e)
	}
	if _, ok := interface{}(store.AuthWaiter{}).(store.AuthWaiter); !ok {
		t.Fatal()
	}
	if waiters, _ := st.AuthWaiters(ctx); len(waiters) != 1 {
		t.Fatal(waiters)
	}
}

// Finding 2: message text alone ("401 unauthorized", "not logged in") is a
// crash path, never a sign-in wait.
func TestTextOnlyAuthWordsDoNotPauseTheStep(t *testing.T) {
	st, pid, ctx := fixture(t)
	w := &Worker{Store: st, Capacity: 1, Spawner: fakeSpawner(func(ctx context.Context, r spawn.SpawnRequest) (*spawn.SpawnResult, error) {
		return recordRun(st, ctx, r)
	})}
	if e := w.PassSlot(ctx, "vm-0"); e != nil {
		t.Fatal(e)
	}
	runs, _ := st.ListRunningRuns(ctx)
	r := runs[0]
	if e := st.FinishNativeExecutionExit(ctx, r.AgentName, r.ID, r.OpencodeSessionID, "native-error", "the endpoint returned 401 unauthorized; the user was not logged in", "exit code 1"); e != nil {
		t.Fatal(e)
	}
	var detail string
	if e := st.DB().QueryRowContext(ctx, `SELECT execution_error FROM bead_tasks WHERE project_id=? AND id=?`, pid, r.TaskBeadID).Scan(&detail); e != nil {
		t.Fatal(e)
	}
	if strings.HasPrefix(detail, authpause.WaitingPrefix) {
		t.Fatalf("text alone parked the step for a sign-in: %q", detail)
	}
	if waiters, _ := st.AuthWaiters(ctx); len(waiters) != 0 {
		t.Fatal(waiters)
	}
}

func parkForSignIn(t *testing.T, st *store.Store, ctx context.Context, pid int64) {
	t.Helper()
	if _, e := st.DB().ExecContext(ctx, `UPDATE bead_tasks SET execution_error=? WHERE project_id=?`, authpause.WaitingPrefix+"x", pid); e != nil {
		t.Fatal(e)
	}
}

func newWatch(t *testing.T, st *store.Store, loggedIn *bool, healthy *bool) (*AuthWatcher, string) {
	home := t.TempDir()
	if _, err := authpause.Mark(home, "default", "OAuth session expired", time.Now()); err != nil {
		t.Fatal(err)
	}
	return &AuthWatcher{Store: st, Home: home,
		Healthy: func(string) bool { return *healthy },
		Login:   func(string) (bool, time.Time) { return *loggedIn, time.Time{} }}, home
}

// Finding 1: the sign-in card never overwrites or deletes a user's own card.
func TestSignInCardNeverTouchesAUserDecisionCard(t *testing.T) {
	st, pid, ctx := fixture(t)
	parkForSignIn(t, st, ctx, pid)
	decision := `{"options":[{"label":"Ship it"}]}`
	if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: "one", Ask: "Pick a release", Note: "user card", Decision: decision}); err != nil {
		t.Fatal(err)
	}
	in, ok := false, false
	watch, home := newWatch(t, st, &in, &ok)
	if err := watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	cards, _ := st.ListNeedsYou(ctx)
	if len(cards) != 2 {
		t.Fatalf("want the user's card plus one sign-in card, got %v", cards)
	}
	user, _ := st.GetNeedsYou(ctx, pid, "one")
	if user.Ask != "Pick a release" || user.Decision != decision {
		t.Fatalf("the user's decision card was overwritten: %+v", user)
	}
	signin, _ := st.GetNeedsYou(ctx, pid, "two")
	if signin == nil || signin.Ask != AuthCardAsk {
		t.Fatalf("sign-in card not on the card-free step: %+v", signin)
	}
	// Recovery removes only the sign-in card.
	in = true
	if err := watch.Pass(ctx); err != nil { // not logged out since, no change: stays
		t.Fatal(err)
	}
	watch.Login = func(string) (bool, time.Time) { return true, time.Now().Add(time.Hour) }
	if err := watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if authpause.Paused(home, "default") {
		t.Fatal("pause survived the sign-in")
	}
	cards, _ = st.ListNeedsYou(ctx)
	if len(cards) != 1 || cards[0].BeadID != "one" || cards[0].Decision != decision {
		t.Fatalf("recovery deleted more than the sign-in card: %v", cards)
	}
}

func TestSignInCardWaitsRatherThanReplaceACard(t *testing.T) {
	st, pid, ctx := fixture(t)
	parkForSignIn(t, st, ctx, pid)
	for _, id := range []string{"one", "two"} {
		if err := st.UpsertNeedsYou(ctx, store.NeedsYou{ProjectID: pid, BeadID: id, Ask: "Decide " + id, Decision: `{"options":[]}`}); err != nil {
			t.Fatal(err)
		}
	}
	in, ok := false, false
	watch, _ := newWatch(t, st, &in, &ok)
	if err := watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	cards, _ := st.ListNeedsYou(ctx)
	for _, c := range cards {
		if c.Ask == AuthCardAsk || !strings.HasPrefix(c.Ask, "Decide ") {
			t.Fatalf("a user card was replaced: %v", cards)
		}
	}
}

// Finding 2 (recovery): a pause also clears once the account is seen logged in
// and healthy, with no logged-out sighting and no credential change.
func TestPauseClearsWhenTheAccountIsSeenLoggedInAndHealthy(t *testing.T) {
	st, pid, ctx := fixture(t)
	parkForSignIn(t, st, ctx, pid)
	in, ok := true, false
	watch, home := newWatch(t, st, &in, &ok)
	if err := watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if !authpause.Paused(home, "default") {
		t.Fatal("an unhealthy account must stay paused")
	}
	ok = true
	watch.Now = func() time.Time { return time.Now().Add(3 * healthEvery) }
	if err := watch.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if authpause.Paused(home, "default") {
		t.Fatal("a logged-in healthy account stayed paused")
	}
	if waiters, _ := st.AuthWaiters(ctx); len(waiters) != 0 {
		t.Fatalf("steps not released: %v", waiters)
	}
}

// Finding 4: a failure between recording the carrier and inserting the card
// leaves one pending card, finished by the next pass, never a second card.
func TestOnePauseRaisesExactlyOneCardEvenAfterAPartialFailure(t *testing.T) {
	st, pid, ctx := fixture(t)
	parkForSignIn(t, st, ctx, pid)
	in, ok := false, false
	watch, home := newWatch(t, st, &in, &ok)
	// The carrier was recorded, then the process died before the card landed.
	if err := authpause.Update(home, "default", func(e *authpause.Entry) { e.CardProject, e.CardBead = pid, "two" }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := watch.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cards, _ := st.ListNeedsYou(ctx)
	if len(cards) != 1 || cards[0].BeadID != "two" || cards[0].Ask != AuthCardAsk {
		t.Fatalf("want exactly one sign-in card on the recorded carrier, got %v", cards)
	}
	// And the card landing first with the record lost cannot make a second one:
	// the insert is idempotent per step.
	if ok, err := st.InsertNeedsYouIfAbsent(ctx, store.NeedsYou{ProjectID: pid, BeadID: "two", Ask: AuthCardAsk}); err != nil || ok {
		t.Fatal("a second sign-in card was inserted on the same step", ok, err)
	}
}
