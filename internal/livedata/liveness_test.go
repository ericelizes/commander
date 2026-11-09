package livedata

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func livenessFixture(t *testing.T) (Snapshot, int64) {
	t.Helper()
	raw, err := os.ReadFile("testdata/board/liveness.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx.Snapshot, fx.Now
}

// The liveness fact is what changes the rows: the same beads without it read
// as the Managers last wrote them (a dead claim working, a wait on a dead
// Operator holding), which is what an older cmdrd showed.
func TestBoardLivenessIsTheFact(t *testing.T) {
	s, now := livenessFixture(t)
	with := Board(s, now)
	s.Live = nil
	without := Board(s, now)
	for _, c := range []struct {
		row        int
		with, word string
	}{
		{0, "Agent stopped on build wave 2 of the job moves: the Manager's move", "Working on build wave 2 of the job moves"},
		{5, "Agent stopped on switch dev to the new database at a quiet window: the Manager's move", "Waiting for an Operator"},
	} {
		if got := with.Workstreams[c.row].Text; got != c.with {
			t.Errorf("row %d with liveness: %q, want %q", c.row+1, got, c.with)
		}
		if got := without.Workstreams[c.row].Text; !strings.HasPrefix(got, c.word) || without.Workstreams[c.row].Why != "" {
			t.Errorf("row %d without liveness: %q (why %q), want %q...", c.row+1, got, without.Workstreams[c.row].Why, c.word)
		}
	}
	// Board never edits the caller's snapshot.
	if s.Workstreams[0].Steps[1].OwnerState != OwnerWorking {
		t.Fatalf("snapshot edited: %+v", s.Workstreams[0].Steps[1])
	}
}

// A Needs you whose work a live Operator is still on is not the user's yet;
// another ask beside it is.
func TestNeedsYouHeldOnlyWhileItsOperatorIsLive(t *testing.T) {
	s, now := livenessFixture(t)
	row := func(s Snapshot) BoardRow { return Board(s, now).Workstreams[2] }
	if r := row(s); r.Kind != "working" || r.Why == "" {
		t.Fatalf("live Operator under the ask: %s %q why %q", r.Kind, r.Text, r.Why)
	}
	s.Live = &SnapLive{Operators: []string{}}
	if r := row(s); r.Kind != "needs" || r.Text != "Needs you: Run the classifier on the real job list?" {
		t.Fatalf("its Operator gone: %s %q", r.Kind, r.Text)
	}
}

// A claim on an Operator checked after the look is not taken for dead.
func TestNewSnapLiveKeepsRunsSpawnedAfterTheLook(t *testing.T) {
	l := NewSnapLive([]string{"operator-op-a-p-1.2"}, 100, []OperatorRow{
		{AgentName: "operator-op-b-p-1.3", SpawnTime: time.Unix(101, 0)},
		{AgentName: "operator-op-c-p-1.4", SpawnTime: time.Unix(99, 0)},
	})
	ops := l.operators()
	if !ops.has("operator-op-a-p-1-2") || !ops.has("operator-op-b-p-1.3") || ops.has("operator-op-c-p-1.4") {
		t.Fatalf("live: %v", l.Operators)
	}
}

func TestMarkLiveOwners(t *testing.T) {
	wait := func(kind, ref string) beads.Wait { return beads.Wait{Kind: kind, Ref: ref, Since: time.Unix(1, 0)} }
	rows := []ObjectiveRow{
		{BeadID: "a", Status: "in_progress", State: beads.StateInProgress, Owner: "operator-live", OwnerState: OwnerWorking},
		{BeadID: "b", Status: "in_progress", State: beads.StateInProgress, Owner: "operator-gone", OwnerState: OwnerWorking},
		{BeadID: "c", Status: "in_progress", State: beads.StateReview, Owner: "operator-gone"},
		{BeadID: "d", Status: "in_progress", State: beads.StateInProgress, Owner: "manager-w", Waits: []beads.Wait{wait(beads.WaitOperator, "operator-gone")}},
		{BeadID: "e", Status: "in_progress", State: beads.StateInProgress, Owner: "manager-w", Waits: []beads.Wait{wait(beads.WaitOperator, "operator-gone"), wait(beads.WaitCI, "")}},
		{BeadID: "f", Status: "closed", State: beads.StateDone, Owner: "operator-gone", OwnerState: OwnerWorking},
	}
	got := MarkLiveOwners(rows, LiveOperators{"operator-live": true})
	want := []struct{ state, why string }{
		{OwnerWorking, ""}, {OwnerDied, whyNotRunning}, {OwnerFinished, whyNotRunning}, {OwnerDied, whyWaitedGone}, {"", ""}, {OwnerWorking, ""},
	}
	for i, w := range want {
		if got[i].OwnerState != w.state || got[i].OwnerWhy != w.why {
			t.Errorf("%s: %q %q, want %q %q", got[i].BeadID, got[i].OwnerState, got[i].OwnerWhy, w.state, w.why)
		}
	}
	if len(got[3].Waits) != 0 || len(got[4].Waits) != 1 || got[4].Waits[0].Kind != beads.WaitCI {
		t.Fatalf("waits: %+v / %+v", got[3].Waits, got[4].Waits)
	}
	if rows[1].OwnerState != OwnerWorking || len(rows[3].Waits) != 1 {
		t.Fatal("edited the loaded rows")
	}
	if MarkLiveOwners(rows, nil)[1].OwnerState != OwnerWorking {
		t.Fatal("no liveness fact must leave claims as recorded")
	}
}

// A settled death carries when it happened and how often cmdrd restarted it.
func TestMarkDiedFromTheSettleEvent(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "a", ProjectID: 1, Status: "open", OwnerState: OwnerDied, Restarts: restartsOf([]string{"crash-respawn:5", "kind:task"})},
		{BeadID: "b", ProjectID: 1, Status: "open", OwnerState: OwnerDied},
	}
	ev := func(bead string, at int64, reason string) store.CostEvent {
		return store.CostEvent{ProjectID: 1, TaskBeadID: sql.NullString{String: bead, Valid: true}, EventType: store.EventOperatorDied,
			Reason: sql.NullString{String: reason, Valid: true}, OccurredAt: at}
	}
	got := markDied(rows, []store.CostEvent{ev("a", 10, "x"), ev("a", 20, "y"), ev("b", 30, "session gone after the Mac slept")})
	if got[0].OwnerSince != 20 || got[0].OwnerWhy != "Operator stopped without a report; restarted once, stopped again" {
		t.Fatalf("a: %+v", got[0])
	}
	if got[1].OwnerSince != 30 || got[1].OwnerWhy != "Operator stopped while the machine slept" {
		t.Fatalf("b: %+v", got[1])
	}
}

