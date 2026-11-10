package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

type invariantTestKey struct{}

// The export-backed display path returns immediately; DB-only invariant
// hydration stalls until its context ends, as a slow bd show would.
type invariantLoadBD struct {
	*beads.MemClient
	entered            chan struct{}
	once               sync.Once
	display, invariant atomic.Int64
	requested          atomic.Int64
}

func (c *invariantLoadBD) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if ctx.Value(invariantTestKey{}) == true {
		c.invariant.Add(1)
		c.requested.Store(int64(len(ids)))
		c.once.Do(func() { close(c.entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c.display.Add(1)
	return c.MemClient.Details(ctx, dir, ids)
}

// Scenario runs here to cover real API write settling, Source loading and
// snapshot publication, rather than duplicating their private behavior.
func TestInvariantLoadIsolationScenario(t *testing.T) {
	var sc struct {
		Rows         int `json:"rows"`
		MaxRefreshMS int `json:"max_refresh_ms"`
		DisplayPolls int `json:"display_polls"`
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/invariant-load-isolation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Rows != 6219 || sc.DisplayPolls < 2 || sc.MaxRefreshMS <= 0 {
		t.Fatal("invalid scenario")
	}
	ctx := context.Background()
	dir := projectDir(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.InsertProject(ctx, store.Project{Name: "example", Path: dir, Type: "code", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	mem := &beads.MemClient{}
	ids := []string{"demo-root", "demo-root.1"}
	mem.Seed(beads.Bead{ID: ids[0], Title: "Example workstream", Status: "open", Labels: []string{"kind:objective"}})
	mem.Seed(beads.Bead{ID: ids[1], Title: "Example step", Status: "open", Labels: []string{"kind:objective"}})
	for i := 2; i < sc.Rows; i++ {
		id := fmt.Sprintf("demo-history-%d", i)
		ids = append(ids, id)
		mem.Seed(beads.Bead{ID: id, Title: "Finished example", Status: "closed", Labels: []string{"kind:objective"}})
	}
	bd := &invariantLoadBD{MemClient: mem, entered: make(chan struct{})}
	cached := beads.NewCached(bd)
	inv := beads.IsolatedIncremental(cached)
	invCtx, cancel := context.WithTimeout(context.WithValue(ctx, invariantTestKey{}, true), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := inv.Details(invCtx, dir, ids); done <- err }()
	defer func() { cancel(); <-done }()
	select {
	case <-bd.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no invariant hydration")
	}
	src := &livedata.Source{Deps: livedata.Deps{Store: st, Beads: cached}}
	var writes atomic.Int64
	s := New(Options{Token: testToken, Source: src.Snapshot, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		writes.Add(1)
		if err := mem.Close(ctx, dir, args[1]); err != nil {
			return nil, nil, err
		}
		err := os.WriteFile(filepath.Join(dir, ".beads", "last-touched"), []byte("closed"), 0600)
		return nil, nil, err
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	defer s.settling.Wait()
	start := time.Now()
	var wg sync.WaitGroup
	for range sc.DisplayPolls {
		wg.Add(1)
		go func() { defer wg.Done(); s.Refresh(ctx) }()
	}
	wg.Wait()
	before := s.current(ctx)
	if before.Degraded != nil || len(before.Workstreams) != 1 || len(before.Workstreams[0].Steps) != 1 {
		t.Fatalf("bad seed snapshot: %+v", before)
	}
	if resp, _ := postWrite(t, ts.URL, "", beads.DoorRequest{Dir: dir, Args: []string{"close", ids[1]}}); resp.StatusCode != http.StatusUnauthorized || writes.Load() != 0 {
		t.Fatal("unauthorized close reached backend")
	}
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"close", ids[1]}})
	if out.Exit != 0 || !out.Settled {
		t.Fatalf("close failed to settle: %+v", out)
	}
	after := s.current(ctx)
	if after.Version <= before.Version || !after.GeneratedAt.After(before.GeneratedAt) || after.Degraded != nil || after.Workstreams[0].Steps[0].Status != "closed" {
		t.Fatalf("close not freshly published: before=%+v after=%+v", before, after)
	}
	if elapsed := time.Since(start); elapsed > time.Duration(sc.MaxRefreshMS)*time.Millisecond {
		t.Fatalf("refresh %v exceeded scenario bound", elapsed)
	}
	if bd.invariant.Load() != 1 || bd.requested.Load() != int64(sc.Rows) || bd.display.Load() != 2 {
		t.Fatalf("reread storm: invariant=%d requested=%d display=%d", bd.invariant.Load(), bd.requested.Load(), bd.display.Load())
	}
	select {
	case err := <-done:
		done <- err
		t.Fatalf("invariant ended before close publication: %v", err)
	default:
	}
	cancel()
	if !errors.Is(invCtx.Err(), context.Canceled) {
		t.Fatal(invCtx.Err())
	}
	t.Logf("rows=%d invariant still blocked; close settled; version=%d->%d generated_at=%s->%s refresh=%v display hydrations=2 unauthorized writes=0", sc.Rows, before.Version, after.Version, before.GeneratedAt.Format(time.RFC3339Nano), after.GeneratedAt.Format(time.RFC3339Nano), time.Since(start))
}
