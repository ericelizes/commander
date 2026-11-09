package api

import (
	"bytes"
	"context"
	"log"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// hangingClient holds the reads a test picks until the load's context ends, as
// a read that outlives the load's budget does.
type hangingClient struct {
	*beads.MemClient
	hang atomic.Pointer[func(beads.ListOptions) bool]
}

func (c *hangingClient) List(ctx context.Context, dir string, opts beads.ListOptions) ([]beads.Bead, error) {
	if hang := c.hang.Load(); hang != nil && (*hang)(opts) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.MemClient.List(ctx, dir, opts)
}

// loadFixture is a board with one workstream and one running Operator, read by
// the real Source from a real store; only the beads backend is in memory.
type loadFixture struct {
	st    *store.Store
	bc    *hangingClient
	src   *livedata.Source
	srv   *Server
	dir   string
	bead  string
	title atomic.Pointer[string]
}

func newLoadFixture(t *testing.T) *loadFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &loadFixture{st: st, dir: projectDir(t)}
	id, err := st.InsertProject(ctx, store.Project{Name: "example", Type: "code", Path: f.dir, ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{Prefix: "p"}
	if f.bead, err = mem.Create(ctx, f.dir, "Before", beads.CreateOptions{Labels: []string{"kind:objective"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: id, AgentName: "operator-example-1", TaskBeadID: f.bead, Status: "running", SpawnTime: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	f.bc = &hangingClient{MemClient: mem}
	f.src = &livedata.Source{Deps: livedata.Deps{Store: st, Beads: f.bc}}
	f.srv = New(Options{Token: testToken, Source: f.src.Snapshot, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		return nil, nil, mem.UpdateFields(ctx, dir, f.bead, beads.Fields{Title: f.title.Load()})
	}})
	t.Cleanup(f.srv.settling.Wait)
	return f
}

func (f *loadFixture) write(t *testing.T, url, to string) beads.DoorResponse {
	t.Helper()
	f.title.Store(&to)
	_, out := postWrite(t, url, testToken, beads.DoorRequest{Dir: f.dir, Args: []string{"update", f.bead, "--title", to}})
	if out.Exit != 0 {
		t.Fatalf("write: %+v", out)
	}
	return out
}

// The diagnosed load: a settle's load ran out of its budget. Every store read
// after that failed, so it came back with no Operators and read as degraded. It
// must publish nothing, keep the last board and the last good data, not read as
// degraded, and answer settled:false. Through the door, the real settle and
// refresh, whichever read the budget ends in.
func TestSettle_ALoadThatOutlivesItsBudgetPublishesNothing(t *testing.T) {
	for name, hang := range map[string]func(beads.ListOptions) bool{
		"in the project read": func(o beads.ListOptions) bool { return !slices.Contains(o.Labels, gates.KindLabel) },
		"in the gate read":    func(o beads.ListOptions) bool { return slices.Contains(o.Labels, gates.KindLabel) },
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prev)
			f := newLoadFixture(t)
			ts := httptest.NewServer(f.srv.Handler())
			defer ts.Close()
			ctx := context.Background()

			if out := f.write(t, ts.URL, "Before"); !out.Settled {
				t.Fatalf("seed did not settle: %+v", out)
			}
			before := f.srv.current(ctx)
			if len(before.Workstreams) != 1 || before.Fleet.Agents != 1 || before.Degraded != nil {
				t.Fatalf("bad fixture: %+v", before)
			}
			ch := f.srv.subscribe()
			defer f.srv.unsubscribe(ch)

			f.srv.refreshBudget = 50 * time.Millisecond
			f.bc.hang.Store(&hang)
			out := f.write(t, ts.URL, "After")
			f.srv.settling.Wait()

			if out.Settled || !out.ProjectionPending {
				t.Fatalf("settled=%v pending=%v on a load that ran out of budget", out.Settled, out.ProjectionPending)
			}
			after := f.srv.current(ctx)
			if after.Version != before.Version || contentHash(after) != contentHash(before) {
				t.Fatalf("a spent load was published: version %d -> %d, operators %d, degraded %+v", before.Version, after.Version, after.Fleet.Agents, after.Degraded)
			}
			select {
			case <-ch:
				t.Fatal("subscribers were told about a spent load")
			default:
			}
			if err := f.src.LastLoadErr(); err != nil {
				t.Fatalf("one spent load reads as degraded: %v", err)
			}
			if last, ok := f.src.Last(); !ok || len(last.Operators) != 1 {
				t.Fatalf("last good data replaced: ok=%v operators=%d", ok, len(last.Operators))
			}
			if !strings.Contains(logs.String(), "kept the last board") {
				t.Fatalf("not logged: %q", logs.String())
			}

			f.bc.hang.Store(nil)
			f.srv.refreshBudget = defaultRefreshBudget
			out = f.write(t, ts.URL, "After")
			if shown := f.srv.current(ctx).Workstreams[0].Title; !out.Settled || shown != "After" {
				t.Fatalf("recovery: settled=%v shown=%q", out.Settled, shown)
			}
		})
	}
}

// A shutdown or a caller that hangs up ends a load the same way.
func TestRefresh_ACancelledLoadPublishesNothing(t *testing.T) {
	f := newLoadFixture(t)
	before := f.srv.current(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f.srv.Refresh(ctx) {
		t.Fatal("a cancelled load changed the board")
	}
	if after := f.srv.current(context.Background()); after.Version != before.Version || after.Fleet.Agents != 1 {
		t.Fatalf("published: %+v", after)
	}
	if !f.srv.loadKept.Load() {
		t.Fatal("a settle would answer settled:true on it")
	}
}

// A store that is really broken is a load with its context alive: it degrades
// as ever, published, with the store's own words.
func TestRefresh_ARealStoreErrorStillDegrades(t *testing.T) {
	for name, tc := range map[string]struct {
		breakIt func(t *testing.T, st *store.Store)
		want    string
	}{
		"schema": {func(t *testing.T, st *store.Store) {
			if _, err := st.DB().Exec(`ALTER TABLE agent_runs RENAME TO agent_runs_gone`); err != nil {
				t.Fatal(err)
			}
		}, "no such table"},
		"closed": {func(t *testing.T, st *store.Store) {
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
		}, "closed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLoadFixture(t)
			before := f.srv.current(context.Background())
			tc.breakIt(t, f.st)
			f.srv.Refresh(context.Background())
			snap := f.srv.current(context.Background())
			if snap.Degraded == nil || !strings.Contains(snap.Degraded.Reason, tc.want) {
				t.Fatalf("degraded = %+v, want %q", snap.Degraded, tc.want)
			}
			if snap.Version == before.Version {
				t.Fatal("the degraded snapshot was not published")
			}
			if err := f.src.LastLoadErr(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LastLoadErr = %v, want %q", err, tc.want)
			}
			if f.srv.lastLoadClean() {
				t.Fatal("a settle would answer settled:true on it")
			}
		})
	}
}
