package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLastGoodDetailsKeepsEdgesAndMovedParents(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{
		ListResult: []beads.Bead{
			{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}},
			{ID: "p-9.1", Title: "Moved step", Status: "open", Labels: []string{"kind:objective"}},
		},
		ParentsResult: map[string]string{"p-1": "", "p-9.1": "p-1"},
		DepsResult:    map[string][]beads.Dep{"p-9.1": {{IssueID: "p-9.1", DependsOnID: "p-g", Type: "blocks"}}},
	}
	src := &Source{Deps: Deps{Store: st, Beads: bc}}
	first := src.Snapshot(ctx)
	if first.Degraded != nil {
		t.Fatalf("first read degraded: %+v", first.Degraded)
	}
	before, _ := src.Last()
	if len(before.Objectives) != 2 || before.Objectives[1].ParentBeadID != "p-1" || len(before.Objectives[1].BlockedBy) != 1 {
		t.Fatalf("bad fixture: %+v", before.Objectives)
	}
	bc.ParentsErr = errors.New("br show: signal: killed")
	bc.ListResult[1].Title = "Unpublished rename"
	failed := src.Snapshot(ctx)
	after, _ := src.Last()
	if failed.Degraded == nil || !failed.PreserveVersion || src.LastLoadErr() == nil {
		t.Fatalf("failure hidden: %+v", failed)
	}
	if !reflect.DeepEqual(first.Workstreams, failed.Workstreams) {
		t.Fatalf("failed project changed tree:\nbefore=%+v\nafter=%+v", first.Workstreams, failed.Workstreams)
	}
	if after.Objectives[1].ParentBeadID != "p-1" || !reflect.DeepEqual(before.Objectives[1].BlockedBy, after.Objectives[1].BlockedBy) {
		t.Fatalf("lost edges: %+v", after.Objectives)
	}
	bc.ParentsErr = nil
	recovered := src.Snapshot(ctx)
	if recovered.Degraded != nil || recovered.PreserveVersion {
		t.Fatalf("did not recover: %+v", recovered.Degraded)
	}
	data, _ := src.Last()
	if data.Objectives[1].Title != "Unpublished rename" {
		t.Fatal("recovery did not refresh")
	}
}

func TestLastGoodProjectIdentityAndEviction(t *testing.T) {
	ctx := context.Background()
	p := store.Project{ID: 1, Name: "example", Path: t.TempDir()}
	other := store.Project{ID: 2, Name: "other", Path: t.TempDir()}
	bc := &beads.MockClient{ListResult: []beads.Bead{{ID: "p-1", Title: "Example", Status: "open", Labels: []string{"kind:objective"}}}}
	for _, change := range []string{"readd", "rename", "remove"} {
		t.Run(change, func(t *testing.T) {
			cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
			bc.ParentsErr = nil
			if rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{p, other}, cache); err != nil || len(rows) != 2 {
				t.Fatalf("seed=%v err=%v", rows, err)
			}
			changed := p
			switch change {
			case "readd":
				changed.ID = 3
			case "rename":
				changed.Name = "renamed"
			case "remove":
				if _, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{other}, cache); err != nil {
					t.Fatal(err)
				}
				if len(cache.rows) != 1 {
					t.Fatalf("removed project retained: %v", cache.rows)
				}
			}
			bc.ParentsErr = errors.New("read unavailable")
			rows, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{changed, other}, cache)
			if err == nil || len(rows) != 1 || rows[0].ProjectID != other.ID {
				t.Fatalf("old identity resurrected: rows=%v err=%v", rows, err)
			}
			if change != "remove" && len(cache.rows) != 1 {
				t.Fatalf("old cache key retained: %v", cache.rows)
			}
		})
	}
}

// touchingClient moves a beads file during every List, as a writer would.
type touchingClient struct {
	*beads.MemClient
	touch atomic.Bool
	n     atomic.Int64
}

func (c *touchingClient) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	if c.touch.Load() {
		// The fingerprint is size+mtime; Linux timestamps are coarse (~4ms), so
		// grow the file on every touch rather than rely on mtime moving.
		_ = os.WriteFile(filepath.Join(dir, ".beads", "last-touched"), []byte(strings.Repeat("x", int(c.n.Add(1)))), 0o600)
	}
	return c.MemClient.List(ctx, dir, opts)
}

func TestChangedDuringReadKeepsLastGoodQuietlyThenDegradesBusy(t *testing.T) {
	old := beads.ReadRetryBackoff
	beads.ReadRetryBackoff = func() time.Duration { return time.Millisecond }
	defer func() { beads.ReadRetryBackoff = old }()
	ctx := context.Background()
	mem := &beads.MemClient{Prefix: "p"}
	project := coldProject(t, mem, 1, "alpha")
	bc := &touchingClient{MemClient: mem}
	cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
	read := func() ([]ObjectiveRow, error) {
		return objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, cache)
	}
	if rows, err := read(); err != nil || len(rows) != 2 {
		t.Fatalf("seed rows=%d err=%v", len(rows), err)
	}
	bc.touch.Store(true)
	for i := 1; i < busyMissLimit; i++ {
		rows, err := read()
		if err != nil || len(rows) != 2 {
			t.Fatalf("miss %d: rows=%d err=%v, want last-good and no error", i, len(rows), err)
		}
	}
	rows, err := read()
	if err == nil || !strings.Contains(err.Error(), "busy: beads changing continuously") || len(rows) != 2 {
		t.Fatalf("miss %d: rows=%d err=%v, want busy degrade with last-good rows", busyMissLimit, len(rows), err)
	}
	bc.touch.Store(false)
	if rows, err := read(); err != nil || len(rows) != 2 || cache.misses[rowsKey(project)] != 0 {
		t.Fatalf("recovery rows=%d err=%v misses=%v", len(rows), err, cache.misses)
	}
}

