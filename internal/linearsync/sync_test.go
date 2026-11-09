package linearsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixture = `{"data":{"teams":{"nodes":[{"activeCycle":{"name":"Cycle 46","startsAt":"2026-10-28","endsAt":"2026-11-10","issues":{"nodes":[{"identifier":"TEAM-1","title":"Ship it","url":"https://linear.app/example/issue/TEAM-1","state":{"name":"In Review","type":"started"}}],"pageInfo":{"hasNextPage":false}}}}]}}}`

// teamHome is a home whose local config names a team (no team means off).
func teamHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte(teamLocal), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

const teamLocal = "[integrations.linear]\nteam='TEAM'"

func TestSprintCache(t *testing.T) {
	home := teamHome(t)
	if err := WriteKey(home, "secret-test-key"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var code atomic.Int32
	code.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "secret-test-key" {
			t.Error("missing bare key")
		}
		var payload struct {
			Query     string
			Variables map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Variables["team"] != "TEAM" || !strings.Contains(payload.Query, "isMe") || !strings.Contains(payload.Query, "activeCycle") {
			t.Error("incorrect sprint filter")
		}
		w.WriteHeader(int(code.Load()))
		io.WriteString(w, fixture)
	}))
	defer srv.Close()
	now := time.Now()
	c := New(Options{Home: home, Endpoint: srv.URL, HTTP: srv.Client().Do, Now: func() time.Time { return now }})
	a := c.Get(context.Background())
	if a.State != "ok" || a.Cycle.End != "2026-11-10" || len(a.Issues) != 1 || a.Issues[0].StatusType != "started" || a.Issues[0].HasWorkstream {
		t.Fatalf("%+v", a)
	}
	a.Issues[0].Title = "changed"
	a.Cycle.Name = "changed"
	if got := c.Get(context.Background()); got.Issues[0].Title != "Ship it" || got.Cycle.Name != "Cycle 46" || calls.Load() != 1 {
		t.Fatalf("cache: %+v calls=%d", got, calls.Load())
	}
	now = now.Add(TTL)
	code.Store(401)
	stale := c.Get(context.Background())
	if stale.State != "bad_key" || stale.StaleSince == nil || stale.FetchedAt == nil || len(stale.Issues) != 1 {
		t.Fatalf("stale: %+v", stale)
	}
	now = now.Add(TTL)
	code.Store(500)
	if got := c.Get(context.Background()); got.State != "unreachable" || !got.StaleSince.Equal(*stale.StaleSince) {
		t.Fatalf("%+v", got)
	}
	now = now.Add(TTL)
	code.Store(200)
	if got := c.Get(context.Background()); got.State != "ok" || got.StaleSince != nil {
		t.Fatalf("recovery: %+v", got)
	}
}
func TestSprintStates(t *testing.T) {
	for _, tc := range []struct {
		name, key, local, body string
		code                   int
		state                  string
	}{
		{name: "no key", state: "no_key"},
		{name: "off", local: "[integrations.linear]\nteam=''", state: "off"},
		{name: "invalid config", local: "not toml [", state: "off"},
		{name: "bad key", key: "secret", code: 401, state: "bad_key"},
		{name: "forbidden", key: "secret", code: 403, state: "bad_key"},
		{name: "unreachable", key: "secret", state: "unreachable"},
		{name: "GraphQL error", key: "secret", code: 200, body: `{"errors":[{"message":"secret"}]}`, state: "unreachable"},
		{name: "malformed", key: "secret", code: 200, body: `secret`, state: "unreachable"},
		{name: "no team", key: "secret", code: 200, body: `{"data":{"teams":{"nodes":[]}}}`, state: "unreachable"},
		{name: "no cycle", key: "secret", code: 200, body: `{"data":{"teams":{"nodes":[{"activeCycle":null}]}}}`, state: "ok"},
		{name: "truncation", key: "secret", code: 200, body: strings.Replace(fixture, `"hasNextPage":false`, `"hasNextPage":true`, 1), state: "unreachable"},
		{name: "redaction", key: "Ship-it", code: 200, body: strings.Replace(fixture, "Ship it", "Ship-it", 1), state: "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := teamHome(t)
			if tc.key != "" {
				if err := WriteKey(home, tc.key); err != nil {
					t.Fatal(err)
				}
			}
			if tc.local != "" {
				if err := os.WriteFile(filepath.Join(home, "local.toml"), []byte(tc.local), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.code == 0 {
					panic("unexpected call")
				}
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			do := srv.Client().Do
			if tc.name == "unreachable" {
				do = func(*http.Request) (*http.Response, error) { return nil, errors.New("secret in upstream error") }
			}
			c := New(Options{Home: home, Endpoint: srv.URL, HTTP: do})
			got := c.Get(context.Background())
			raw, _ := json.Marshal(got)
			if got.State != tc.state || strings.Contains(string(raw), "secret") || (tc.name == "redaction" && strings.Contains(string(raw), tc.key)) {
				t.Fatalf("%s", raw)
			}
			if tc.key == "" && calls.Load() != 0 {
				t.Fatal("called without key")
			}
		})
	}
}
func TestSingleflight(t *testing.T) {
	home := teamHome(t)
	if err := WriteKey(home, "test-key"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		io.WriteString(w, fixture)
	}))
	defer srv.Close()
	c := New(Options{Home: home, Endpoint: srv.URL, HTTP: srv.Client().Do})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := c.Get(context.Background()); got.State != "ok" {
				t.Errorf("%+v", got)
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// No team in the local config is off: no call, whether or not a key is saved.
func TestNoTeamIsOff(t *testing.T) {
	home := t.TempDir()
	if err := WriteKey(home, "test-key"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no team must not call Linear")
	}))
	defer srv.Close()
	c := New(Options{Home: home, Endpoint: srv.URL, HTTP: srv.Client().Do})
	if got := c.Get(context.Background()); got.State != "off" {
		t.Fatalf("state = %q, want off", got.State)
	}
}