// cmdrd's snapshot checks claims against what runs: a step whose Operator's run
// row still says running reads stopped once the liveness fact lacks it, and
// the watch and invariant follow.
func TestSourceSnapshotChecksClaimsAgainstLiveness(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	pid, err := st.InsertProject(ctx, store.Project{Name: "app", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "app-1.1", AgentName: "operator-op-x-app-1.1", SpawnTime: 1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentState(ctx, "manager-app-1", store.AgentIdle, ""); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "app-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "app-1.1", Title: "Do the work", Status: "in_progress", Labels: []string{"kind:objective", "owner:operator-op-x-app-1-1"}},
		},
		ParentsResult: map[string]string{"app-1": "", "app-1.1": "app-1"},
	}
	alive := []string{"operator-op-x-app-1.1"}
	src := &Source{Deps: Deps{Store: st, Beads: bc}, Live: func() ([]string, time.Time, bool) { return alive, time.Unix(1000, 0), true }}
	step := func(s Snapshot) SnapStep { return s.Workstreams[0].Steps[0] }
	if s := src.Snapshot(ctx); step(s).OwnerState != OwnerWorking || len(s.Broken) != 0 || len(s.Watch["manager-app-1"].Operators) != 1 {
		t.Fatalf("live: %+v broken %v watch %+v", step(s), s.Broken, s.Watch)
	}
	alive = nil
	s := src.Snapshot(ctx)
	if step(s).OwnerState != OwnerDied || step(s).OwnerWhy != whyNotRunning || len(s.Broken) != 1 || len(s.Watch["manager-app-1"].Operators) != 0 {
		t.Fatalf("gone: %+v broken %v watch %+v", step(s), s.Broken, s.Watch)
	}
	if r := s.Display.Workstreams[0]; r.Kind != "stalled" || r.Why != whyNotRunning {
		t.Fatalf("row: %+v", r)
	}
	if s.Live == nil || s.Live.At != 1000 {
		t.Fatalf("live fact: %+v", s.Live)
	}
	// The same claim with a report in: the Manager's move, nothing broken, the bead's claim kept in the loaded rows.
	src.Reported = func() []SnapReported {
		return []SnapReported{{Operator: "operator-op-x-app-1-1", ProjectID: s.Workstreams[0].ProjectID, Bead: "app-1.1", Since: 1793214000}}
	}
	s = src.Snapshot(ctx)
	if st := step(s); st.Owner != "manager-app-1" || st.State != "review" || st.Waiting != "Manager to close" || len(s.Broken) != 0 || len(SnapshotViolations(s)) != 0 {
		t.Fatalf("reported: %+v broken %v", st, s.Broken)
	}
	if r := s.Display.Workstreams[0]; r.Why == whyNotRunning {
		t.Fatalf("reported row: %+v", r)
	}
	if len(s.Live.Reported) != 1 {
		t.Fatalf("live fact: %+v", s.Live)
	}
	src.Reported = nil
	if s := src.Snapshot(ctx); step(s).OwnerState != OwnerDied || len(s.Broken) != 1 {
		t.Fatalf("settle dropped, bead speaks again: %+v broken %v", step(s), s.Broken)
	}
}
