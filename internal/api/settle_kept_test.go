package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// overlappingClient moves a beads file during every List, as a writer would.
type overlappingClient struct {
	*beads.MemClient
	touch atomic.Bool
	n     atomic.Int64
}

func (c *overlappingClient) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	if c.touch.Load() {
		_ = os.WriteFile(filepath.Join(dir, ".beads", "last-touched"), []byte(strings.Repeat("x", int(c.n.Add(1)))), 0o600)
	}
	return c.MemClient.List(ctx, dir, opts)
}

// settled:true promises the snapshot shows the write. A load whose read overlaps
// writes on all its attempts keeps last-good rows without the write: that is
// settled:false, and stays quiet (not degraded, no version bump). The door, the
// real Source and the real read, only the backend is in memory.
func TestWrite_SettledIsFalseWhenAnOverlappedReadKeptLastGood(t *testing.T) {
	old := beads.ReadRetryBackoff
	beads.ReadRetryBackoff = func() time.Duration { return time.Millisecond }
	defer func() { beads.ReadRetryBackoff = old }()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := projectDir(t)
	if _, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "p"}
	id, err := mem.Create(ctx, dir, "Before", beads.CreateOptions{Labels: []string{"kind:objective"}})
	if err != nil {
		t.Fatal(err)
	}
	bc := &overlappingClient{MemClient: mem}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: bc}}
	var title atomic.Pointer[string]
	s := New(Options{Token: testToken, Source: src.Snapshot, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		return nil, nil, mem.UpdateFields(ctx, dir, id, beads.Fields{Title: title.Load()})
	}})
	defer s.settling.Wait()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	write := func(to string) beads.DoorResponse {
		t.Helper()
		title.Store(&to)
		_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"update", id, "--title", to}})
		if out.Exit != 0 {
			t.Fatalf("write: %+v", out)
		}
		return out
	}
	shown := func() string {
		t.Helper()
		snap := s.current(ctx)
		if len(snap.Workstreams) != 1 {
			t.Fatalf("workstreams = %+v", snap.Workstreams)
		}
		return snap.Workstreams[0].Title
	}

	if out := write("Before"); !out.Settled || shown() != "Before" {
		t.Fatalf("seed: settled=%v shown=%q", out.Settled, shown())
	}
	version := s.current(ctx).Version

	bc.touch.Store(true)
	out := write("After")
	if shown() != "Before" {
		t.Fatalf("bad fixture: the overlapped load showed %q", shown())
	}
	if out.Settled || !out.ProjectionPending {
		t.Fatalf("settled=%v pending=%v although the snapshot still reads %q", out.Settled, out.ProjectionPending, shown())
	}
	if snap := s.current(ctx); snap.Degraded != nil || snap.Version != version {
		t.Fatalf("the kept rows are not quiet: degraded=%+v version=%d, was %d", snap.Degraded, snap.Version, version)
	}

	bc.touch.Store(false)
	if out := write("After"); !out.Settled || shown() != "After" {
		t.Fatalf("recovery: settled=%v shown=%q", out.Settled, shown())
	}
}
