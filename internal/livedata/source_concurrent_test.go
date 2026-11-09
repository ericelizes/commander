package livedata

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// churningClient answers every objective read with new workstreams, so each
// load's rows differ from the last: every board_since row is written and removed.
type churningClient struct {
	*beads.MemClient
	rows  int
	gen   atomic.Int64
	block chan struct{} // non-nil: the first objective read waits on it
	held  chan struct{} // closed once that read waits
	once  sync.Once
}

func (c *churningClient) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	if !slices.Contains(opts.Labels, "kind:objective") {
		return c.MemClient.List(ctx, dir, opts)
	}
	if c.block != nil {
		first := false
		c.once.Do(func() { first = true })
		if first {
			close(c.held)
			<-c.block
		}
	}
	gen := c.gen.Add(1)
	out := make([]beads.Bead, c.rows)
	for i := range out {
		out[i] = beads.Bead{ID: fmt.Sprintf("p-%d%04d", gen, i), Title: "Workstream", Status: "open", Labels: []string{"kind:objective"}}
	}
	return out, nil
}

func churningSource(t *testing.T, rows int) (*Source, *churningClient) {
	t.Helper()
	st := openStore(t)
	mem := &beads.MemClient{Prefix: "p"}
	project := coldProject(t, mem, 1, "example")
	if _, err := st.InsertProject(context.Background(), store.Project{Name: project.Name, Type: "code", Path: project.Path, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	bc := &churningClient{MemClient: mem, rows: rows}
	return &Source{Deps: Deps{Store: st, Beads: bc}}, bc
}

func bothSnapshot(calls int, each func(ctx context.Context)) {
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range calls {
				each(context.Background())
			}
		})
	}
	wg.Wait()
}

// The health loop and an API load call one Source at once: with changing rows,
// everything after Load (unread counts, chat ages, the board's ages) is shared.
// Run with -race.
func TestConcurrentSnapshotsWithChangingRows(t *testing.T) {
	src, _ := churningSource(t, 3)
	src.Unread = func(context.Context, string) (int, error) { return 0, nil }
	src.Chats = func() map[string]ChatState { return map[string]ChatState{"commander": {}} }
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				src.Last()
				src.LastLoadErr()
			}
		}
	})
	bothSnapshot(30, func(ctx context.Context) {
		if snap := src.Snapshot(ctx); snap.Display == nil {
			t.Error("no display")
		}
	})
	close(stop)
	readers.Wait()
}

// Without -race too: two callers filling board_since at once used to end the
// whole process ("fatal error: concurrent map read and map write"), which no
// recover catches. No store here, so the map work is all a fill does, and Chats
// lines the callers up just before the board.
func TestConcurrentSnapshotsDoNotCrashTheSinceTracker(t *testing.T) {
	meet := make(chan struct{})
	src := &Source{Chats: func() map[string]ChatState {
		select {
		case meet <- struct{}{}:
		case <-meet:
		case <-time.After(time.Millisecond):
		}
		return nil
	}}
	project := store.Project{ID: 1, Name: "example", Path: "/example", Status: "active"}
	var gen atomic.Int64
	const rows = 200
	bothSnapshot(50, func(ctx context.Context) {
		data := Data{Projects: []store.Project{project}, LoadedAt: time.Now()}
		n := gen.Add(1)
		for i := range rows {
			data.Objectives = append(data.Objectives, ObjectiveRow{ProjectID: 1, ProjectName: "example", BeadID: fmt.Sprintf("p-%d%04d", n, i), Title: "Workstream", Status: "open"})
		}
		if snap := src.build(ctx, data); snap.Display == nil || len(snap.Display.Workstreams) != rows {
			t.Errorf("bad fixture: %d rows on the board", len(snap.Workstreams))
		}
	})
	if len(src.since.rows) != rows {
		t.Fatalf("board_since has %d rows after the last clean load, want %d", len(src.since.rows), rows)
	}
}

// A load can take minutes: the health loop's Snapshot must not wait behind one.
func TestSnapshotDoesNotWaitForAnotherCallersLoad(t *testing.T) {
	src, bc := churningSource(t, 1)
	bc.block, bc.held = make(chan struct{}), make(chan struct{})
	slow := make(chan struct{})
	go func() { defer close(slow); src.Snapshot(context.Background()) }()
	<-bc.held
	defer func() { close(bc.block); <-slow }()
	done := make(chan struct{})
	go func() { defer close(done); src.Snapshot(context.Background()) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a Snapshot waited for another caller's load")
	}
}
