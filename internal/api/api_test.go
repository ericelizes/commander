package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/example-user/commander/internal/livedata"
)

const testToken = "s3cret"

// fakeSource returns whatever snapshot the test last set.
type fakeSource struct {
	mu   sync.Mutex
	snap livedata.Snapshot
}

func (f *fakeSource) set(title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = livedata.Snapshot{
		GeneratedAt: time.Now(),
		Workstreams: []livedata.SnapWorkstream{{N: 1, BeadID: "w", Title: title}},
		NeedsYou:    []livedata.NeedsYou{},
	}
}

func (f *fakeSource) load(context.Context) livedata.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.GeneratedAt = time.Now() // changes every load; must not bump the version
	return s
}

func newTestServer(t *testing.T, run Runner) (*Server, *fakeSource, *httptest.Server) {
	t.Helper()
	src := &fakeSource{}
	src.set("first")
	s := New(Options{Token: testToken, Source: src.load, Run: run})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, src, ts
}

func get(t *testing.T, url string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestAuth_TokenRequired(t *testing.T) {
	_, _, ts := newTestServer(t, nil)
	cases := []struct {
		name   string
		url    string
		header map[string]string
		want   int
	}{
		{"no token", ts.URL + "/api/snapshot", nil, http.StatusUnauthorized},
		{"bad token", ts.URL + "/api/snapshot", map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"bearer", ts.URL + "/api/snapshot", map[string]string{"Authorization": "Bearer " + testToken}, http.StatusOK},
		{"query", ts.URL + "/api/snapshot?token=" + testToken, nil, http.StatusOK},
		{"placeholder is public", ts.URL + "/", nil, http.StatusOK},
		{"foreign origin", ts.URL + "/api/snapshot?token=" + testToken, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
	}
	for _, c := range cases {
		if got := get(t, c.url, c.header).StatusCode; got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCORS_OnlyLocalOrigins(t *testing.T) {
	_, _, ts := newTestServer(t, nil)
	resp := get(t, ts.URL+"/api/snapshot?token="+testToken, map[string]string{"Origin": "http://localhost:5173"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("local origin not allowed: %v", resp.Header)
	}
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/cmd", nil)
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	pre, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = pre.Body.Close()
	if pre.StatusCode != http.StatusNoContent || pre.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("preflight = %d %v", pre.StatusCode, pre.Header)
	}
	// The app's Install sends OriginHeader; a preflight that does not allow it blocks the request.
	if !strings.Contains(pre.Header.Get("Access-Control-Allow-Headers"), OriginHeader) {
		t.Fatalf("preflight does not allow %s: %v", OriginHeader, pre.Header)
	}
}

func TestSnapshot_ShapeAndVersion(t *testing.T) {
	s, src, ts := newTestServer(t, nil)
	read := func() map[string]any {
		var m map[string]any
		if err := json.NewDecoder(get(t, ts.URL+"/api/snapshot?token="+testToken, nil).Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := read()
	for _, key := range []string{"version", "generated_at", "projects", "workstreams", "needs_you", "agents", "fleet", "unread"} {
		if _, ok := m[key]; !ok {
			t.Errorf("snapshot lacks %q: %v", key, m)
		}
	}
	if m["version"].(float64) != 1 {
		t.Fatalf("version = %v", m["version"])
	}
	if s.Refresh(context.Background()) {
		t.Fatal("an unchanged snapshot (only generated_at moved) must not bump the version")
	}
	src.set("second")
	if !s.Refresh(context.Background()) {
		t.Fatal("a changed snapshot must bump the version")
	}
	if m := read(); m["version"].(float64) != 2 {
		t.Fatalf("version = %v", m["version"])
	}
}

func TestCmd_AllowList(t *testing.T) {
	var ran [][]string
	var mu sync.Mutex
	run := func(_ context.Context, argv []string) CmdResult {
		mu.Lock()
		ran = append(ran, argv)
		mu.Unlock()
		return CmdResult{OK: true, Stdout: "done\n"}
	}
	_, _, ts := newTestServer(t, run)
	post := func(body string, token string) (int, CmdResult) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/cmd", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var res CmdResult
		_ = json.NewDecoder(resp.Body).Decode(&res)
		return resp.StatusCode, res
	}
	for _, body := range []string{
		`{"argv":["restart"]}`,
		`{"argv":["refresh"]}`,
		`{"argv":["tab","nuke"]}`,
		`{"argv":["tab"]}`,
		`{"argv":[]}`,
		`{"argv":["sh","-c","rm -rf /"]}`,
	} {
		if code, _ := post(body, testToken); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", body, code)
		}
	}
	if code, _ := post(`{"argv":["tab","ls"]}`, "wrong"); code != http.StatusUnauthorized {
		t.Errorf("cmd without token: %d", code)
	}
	code, res := post(`{"argv":["internal-resolve-needs","--project","p","--bead","w.1","--action","decide"]}`, testToken)
	if code != http.StatusOK || !res.OK || res.Stdout != "done\n" {
		t.Fatalf("allowed cmd = %d %+v", code, res)
	}
	if code, _ := post(`{"argv":["tab","add","--url","x"]}`, testToken); code != http.StatusOK {
		t.Fatalf("tab add = %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 || ran[0][0] != "internal-resolve-needs" || ran[1][1] != "add" {
		t.Fatalf("ran = %v", ran)
	}
}

func TestExecRunner_ArgvNotShell(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cmdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|' \"$@\"\necho oops >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := ExecRunner(script)(context.Background(), []string{"tab", "add", "a b; echo injected"})
	if res.OK || res.Exit != 3 || res.Stdout != "tab|add|a b; echo injected|" || res.Stderr != "oops\n" {
		t.Fatalf("res = %+v", res)
	}
}

func TestEvents_InitialSnapshotThenUpdates(t *testing.T) {
	s, src, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/events?token=" + testToken
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/events", nil); err == nil {
		t.Fatal("websocket without a token must be refused")
	}
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	var ev Event
	if err := wsjson.Read(ctx, c, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "snapshot" || ev.Snapshot.Version != 1 || ev.Snapshot.Workstreams[0].Title != "first" {
		t.Fatalf("initial = %+v", ev)
	}
	s.Refresh(ctx) // unchanged: nothing is sent
	src.set("second")
	s.Refresh(ctx)
	if err := wsjson.Read(ctx, c, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Snapshot.Version != 2 || ev.Snapshot.Workstreams[0].Title != "second" {
		t.Fatalf("update = %+v", ev)
	}
	// The server answers pings (pongs are read by the client's reader).
	if err := c.Ping(c.CloseRead(ctx)); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestEnsureToken_CreatesPrivateFileAndReusesIt(t *testing.T) {
	home := t.TempDir()
	tok, err := EnsureToken(home)
	if err != nil || len(tok) != 64 {
		t.Fatalf("token %q err %v", tok, err)
	}
	info, err := os.Stat(filepath.Join(home, "api-token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	again, err := EnsureToken(home)
	if err != nil || again != tok {
		t.Fatalf("second call = %q %v, want the same token", again, err)
	}
	// A pre-existing loose file is tightened.
	if err := os.Chmod(filepath.Join(home, "api-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureToken(home); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(home, "api-token")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v after re-ensure", info.Mode().Perm())
	}
}

func TestStart_FallsThroughATakenPortAndWritesTheURL(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port

	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &fakeSource{}
	src.set("x")
	base, err := Start(ctx, home, port, New(Options{Token: testToken, Source: src.load}))
	if err != nil {
		t.Fatal(err)
	}
	if base == fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("bound the taken port %d", port)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "api-url"))
	if strings.TrimSpace(string(raw)) != base {
		t.Fatalf("api-url = %q, want %q", raw, base)
	}
	if resp := get(t, base+"/api/snapshot?token="+testToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func appServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	src := &fakeSource{}
	src.set("x")
	ts := httptest.NewServer(New(Options{Token: testToken, Source: src.load, AppDir: dir}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestApp_ServedWithoutTokenAPIStillNeedsIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Commander</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := appServer(t, dir)
	resp := get(t, ts.URL+"/app/", nil)
	if b := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(b, "Commander") {
		t.Fatalf("GET /app/ = %d %q", resp.StatusCode, b)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if resp := get(t, ts.URL+"/app", nil); resp.Request.URL.Path != "/app/" {
		t.Fatalf("/app did not redirect to /app/: %s", resp.Request.URL.Path)
	}
	if resp := get(t, ts.URL+"/api/snapshot", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/snapshot without token = %d", resp.StatusCode)
	}
}

func TestApp_NotBuiltSaysHow(t *testing.T) {
	for _, dir := range []string{"", t.TempDir()} {
		resp := get(t, appServer(t, dir).URL+"/app/", nil)
		if b := body(t, resp); resp.StatusCode != http.StatusNotFound || !strings.Contains(b, "npm run build") {
			t.Fatalf("dir %q: GET /app/ = %d %q", dir, resp.StatusCode, b)
		}
	}
}

// A client that hangs up while the first snapshot is built must not leave an
// error snapshot behind for everyone else.
func TestFirstSnapshotSurvivesACanceledRequest(t *testing.T) {
	s := New(Options{Token: "t", Source: func(ctx context.Context) livedata.Snapshot {
		if ctx.Err() != nil {
			return livedata.Snapshot{Error: "canceled"}
		}
		return livedata.Snapshot{}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.current(ctx); got.Error != "" {
		t.Fatalf("first snapshot built from a canceled request: %q", got.Error)
	}
}

// The app treats an accepted /api/events socket as "cmdrd is up" and keeps its
// held snapshot meanwhile: the first snapshot after a restart is built cold and
// can take minutes, so the upgrade must not wait for it.
func TestEvents_AcceptedBeforeTheColdFirstSnapshot(t *testing.T) {
	release := make(chan struct{})
	s := New(Options{Token: testToken, Source: func(context.Context) livedata.Snapshot {
		<-release
		return livedata.Snapshot{NeedsYou: []livedata.NeedsYou{}}
	}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/events?token="+testToken, nil)
	if err != nil {
		t.Fatalf("dial while the first snapshot is still loading: %v", err)
	}
	defer c.CloseNow()
	close(release)
	var ev Event
	if err := wsjson.Read(ctx, c, &ev); err != nil || ev.Snapshot.Version != 1 {
		t.Fatalf("first snapshot = %+v, %v", ev, err)
	}
}

func TestLastGoodProjectFailureDoesNotBumpVersion(t *testing.T) {
	s, src, _ := newTestServer(t, nil)
	s.Refresh(context.Background())
	version := s.current(context.Background()).Version
	src.mu.Lock()
	src.snap.PreserveVersion = true
	src.snap.Degraded = &livedata.SnapDegraded{Reason: "project example unreadable"}
	src.mu.Unlock()
	s.Refresh(context.Background())
	if got := s.current(context.Background()); got.Version != version || got.Degraded == nil {
		t.Fatalf("failed project version=%d want=%d degraded=%+v", got.Version, version, got.Degraded)
	}
}

func TestLastGoodDegradationEventAtSameVersion(t *testing.T) {
	s, src, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/events?token="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	var initial, event Event
	if err := wsjson.Read(ctx, c, &initial); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	src.snap.PreserveVersion = true
	src.snap.Degraded = &livedata.SnapDegraded{Reason: "project example unreadable"}
	src.mu.Unlock()
	s.Refresh(ctx)
	if err := wsjson.Read(ctx, c, &event); err != nil {
		t.Fatal(err)
	}
	if event.Snapshot.Version != initial.Snapshot.Version || event.Snapshot.Degraded == nil {
		t.Fatalf("event=%+v", event)
	}
}