func TestChangedDuringReadRealErrorStillDegrades(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{Prefix: "p"}
	project := coldProject(t, mem, 1, "alpha")
	bc := &perProjectClient{MemClient: mem, failDir: project.Path}
	cache := &projectRowsCache{rows: map[projectRowsKey][]ObjectiveRow{}}
	_, err := objectiveRowsFromBeadsCached(ctx, bc, []store.Project{project}, cache)
	if !OnlyProjectReadErrors(err) {
		t.Fatalf("err=%v, want a degrading project read error", err)
	}
}

// Every project of a load has its read time on the snapshot, and it stays off the wire.
func TestSnapshotCarriesEachProjectsReadTime(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	for _, name := range []string{"alpha", "beta"} {
		if _, err := st.InsertProject(ctx, store.Project{Name: name, Type: "code", Path: "/" + name, ActivatedAt: 1, Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	src := &Source{Deps: Deps{Store: st, Beads: &beads.MockClient{}}}
	snap := src.Snapshot(ctx)
	if _, ok := snap.ProjectTimes["alpha"]; !ok || len(snap.ProjectTimes) != 2 {
		t.Fatalf("ProjectTimes = %v", snap.ProjectTimes)
	}
	raw, _ := json.Marshal(snap)
	if strings.Contains(string(raw), "ProjectTimes") {
		t.Fatal("read times reached the wire, and the content hash")
	}
}

// A load whose own context ended reaches no one: the watchers judge plans and
// clear health from OnSnapshot, and a hollow board would pass for a real one.
func TestSpentLoadReachesNoWatcherAndKeepsLastData(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	bc := &beads.MockClient{ListResult: []beads.Bead{{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}}}, ParentsResult: map[string]string{"p-1": ""}}
	var seen atomic.Int32
	src := &Source{Deps: Deps{Store: st, Beads: bc}, OnSnapshot: func(*Snapshot) { seen.Add(1) }}
	src.Snapshot(ctx)
	before, _ := src.Last()
	seen.Store(0)

	spent, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	snap := src.Snapshot(spent)
	if snap.Display != nil || snap.Degraded != nil || !snap.KeptLastGood {
		t.Fatalf("spent load built a board: %+v", snap)
	}
	if seen.Load() != 0 {
		t.Fatal("a watcher saw the spent load")
	}
	if err := src.LastLoadErr(); err != nil {
		t.Fatalf("LastLoadErr = %v", err)
	}
	if after, ok := src.Last(); !ok || !after.LoadedAt.Equal(before.LoadedAt) {
		t.Fatalf("last data replaced: %v, was %v", after.LoadedAt, before.LoadedAt)
	}
}

// One spent load is quiet, a clean or failed load speaks for itself, and a store
// that lets no load finish still reads as degraded in the end: expired settles do
// not reset the clock, whatever a hung load is doing meanwhile.
func TestNoLoadFinishingForLongReadsAsDegraded(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	src := &Source{Deps: Deps{Store: st}}
	if err := src.LastLoadErr(); err != nil {
		t.Fatalf("before any load: %v", err)
	}
	src.Snapshot(ctx)
	if err := src.LastLoadErr(); err != nil {
		t.Fatalf("after a clean load: %v", err)
	}
	spent, cancel := context.WithCancel(ctx)
	cancel()
	age := func(d time.Duration) {
		src.last.mu.Lock()
		src.last.at = time.Now().Add(-d)
		src.last.mu.Unlock()
	}

	age(noLoadFor - time.Minute)
	src.Snapshot(spent)
	if err := src.LastLoadErr(); err != nil {
		t.Fatalf("inside the window: %v", err)
	}
	age(noLoadFor + time.Minute)
	src.Snapshot(spent)
	err := src.LastLoadErr()
	if err == nil || !strings.Contains(err.Error(), "no load has finished for 11m") {
		t.Fatalf("a hung load reads as %v", err)
	}
	src.Snapshot(ctx)
	if err := src.LastLoadErr(); err != nil {
		t.Fatalf("a finished load did not clear it: %v", err)
	}
}

// slowGateClient makes the gate read the slow part of a load.
type slowGateClient struct {
	*beads.MockClient
	wait time.Duration
}

func (c *slowGateClient) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	if len(opts.Labels) > 0 && opts.Labels[0] == gates.KindLabel {
		time.Sleep(c.wait)
	}
	return c.MockClient.List(ctx, dir, opts)
}

// Each phase of a load is timed where it runs, on a spent load too.
func TestLoadPhasesTimeTheGateRead(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	const wait = 40 * time.Millisecond
	bc := &slowGateClient{MockClient: &beads.MockClient{ListResult: []beads.Bead{{ID: "p-1", Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}}}, ParentsResult: map[string]string{"p-1": ""}}, wait: wait}
	src := &Source{Deps: Deps{Store: st, Beads: bc}}
	p := src.Snapshot(ctx).Phases
	if p.Gates < wait {
		t.Fatalf("the gate read's time landed elsewhere: %s", p)
	}
	if p.Build <= 0 || p.Beads <= 0 {
		t.Fatalf("phases not timed: %s", p)
	}
	spent, cancel := context.WithCancel(ctx)
	defer cancel()
	bc.wait = 0
	cancel()
	if snap := src.Snapshot(spent); !strings.Contains(snap.LoadTimes(), "phases: lock ") {
		t.Fatalf("a spent load's line: %s", snap.LoadTimes())
	}
}
