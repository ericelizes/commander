package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// fakeTabStore is an in-memory TabStore: one workstream window "w-1" in
// project "p" (id 7) plus the Commander window.
type fakeTabStore struct {
	mu      sync.Mutex
	tabs    []store.InspectorTab
	active  map[string]int64
	cache   map[string]string
	cacheAt map[string]time.Time
}

func newFakeTabStore(tabs ...store.InspectorTab) *fakeTabStore {
	return &fakeTabStore{tabs: tabs, active: map[string]int64{}, cache: map[string]string{}, cacheAt: map[string]time.Time{}}
}

func winKey(pid int64, ws string) string { return ws + "@" + string(rune('0'+pid)) }

func (f *fakeTabStore) InspectorWindow(_ context.Context, project, ws string) (int64, string, error) {
	switch {
	case ws == "" && project == "":
		return 0, "", nil
	case ws == "w-1":
		return 7, "w-1", nil
	}
	return 0, "", errors.New("workstream has no Manager yet")
}

func (f *fakeTabStore) ListTabs(_ context.Context, pid int64, ws string) ([]store.InspectorTab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.InspectorTab
	for _, t := range f.tabs {
		if t.ProjectID == pid && t.WorkstreamBeadID == ws {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTabStore) GetTab(_ context.Context, id int64) (*store.InspectorTab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tabs {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, nil
}

func (f *fakeTabStore) GetActiveTab(_ context.Context, pid int64, ws string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[winKey(pid, ws)], nil
}

func (f *fakeTabStore) GetTabCache(_ context.Context, pid int64, ws string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cache[winKey(pid, ws)], f.cacheAt[winKey(pid, ws)], nil
}

func (f *fakeTabStore) SaveTabCache(_ context.Context, pid int64, ws, js string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache[winKey(pid, ws)], f.cacheAt[winKey(pid, ws)] = js, at
	return nil
}

func testTabs() []store.InspectorTab {
	return []store.InspectorTab{
		{ID: 1, ProjectID: 7, WorkstreamBeadID: "w-1", Position: 1, Title: "Spec", Kind: "url", Target: "https://www.notion.so/acme/Spec-abc", RefreshSecs: 300},
		{ID: 2, ProjectID: 7, WorkstreamBeadID: "w-1", Position: 2, Title: "tests", Kind: "cmd", Target: "go test ./...", RefreshSecs: 10},
		{ID: 3, ProjectID: 7, WorkstreamBeadID: "w-1", Position: 3, Title: "PR #4", Kind: "url", Target: "https://github.com/o/r/pull/4", Renderer: "github-pr", RefreshSecs: 60},
		{ID: 4, ProjectID: 7, WorkstreamBeadID: "w-1", Position: 4, Title: "⚑ w-1.2", Kind: "decision", Target: "w-1.2", RefreshSecs: 60},
		{ID: 9, Position: 1, Title: "Datadog", Kind: "url", Target: "https://app.datadoghq.com/", RefreshSecs: 60},
	}
}

// countingFetcher returns a card titled by the target and counts calls.
type countingFetcher struct {
	calls   atomic.Int32
	targets sync.Map // target -> renderer
	gate    chan struct{}
}

func (c *countingFetcher) fetch(_ context.Context, t inspector.Target) (inspector.Card, error) {
	c.calls.Add(1)
	c.targets.Store(t.Target, t.Renderer)
	if c.gate != nil {
		<-c.gate
	}
	return inspector.Card{Renderer: t.Renderer, Title: "card " + t.Target, Markdown: "**hi**"}, nil
}

func newTabsServer(t *testing.T, st *fakeTabStore, f *countingFetcher, now func() time.Time) *httptest.Server {
	t.Helper()
	src := &fakeSource{}
	src.set("x")
	s := New(Options{Token: testToken, Source: src.load, Tabs: &Tabs{Store: st, Fetch: f.fetch, Now: now}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp := get(t, url, map[string]string{"Authorization": "Bearer " + testToken})
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestTabs_ListWindow(t *testing.T) {
	st := newFakeTabStore(testTabs()...)
	ts := newTabsServer(t, st, &countingFetcher{}, nil)

	var list TabList
	if code := getJSON(t, ts.URL+"/api/tabs?ws=w-1.3&project=p", &list); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if list.WS != "w-1" || list.ProjectID != 7 {
		t.Fatalf("window = %q/%d, want w-1/7 (step ids resolve to their workstream)", list.WS, list.ProjectID)
	}
	if len(list.Tabs) != 4 {
		t.Fatalf("got %d tabs, want 4", len(list.Tabs))
	}
	if list.ActiveID != 1 {
		t.Errorf("active = %d, want first tab when none recorded", list.ActiveID)
	}
	wantView := map[int64]string{1: ViewWeb, 2: ViewCard, 3: ViewCard, 4: ViewDecision}
	for _, tab := range list.Tabs {
		if tab.View != wantView[tab.ID] {
			t.Errorf("tab %d view = %q, want %q", tab.ID, tab.View, wantView[tab.ID])
		}
	}
	if list.Tabs[0].Detected != inspector.RendererNotion {
		t.Errorf("detected = %q, want notion", list.Tabs[0].Detected)
	}

	st.active[winKey(7, "w-1")] = 3
	getJSON(t, ts.URL+"/api/tabs?ws=w-1", &list)
	if list.ActiveID != 3 {
		t.Errorf("active = %d, want 3", list.ActiveID)
	}

	getJSON(t, ts.URL+"/api/tabs", &list)
	if len(list.Tabs) != 1 || list.Tabs[0].ID != 9 {
		t.Errorf("commander window tabs = %+v", list.Tabs)
	}
	if code := getJSON(t, ts.URL+"/api/tabs?ws=nope", nil); code != http.StatusBadRequest {
		t.Errorf("unknown ws status = %d, want 400", code)
	}
}

func TestTabs_AuthAndUnconfigured(t *testing.T) {
	ts := newTabsServer(t, newFakeTabStore(), &countingFetcher{}, nil)
	if resp := get(t, ts.URL+"/api/tabs", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	_, _, plain := newTestServer(t, nil)
	if code := getJSON(t, plain.URL+"/api/tabs", nil); code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured: %d, want 503", code)
	}
}

func TestTabs_CardFetchCacheAndRefresh(t *testing.T) {
	st := newFakeTabStore(testTabs()...)
	f := &countingFetcher{}
	now := time.Now()
	var clock atomic.Int64
	clock.Store(now.Unix())
	ts := newTabsServer(t, st, f, func() time.Time { return time.Unix(clock.Load(), 0) })

	var res CardResponse
	if code := getJSON(t, ts.URL+"/api/tabs/card?id=1", &res); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if res.Card.Title != "card https://www.notion.so/acme/Spec-abc" || res.Stale || res.Refreshing {
		t.Fatalf("first fetch = %+v", res)
	}
	if r, _ := f.targets.Load("https://www.notion.so/acme/Spec-abc"); r != inspector.RendererNotion {
		t.Errorf("renderer = %v, want the notion digest (never the browser picture)", r)
	}
	// Tab 1 is the window's active tab (first by default): the card is cached in the store.
	if st.cache[winKey(7, "w-1")] == "" {
		t.Error("active tab's card not saved to the store cache")
	}

	// Fresh: served from memory, no new fetch.
	getJSON(t, ts.URL+"/api/tabs/card?id=1", &res)
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (fresh card cached)", f.calls.Load())
	}

	// Stale after refresh_secs: the old card at once, a background refresh.
	clock.Add(301)
	getJSON(t, ts.URL+"/api/tabs/card?id=1", &res)
	if !res.Stale || !res.Refreshing || res.Card.Title == "" {
		t.Fatalf("stale response = %+v", res)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("calls = %d, want a background refresh", f.calls.Load())
	}

	// force fetches synchronously.
	getJSON(t, ts.URL+"/api/tabs/card?id=1&force=1", &res)
	if f.calls.Load() != 3 || res.Refreshing {
		t.Errorf("force: calls=%d res=%+v", f.calls.Load(), res)
	}
}

func TestTabs_CardSeededFromStoreCache(t *testing.T) {
	st := newFakeTabStore(testTabs()...)
	st.active[winKey(7, "w-1")] = 2
	js, _ := inspector.Marshal(inspector.Card{Title: "cached", FetchedAt: time.Now()})
	st.cache[winKey(7, "w-1")] = js
	f := &countingFetcher{}
	ts := newTabsServer(t, st, f, nil)

	var res CardResponse
	getJSON(t, ts.URL+"/api/tabs/card?id=2", &res)
	if res.Card.Title != "cached" || f.calls.Load() != 0 {
		t.Errorf("got %q with %d fetches, want the store cache and none", res.Card.Title, f.calls.Load())
	}
	// Tab 3 is not active, so the window's cache is not its card.
	getJSON(t, ts.URL+"/api/tabs/card?id=3", &res)
	if res.Card.Title != "card https://github.com/o/r/pull/4" {
		t.Errorf("tab 3 card = %q", res.Card.Title)
	}
	if st.cache[winKey(7, "w-1")] != js {
		t.Error("an inactive tab's fetch overwrote the window cache")
	}
}

func TestTabs_CardErrors(t *testing.T) {
	ts := newTabsServer(t, newFakeTabStore(testTabs()...), &countingFetcher{}, nil)
	cases := map[string]int{
		"/api/tabs/card":       http.StatusBadRequest,
		"/api/tabs/card?id=x":  http.StatusBadRequest,
		"/api/tabs/card?id=99": http.StatusNotFound,
		"/api/tabs/card?id=4":  http.StatusBadRequest, // decision
	}
	for path, want := range cases {
		if code := getJSON(t, ts.URL+path, nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

func TestTabs_ConcurrentFetchesShareOne(t *testing.T) {
	f := &countingFetcher{gate: make(chan struct{})}
	ts := newTabsServer(t, newFakeTabStore(testTabs()...), f, nil)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var res CardResponse
			getJSON(t, ts.URL+"/api/tabs/card?id=2", &res)
			if res.Card.Title != "card go test ./..." {
				t.Errorf("card = %q", res.Card.Title)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 shared fetch", f.calls.Load())
	}
}

func TestTabView(t *testing.T) {
	cases := []struct {
		tab  store.InspectorTab
		want string
	}{
		{store.InspectorTab{Kind: "url", Target: "https://linear.app/x/issue/AB-1"}, ViewWeb},
		{store.InspectorTab{Kind: "url", Target: "https://linear.app/x/issue/AB-1", Renderer: "linear"}, ViewCard},
		{store.InspectorTab{Kind: "url", Target: "https://example.com", Renderer: "browser"}, ViewWeb},
		{store.InspectorTab{Kind: "url", Target: "file:///etc/passwd"}, ViewCard},
		{store.InspectorTab{Kind: "cmd", Target: "ls"}, ViewCard},
		{store.InspectorTab{Kind: "decision", Target: "b-1"}, ViewDecision},
	}
	for _, c := range cases {
		if got := tabView(c.tab); got != c.want {
			t.Errorf("tabView(%+v) = %q, want %q", c.tab, got, c.want)
		}
	}
	if r := cardRenderer(store.InspectorTab{Kind: "url", Target: "https://example.com"}); r != inspector.RendererLink {
		t.Errorf("web tab card renderer = %q, want link", r)
	}
}

// A step promoted to its own workstream keeps its dotted id; its tabs are that
// workstream's, by the parent edges in the loaded data.
func TestTabs_RootBeadFollowsLoadedParents(t *testing.T) {
	s := New(Options{Token: testToken, LiveData: func() (livedata.Data, bool) {
		return livedata.Data{Objectives: []livedata.ObjectiveRow{
			{BeadID: "w-1"}, {BeadID: "w-1.2", ParentBeadID: "w-1"},
			{BeadID: "w-1.8"}, {BeadID: "w-1.8.4", ParentBeadID: "w-1.8"},
		}}, true
	}})
	for in, want := range map[string]string{"w-1.8.4": "w-1.8", "w-1.8": "w-1.8", "w-1.2": "w-1", "w-1.2.9": "w-1"} {
		if got := s.rootBead(in); got != want {
			t.Errorf("rootBead(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTabViewNotionPrefersStructuredWithToken(t *testing.T) {
	urls := []string{
		"https://app.notion.com/p/0123456789abcdef0123456789abcdef",
		"http://www.notion.so/Spec-0123456789abcdef0123456789abcdef",
		"https://acme.notion.site/Spec-0123456789abcdef0123456789abcdef",
		"https://acme.notion.so/Spec-0123456789abcdef0123456789abcdef",
	}
	for _, u := range urls {
		if got := inspector.Detect("url", u); got != inspector.RendererNotion {
			t.Errorf("Detect(%s) = %q", u, got)
		}
		tab := store.InspectorTab{Kind: "url", Target: u}
		t.Setenv("NOTION_TOKEN", "")
		if got := tabView(tab); got != ViewWeb {
			t.Errorf("no token: tabView(%s) = %q, want web", u, got)
		}
		t.Setenv("NOTION_TOKEN", "secret")
		if got := tabView(tab); got != ViewCard {
			t.Errorf("token: tabView(%s) = %q, want card", u, got)
		}
		tab.Renderer = "browser"
		if got := tabView(tab); got != ViewWeb {
			t.Errorf("pinned browser: tabView(%s) = %q, want web", u, got)
		}
	}
}
