package daemon

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
)

type fakeWatchStore struct {
	runs     []store.AgentRun
	watches  map[int64]store.RunWatch
	checkins map[string]store.Checkin
	listErr  error
}

func (f *fakeWatchStore) ListRunningRuns(context.Context) ([]store.AgentRun, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.runs, nil
}
func (f *fakeWatchStore) RunWatches(context.Context) (map[int64]store.RunWatch, error) {
	return f.watches, nil
}
func (f *fakeWatchStore) SetRunWatch(_ context.Context, id int64, w store.RunWatch) error {
	f.watches[id] = w
	return nil
}
func (f *fakeWatchStore) Checkins(context.Context) (map[string]store.Checkin, error) {
	out := map[string]store.Checkin{}
	for k, v := range f.checkins {
		out[k] = v
	}
	return out, nil
}
func (f *fakeWatchStore) SetCheckin(_ context.Context, role string, c store.Checkin) error {
	f.checkins[role] = c
	return nil
}
func (f *fakeWatchStore) ClearCheckin(_ context.Context, role string) error {
	delete(f.checkins, role)
	return nil
}

type said struct{ role, text string }

func newWatch(st *fakeWatchStore, now time.Time, active map[string]time.Time) (*opWatch, *[]said, *[]said) {
	var delivered, mailed []said
	w := &opWatch{
		store: st,
		now:   func() time.Time { return now },
		activeAt: func(sid string) (time.Time, bool) {
			t, ok := active[sid]
			return t, ok
		},
		deliver: func(_ context.Context, role, prompt string) error {
			delivered = append(delivered, said{role, prompt})
			return nil
		},
		mail: func(_ context.Context, role, subject, _ string) error {
			mailed = append(mailed, said{role, subject})
			return nil
		},
	}
	return w, &delivered, &mailed
}

func TestWatchRecordsActivityWithoutArmingACheckin(t *testing.T) {
	now := time.Unix(10_000, 0)
	st := &fakeWatchStore{
		runs:     []store.AgentRun{{ID: 1, TaskBeadID: "example-app-t11.2.4.4", AgentName: "operator-op-a", OpencodeSessionID: "s1", SpawnTime: 9_000}},
		watches:  map[int64]store.RunWatch{},
		checkins: map[string]store.Checkin{"manager-gone-1": {Due: 1}},
	}
	w, delivered, mailed := newWatch(st, now, map[string]time.Time{"s1": now.Add(-30 * time.Second)})
	w.pass(context.Background())
	if got := st.watches[1].ActiveAt; got != now.Add(-30*time.Second).Unix() {
		t.Fatalf("active_at = %d", got)
	}
	c := st.checkins["manager-example-app-t11"]
	if c.Due != 0 || c.Checked != now.Unix() {
		t.Fatalf("checkin = %+v", c)
	}
	if _, ok := st.checkins["manager-gone-1"]; ok {
		t.Fatal("a Manager with no Operators keeps its check-in")
	}
	if len(*delivered)+len(*mailed) != 0 {
		t.Fatalf("woke someone on a quiet pass: %v %v", *delivered, *mailed)
	}
}

func TestWatchDeliversARequestedCheckinOnce(t *testing.T) {
	now := time.Unix(10_000, 0)
	st := &fakeWatchStore{
		runs:     []store.AgentRun{{ID: 1, TaskBeadID: "p-7.1", AgentName: "operator-op-a", SpawnTime: 9_900}, {ID: 2, TaskBeadID: "p-7.2", AgentName: "operator-op-b", SpawnTime: 9_900}},
		watches:  map[int64]store.RunWatch{},
		checkins: map[string]store.Checkin{"manager-p-7": {Due: now.Unix() - 5}},
	}
	w, delivered, _ := newWatch(st, now, nil)
	w.pass(context.Background())
	if len(*delivered) != 1 || (*delivered)[0].role != "manager-p-7" || !strings.HasPrefix((*delivered)[0].text, "[mail] cmdrd: Check-in: 2 Operators still at work\n") {
		t.Fatalf("delivered = %v", *delivered)
	}
	if st.checkins["manager-p-7"].Due != 0 {
		t.Fatalf("not disarmed: %+v", st.checkins["manager-p-7"])
	}

	// Mid-turn: the check-in stays due for the next pass.
	st.checkins["manager-p-7"] = store.Checkin{Due: now.Unix() - 5}
	w.deliver = func(context.Context, string, string) error { return wake.ErrDeferred }
	w.pass(context.Background())
	if st.checkins["manager-p-7"].Due != now.Unix()-5 {
		t.Fatalf("deferred check-in moved: %+v", st.checkins["manager-p-7"])
	}
}

func TestWatchTellsTheManagerOnceWhenAnOperatorGoesQuiet(t *testing.T) {
	now := time.Unix(100_000, 0)
	st := &fakeWatchStore{
		runs:     []store.AgentRun{{ID: 1, TaskBeadID: "p-7.1", AgentName: "operator-op-a", OpencodeSessionID: "s1", SpawnTime: 1, TmuxPaneID: sql.NullString{String: "cmdr-p-op-a", Valid: true}}},
		watches:  map[int64]store.RunWatch{},
		checkins: map[string]store.Checkin{},
	}
	w, _, mailed := newWatch(st, now, map[string]time.Time{"s1": now.Add(-25 * time.Minute)})
	w.pass(context.Background())
	w.pass(context.Background())
	if len(*mailed) != 1 || (*mailed)[0].role != "manager-p-7" || (*mailed)[0].text != "operator-op-a quiet for 25m on p-7.1" {
		t.Fatalf("mailed = %v", *mailed)
	}
}

// Invented promotion: example-app-t02.8 becomes a workstream, so its
// Operators belong to manager-example-app-t02.8. The snapshot (grouped by real
// parent edges) decides; a run it does not show yet is resolved through br.
func TestWatchGroupsByTheRealWorkstream(t *testing.T) {
	now := time.Unix(10_000, 0)
	newStore := func() *fakeWatchStore {
		return &fakeWatchStore{
			runs:     []store.AgentRun{{ID: 1, ProjectID: 3, TaskBeadID: "example-app-t02.8.4.5", AgentName: "operator-op-a", SpawnTime: 9_900}},
			watches:  map[int64]store.RunWatch{},
			checkins: map[string]store.Checkin{},
		}
	}

	st := newStore()
	w, _, _ := newWatch(st, now, nil)
	w.latest = func() (livedata.Snapshot, bool) {
		return livedata.Snapshot{Watch: map[string]livedata.SnapWatch{
			"manager-example-app-t02.8": {Operators: []livedata.SnapWatchedOp{{Role: "operator-op-a", BeadID: "example-app-t02.8.4.5"}}},
		}}, true
	}
	w.manager = func(context.Context, store.AgentRun) string {
		t.Fatal("the snapshot already names the Manager")
		return ""
	}
	w.pass(context.Background())
	if _, ok := st.checkins["manager-example-app-t02.8"]; !ok {
		t.Fatalf("from the snapshot: checkins = %v", st.checkins)
	}

	st = newStore()
	w, _, _ = newWatch(st, now, nil)
	bc := &beads.MockClient{ParentsResult: map[string]string{"example-app-t02.8": ""}}
	w.manager = func(ctx context.Context, run store.AgentRun) string { return runManager(ctx, oneProject{}, bc, run) }
	w.pass(context.Background())
	if _, ok := st.checkins["manager-example-app-t02.8"]; !ok || len(st.checkins) != 1 {
		t.Fatalf("through br: checkins = %v", st.checkins)
	}
}
