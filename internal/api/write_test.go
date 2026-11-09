package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

func projectDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func postWrite(t *testing.T, url, token string, req beads.DoorRequest) (*http.Response, beads.DoorResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, url+beads.DoorPath, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out beads.DoorResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestWrite_NeedsTokenAndAWrite(t *testing.T) {
	_, _, ts := newTestServer(t, nil)
	dir := projectDir(t)
	if resp, _ := postWrite(t, ts.URL, "", beads.DoorRequest{Dir: dir, Args: []string{"close", "x"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp, _ := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: dir, Args: []string{"list", "--json"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a read: %d", resp.StatusCode)
	}
	if resp, _ := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: t.TempDir(), Args: []string{"close", "x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("not a project: %d", resp.StatusCode)
	}
}

// The answer comes back only once the daemon's snapshot shows the write.
func TestWrite_VisibleInStateOnReturn(t *testing.T) {
	src := &fakeSource{}
	src.set("before")
	s := New(Options{Token: testToken, Source: src.load, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		time.Sleep(30 * time.Millisecond)
		src.set("after")
		return []byte("ok"), nil, nil
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	s.Refresh(context.Background())
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "x"}})
	if out.Exit != 0 || out.Stdout != "ok" || !out.Settled {
		t.Fatalf("answer = %+v", out)
	}
	if got := s.current(context.Background()).Workstreams[0].Title; got != "after" {
		t.Fatalf("snapshot after the answer shows %q", got)
	}
}

func TestWrite_ReportsBrFailure(t *testing.T) {
	src := &fakeSource{}
	src.set("a")
	s := New(Options{Token: testToken, Source: src.load, Write: func(ctx context.Context, dir string, args []string) ([]byte, []byte, error) {
		return nil, []byte("no such bead"), os.ErrNotExist
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	_, out := postWrite(t, ts.URL, testToken, beads.DoorRequest{Dir: projectDir(t), Args: []string{"close", "x"}})
	if out.Exit == 0 || out.Stderr != "no such bead" || out.Error == "" {
		t.Fatalf("answer = %+v", out)
	}
}

func TestSettle_CoalescesWritersOntoOneLoad(t *testing.T) {
	var mu sync.Mutex
	loads := 0
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		mu.Lock()
		loads++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return livedata.Snapshot{}
	}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.settle(context.Background(), time.Now()) }()
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
	if loads > 3 {
		t.Fatalf("%d snapshot loads for 8 writers", loads)
	}
}

// A settle that times out neither cancels the snapshot load nor publishes a partial one.
func TestSettle_TimeoutLetsTheLoadFinishUncancelled(t *testing.T) {
	old := settleWait
	settleWait = 30 * time.Millisecond
	defer func() { settleWait = old }()
	var cancelled atomic.Bool
	release := make(chan struct{})
	finished := make(chan struct{})
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		defer close(finished)
		<-release
		time.Sleep(3 * settleWait) // well past the wait: a WithTimeout(settleWait) ctx is dead by now
		cancelled.Store(ctx.Err() != nil)
		return livedata.Snapshot{}
	}})
	defer s.settling.Wait()
	if s.settle(context.Background(), time.Now()) {
		t.Fatal("settle reported settled although the load was still running")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("load never finished")
	}
	if cancelled.Load() {
		t.Fatal("the load's context was cancelled by the timed-out settle")
	}
}

// Waiters queued behind a slow load drain without loading again.
func TestSettle_TimedOutWaitersDoNotPileUpLoads(t *testing.T) {
	old := settleWait
	settleWait = 10 * time.Millisecond
	defer func() { settleWait = old }()
	var loads atomic.Int32
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		loads.Add(1)
		time.Sleep(100 * time.Millisecond)
		return livedata.Snapshot{}
	}})
	for i := 0; i < 10; i++ {
		s.settle(context.Background(), time.Now())
	}
	s.settling.Wait()
	if n := loads.Load(); n > 3 {
		t.Fatalf("%d loads for 10 timed-out settles", n)
	}
}

// The shared load never sees the caller's context values (door env).
func TestSettle_LoadDoesNotInheritCallerContext(t *testing.T) {
	type key struct{}
	var leaked atomic.Bool
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		leaked.Store(ctx.Value(key{}) != nil)
		return livedata.Snapshot{}
	}})
	s.settle(context.WithValue(context.Background(), key{}, "env"), time.Now())
	if leaked.Load() {
		t.Fatal("the caller's context values reached the shared snapshot load")
	}
}

// A load past slowLoad says how long it took and where the time went; a quick one says nothing.
func TestRefreshLogsASlowLoadPerProject(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	s := New(Options{Token: testToken, Source: func(context.Context) livedata.Snapshot {
		time.Sleep(5 * time.Millisecond)
		return livedata.Snapshot{ProjectTimes: map[string]time.Duration{"alpha": time.Second, "beta": 2 * time.Second}}
	}})

	s.slowLoad = time.Hour
	s.Refresh(context.Background())
	if logs.Len() != 0 {
		t.Fatalf("a quick load logged: %s", logs.String())
	}
	s.slowLoad = time.Millisecond
	s.Refresh(context.Background())
	if got := logs.String(); !strings.Contains(got, "snapshot load took") || !strings.Contains(got, "beta 2s, alpha 1s") {
		t.Fatalf("log = %q", got)
	}
	if got := New(Options{Token: testToken}).slowLoad; got != 10*time.Second {
		t.Fatalf("slowLoad = %s, want 10s", got)
	}
}

// Time queued behind a slow load is not the next load's: its budget starts at
// the lock, so it never runs on an expired context and publishes an empty board.
func TestSettle_BudgetStartsOnceTheLoadHoldsTheLock(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	expired := make(chan bool, 1)
	var loads atomic.Int32
	s := New(Options{Token: testToken, Source: func(ctx context.Context) livedata.Snapshot {
		if loads.Add(1) == 1 {
			close(started)
			<-release
		} else {
			expired <- ctx.Err() != nil
		}
		return livedata.Snapshot{}
	}})
	s.refreshBudget = 40 * time.Millisecond
	refreshed := make(chan struct{})
	go func() { defer close(refreshed); s.Refresh(context.Background()) }()
	<-started
	done := make(chan struct{})
	go func() { defer close(done); s.settle(context.Background(), time.Now()) }()
	time.Sleep(3 * s.refreshBudget) // queued well past the budget
	close(release)
	select {
	case gone := <-expired:
		if gone {
			t.Fatal("the queued load ran on a context its wait had already expired")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the queued load never ran")
	}
	<-done
	<-refreshed
	s.settling.Wait()
}

// A slow load says where its time went: the rest of the 3-minute load that had
// no timer is what this line is for.
func TestRefreshLogsTheLoadsPhases(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	s := New(Options{Token: testToken, Source: func(context.Context) livedata.Snapshot {
		time.Sleep(5 * time.Millisecond)
		return livedata.Snapshot{Phases: livedata.LoadPhases{Lock: time.Millisecond, PreRead: 2 * time.Millisecond, Beads: time.Minute, Gates: 78 * time.Second, Store: 3 * time.Millisecond, Build: 4 * time.Millisecond}}
	}})
	s.slowLoad = time.Millisecond
	s.Refresh(context.Background())
	want := "phases: lock 1ms, pre-read 2ms, beads 1m0s, gates 1m18s, store 3ms, build 4ms"
	if got := logs.String(); !strings.Contains(got, want) {
		t.Fatalf("log = %q, want %q", got, want)
	}
}
