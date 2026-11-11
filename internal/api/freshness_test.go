package api

import (
	"context"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
)

func TestCompleteLoadHeartbeat(t *testing.T) {
	snap := livedata.Snapshot{GeneratedAt: time.Now(), Workstreams: []livedata.SnapWorkstream{{Title: "Retained work"}}}
	s := New(Options{Source: func(context.Context) livedata.Snapshot { return snap }})
	ctx := context.Background()
	if !s.Refresh(ctx) {
		t.Fatal("initial content missing")
	}
	before := s.current(ctx)
	if before.LoadCompletedAt.IsZero() || before.LoadError {
		t.Fatal("missing successful heartbeat")
	}
	if s.Refresh(ctx) {
		t.Fatal("unchanged healthy content advanced version")
	}
	healthy := s.current(ctx)
	if !healthy.LoadCompletedAt.After(before.LoadCompletedAt) || healthy.Version != before.Version || healthy.GeneratedAt != before.GeneratedAt {
		t.Fatal("heartbeat must advance independently of content")
	}
	snap.KeptLastGood = true
	s.Refresh(ctx)
	failed := s.current(ctx)
	if !failed.LoadError || failed.LoadCompletedAt != healthy.LoadCompletedAt || len(failed.Workstreams) != 1 {
		t.Fatal("partial load renewed evidence or lost rows")
	}
	snap.KeptLastGood = false
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	s.Refresh(canceled)
	if !s.current(ctx).LoadError || s.current(ctx).LoadCompletedAt != healthy.LoadCompletedAt {
		t.Fatal("canceled load renewed heartbeat")
	}
	s.Refresh(ctx)
	recovered := s.current(ctx)
	if recovered.LoadError || !recovered.LoadCompletedAt.After(healthy.LoadCompletedAt) || recovered.Version != before.Version {
		t.Fatal("unchanged recovery did not restore independent heartbeat")
	}
	if contentHash(before) != contentHash(recovered) {
		t.Fatal("freshness changed content hash")
	}
}

func TestFreshnessWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &fakeSource{}
	f.set("Retained work")
	s := New(Options{Token: testToken, Source: f.load})
	s.Refresh(ctx)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/events?token="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	var first Event
	if err := wsjson.Read(ctx, c, &first); err != nil {
		t.Fatal(err)
	}
	s.Refresh(ctx)
	var frame struct {
		Type      string    `json:"type"`
		Completed time.Time `json:"load_completed_at"`
		Error     bool      `json:"load_error"`
	}
	if err := wsjson.Read(ctx, c, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "freshness" || !frame.Completed.After(first.Snapshot.LoadCompletedAt) || frame.Error {
		t.Fatalf("bad heartbeat: %+v", frame)
	}
	f.mu.Lock()
	f.snap.KeptLastGood = true
	f.mu.Unlock()
	s.Refresh(ctx)
	if err := wsjson.Read(ctx, c, &frame); err != nil {
		t.Fatal(err)
	}
	if !frame.Error {
		t.Fatal("load failure not sent over connected socket")
	}
	f.mu.Lock()
	f.snap.KeptLastGood = false
	f.mu.Unlock()
	s.Refresh(ctx)
	if err := wsjson.Read(ctx, c, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Error {
		t.Fatal("recovery not sent")
	}
}