func TestTeamOverride(t *testing.T) {
	home := t.TempDir()
	WriteKey(home, "test-key")
	os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam='ABC'"), 0600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"team":"ABC"`) {
			t.Error(string(raw))
		}
		io.WriteString(w, fixture)
	}))
	defer srv.Close()
	if got := New(Options{Home: home, Endpoint: srv.URL, HTTP: srv.Client().Do}).Get(context.Background()); got.State != "ok" {
		t.Fatal(got)
	}
}
func TestReadOnlyPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(raw)), "muta"+"tion") {
			t.Fatalf("write operation in %s", file)
		}
	}
}
func TestKeyPermissions(t *testing.T) {
	home := t.TempDir()
	if err := WriteKey(home, "credential"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(KeyPath(home))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	os.Chmod(KeyPath(home), 0644)
	if _, err := ReadKey(home); err == nil {
		t.Fatal("accepted loose key")
	}
	if err := WriteKey(home, "replacement"); err != nil {
		t.Fatal(err)
	}
	if key, err := ReadKey(home); err != nil || key != "replacement" {
		t.Fatal("replacement failed")
	}
	if err := WriteKey(home, "a\nb"); err == nil {
		t.Fatal("accepted multiline key")
	}
	os.Remove(KeyPath(home))
	os.Symlink(filepath.Join(home, "other"), KeyPath(home))
	if _, err := ReadKey(home); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestSprintTimeout(t *testing.T) {
	home := teamHome(t)
	if err := WriteKey(home, "timeout-secret"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); srv.Close() }()
	httpClient := srv.Client()
	httpClient.Timeout = 20 * time.Millisecond
	c := New(Options{Home: home, Endpoint: srv.URL, HTTP: httpClient.Do})
	if got := c.Get(context.Background()); got.State != "unreachable" {
		t.Fatal(got)
	}
}

func TestKeyChangesInvalidateCache(t *testing.T) {
	home := teamHome(t)
	calls := 0
	c := New(Options{Home: home, HTTP: func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fixture))}, nil
	}})
	if got := c.Get(context.Background()); got.State != "no_key" {
		t.Fatal(got)
	}
	if err := WriteKey(home, "first"); err != nil {
		t.Fatal(err)
	}
	if got := c.Get(context.Background()); got.State != "ok" || calls != 1 {
		t.Fatal(got, calls)
	}
	// Force a distinct mtime without relying on filesystem clock granularity.
	stamp := time.Now().Add(time.Hour)
	if err := os.Chtimes(KeyPath(home), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got := c.Get(context.Background()); got.State != "ok" || calls != 2 {
		t.Fatal(got, calls)
	}
	if err := os.Remove(KeyPath(home)); err != nil {
		t.Fatal(err)
	}
	if got := c.Get(context.Background()); got.State != "no_key" {
		t.Fatal(got)
	}
	// Even a cached off result is reconsidered when a key appears.
	os.WriteFile(filepath.Join(home, "local.toml"), []byte("[integrations.linear]\nteam=''"), 0600)
	WriteKey(home, "second")
	if got := c.Get(context.Background()); got.State != "off" {
		t.Fatal(got)
	}
	os.WriteFile(filepath.Join(home, "local.toml"), []byte(teamLocal), 0600)
	os.Remove(KeyPath(home))
	if got := c.Get(context.Background()); got.State != "no_key" {
		t.Fatal(got)
	}
}

func TestSingleflightPanicRecovery(t *testing.T) {
	home := teamHome(t)
	if err := WriteKey(home, "panic-secret"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	c := New(Options{Home: home, HTTP: func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		panic("panic-secret")
	}})
	results := make(chan Response, 2)
	go func() { results <- c.Get(context.Background()) }()
	<-entered
	go func() { results <- c.Get(context.Background()) }()
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			raw, _ := json.Marshal(got)
			if got.State != "unreachable" || strings.Contains(string(raw), "panic-secret") {
				t.Fatal(string(raw))
			}
		case <-time.After(time.Second):
			t.Fatal("singleflight stranded a caller")
		}
	}
	if c.flight != nil {
		t.Fatal("flight not cleared")
	}
}
