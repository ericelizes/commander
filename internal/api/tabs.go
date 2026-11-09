package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/inspector"
	"github.com/example-user/commander/internal/store"
)

// TabStore is the part of the store the inspector endpoints read.
type TabStore interface {
	InspectorWindow(ctx context.Context, projectName, wsBeadID string) (int64, string, error)
	ListTabs(ctx context.Context, projectID int64, wsBeadID string) ([]store.InspectorTab, error)
	GetTab(ctx context.Context, id int64) (*store.InspectorTab, error)
	GetActiveTab(ctx context.Context, projectID int64, wsBeadID string) (int64, error)
	GetTabCache(ctx context.Context, projectID int64, wsBeadID string) (string, time.Time, error)
	SaveTabCache(ctx context.Context, projectID int64, wsBeadID string, cacheJSON string, at time.Time) error
}

// CardFetcher builds a card for a tab target (inspector.Fetcher.Fetch).
type CardFetcher func(ctx context.Context, t inspector.Target) (inspector.Card, error)

// Tabs serves the inspector tab list and text cards (GET /api/tabs,
// GET /api/tabs/card). Mutations go through /api/cmd's `tab` verbs.
type Tabs struct {
	Store TabStore
	Fetch CardFetcher      // nil = inspector.Fetcher{}.Fetch
	Now   func() time.Time // nil = time.Now

	mu       sync.Mutex
	cards    map[int64]inspector.Card
	inflight map[int64]chan struct{}
}

// Tab views: how the app draws a tab.
const (
	ViewWeb      = "web"      // a live webview of Target
	ViewCard     = "card"     // a text card from /api/tabs/card
	ViewDecision = "decision" // a decision form (target is the step bead id)
)

// TabInfo is one tab in GET /api/tabs.
type TabInfo struct {
	ID          int64  `json:"id"`
	Position    int    `json:"position"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	Renderer    string `json:"renderer"` // as stored ("" = auto)
	Detected    string `json:"detected"` // inspector.Detect(kind, target)
	View        string `json:"view"`
	RefreshSecs int    `json:"refresh_secs"`
	AddedBy     string `json:"added_by"`
	CreatedAt   int64  `json:"created_at"`
}

// TabList is the response of GET /api/tabs.
type TabList struct {
	ProjectID int64     `json:"project_id"`
	WS        string    `json:"ws"`
	ActiveID  int64     `json:"active_id"` // 0 when there are no tabs
	Tabs      []TabInfo `json:"tabs"`
}

// CardResponse is the response of GET /api/tabs/card.
type CardResponse struct {
	TabID      int64          `json:"tab_id"`
	Card       inspector.Card `json:"card"`
	Stale      bool           `json:"stale"`
	Refreshing bool           `json:"refreshing"` // a background refresh is running; poll again
}

// textRenderers are url renderers that a tab can pin to get a card instead of
// a webview.
var textRenderers = map[string]bool{
	inspector.RendererGitHubPR: true, inspector.RendererLinear: true,
	inspector.RendererNotion: true, inspector.RendererLink: true, inspector.RendererCmd: true,
}

// tabView picks how the app draws a tab: url tabs are webviews unless their
// renderer pins a text card or the target is not http(s).
func tabView(t store.InspectorTab) string {
	switch t.Kind {
	case inspector.KindDecision:
		return ViewDecision
	case inspector.KindCmd:
		return ViewCard
	}
	if textRenderers[t.Renderer] {
		return ViewCard
	}
	// A Notion page with a token renders as its real body (title, status,
	// outline, comments); the live webview often paints only Notion's top bar.
	if t.Renderer == "" && inspector.Detect(t.Kind, t.Target) == inspector.RendererNotion && os.Getenv("NOTION_TOKEN") != "" {
		return ViewCard
	}
	u, err := url.Parse(t.Target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ViewCard
	}
	return ViewWeb
}

// cardRenderer is the renderer /api/tabs/card uses: the stored or detected
// one, never the headless-Chrome picture (the app has real webviews).
func cardRenderer(t store.InspectorTab) string {
	r := t.Renderer
	if r == "" {
		r = inspector.Detect(t.Kind, t.Target)
	}
	if r == inspector.RendererWeb || r == inspector.RendererBrowser {
		return inspector.RendererLink
	}
	return r
}

// rootBead is the workstream whose Manager owns a (possibly step) bead id, by
// the latest loaded data: the nearest nested workstream with a Manager of its
// own, else the root (tabs belong to that Manager's chat).
func (s *Server) rootBead(id string) string {
	if s.opts.LiveData != nil {
		if d, ok := s.opts.LiveData(); ok {
			return d.OwnerOf(id)
		}
	}
	return beads.Root(context.Background(), nil, "", nil, id)
}

func (s *Server) registerTabs(mux *http.ServeMux) {
	mux.Handle("GET /api/tabs", s.auth(http.HandlerFunc(s.serveTabs)))
	mux.Handle("GET /api/tabs/card", s.auth(http.HandlerFunc(s.serveTabCard)))
}

func (s *Server) tabs(w http.ResponseWriter) *Tabs {
	t := s.opts.Tabs
	if t == nil || t.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "inspector tabs not configured"})
		return nil
	}
	return t
}

func (s *Server) serveTabs(w http.ResponseWriter, r *http.Request) {
	t := s.tabs(w)
	if t == nil {
		return
	}
	q := r.URL.Query()
	ws := q.Get("ws")
	if ws != "" {
		ws = s.rootBead(ws)
	}
	list, err := t.List(r.Context(), q.Get("project"), ws)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) serveTabCard(w http.ResponseWriter, r *http.Request) {
	t := s.tabs(w)
	if t == nil {
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id must be a tab id"})
		return
	}
	force := r.URL.Query().Get("force") == "1"
	res, status, err := t.Card(r.Context(), id, force)
	if err != nil {
		writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (t *Tabs) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// List returns a window's tabs; ws "" is the Commander window.
func (t *Tabs) List(ctx context.Context, project, ws string) (TabList, error) {
	pid, ws, err := t.Store.InspectorWindow(ctx, project, ws)
	if err != nil {
		return TabList{}, err
	}
	rows, err := t.Store.ListTabs(ctx, pid, ws)
	if err != nil {
		return TabList{}, err
	}
	active, err := t.Store.GetActiveTab(ctx, pid, ws)
	if err != nil {
		return TabList{}, err
	}
	out := TabList{ProjectID: pid, WS: ws, Tabs: []TabInfo{}}
	found := false
	for _, row := range rows {
		out.Tabs = append(out.Tabs, TabInfo{
			ID: row.ID, Position: row.Position, Title: row.Title, Kind: row.Kind, Target: row.Target,
			Renderer: row.Renderer, Detected: inspector.Detect(row.Kind, row.Target), View: tabView(row),
			RefreshSecs: row.RefreshSecs, AddedBy: row.AddedBy, CreatedAt: row.CreatedAt.Unix(),
		})
		found = found || row.ID == active
	}
	if found {
		out.ActiveID = active
	} else if len(rows) > 0 {
		out.ActiveID = rows[0].ID // same fallback as the TUI
	}
	return out, nil
}

// Card returns the tab's text card. A missing card is fetched now; a stale one
// is returned at once while a background refresh runs; force fetches now.
func (t *Tabs) Card(ctx context.Context, id int64, force bool) (CardResponse, int, error) {
	tab, err := t.Store.GetTab(ctx, id)
	if err != nil {
		return CardResponse{}, http.StatusInternalServerError, err
	}
	if tab == nil {
		return CardResponse{}, http.StatusNotFound, errors.New("no such tab")
	}
	if tab.Kind == inspector.KindDecision {
		return CardResponse{}, http.StatusBadRequest, errors.New("decision tabs are forms, not cards")
	}
	card, have := t.cached(ctx, *tab)
	stale := !have || inspector.Stale(card.FetchedAt, tab.RefreshSecs, t.now())
	switch {
	case !have || force:
		card = t.refresh(*tab, true)
		return CardResponse{TabID: id, Card: card}, http.StatusOK, nil
	case stale:
		t.refresh(*tab, false)
		return CardResponse{TabID: id, Card: card, Stale: true, Refreshing: true}, http.StatusOK, nil
	}
	return CardResponse{TabID: id, Card: card}, http.StatusOK, nil
}

// cached returns the in-memory card, else the store's cache when tab is its
// window's active tab (the store keeps one card per window, like the TUI).
func (t *Tabs) cached(ctx context.Context, tab store.InspectorTab) (inspector.Card, bool) {
	t.mu.Lock()
	card, ok := t.cards[tab.ID]
	t.mu.Unlock()
	if ok {
		return card, true
	}
	if !t.isActive(ctx, tab) {
		return inspector.Card{}, false
	}
	js, at, err := t.Store.GetTabCache(ctx, tab.ProjectID, tab.WorkstreamBeadID)
	if err != nil || js == "" {
		return inspector.Card{}, false
	}
	card, err = inspector.Unmarshal(js)
	if err != nil {
		return inspector.Card{}, false
	}
	if card.FetchedAt.IsZero() {
		card.FetchedAt = at
	}
	return card, true
}

func (t *Tabs) isActive(ctx context.Context, tab store.InspectorTab) bool {
	active, err := t.Store.GetActiveTab(ctx, tab.ProjectID, tab.WorkstreamBeadID)
	if err != nil {
		return false
	}
	if active == 0 {
		rows, err := t.Store.ListTabs(ctx, tab.ProjectID, tab.WorkstreamBeadID)
		if err != nil || len(rows) == 0 {
			return false
		}
		active = rows[0].ID
	}
	return active == tab.ID
}

// refresh fetches tab's card once at a time per tab. With wait it blocks until
// the card is ready and returns it; otherwise it returns at once.
func (t *Tabs) refresh(tab store.InspectorTab, wait bool) inspector.Card {
	t.mu.Lock()
	if t.cards == nil {
		t.cards, t.inflight = map[int64]inspector.Card{}, map[int64]chan struct{}{}
	}
	done, running := t.inflight[tab.ID]
	if !running {
		done = make(chan struct{})
		t.inflight[tab.ID] = done
		go t.fetch(tab, done)
	}
	t.mu.Unlock()
	if !wait {
		return inspector.Card{}
	}
	<-done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cards[tab.ID]
}

// fetch runs detached from the request so a closed client does not waste a
// slow (Notion/Linear) fetch.
func (t *Tabs) fetch(tab store.InspectorTab, done chan struct{}) {
	fetch := t.Fetch
	if fetch == nil {
		fetch = inspector.Fetcher{}.Fetch
	}
	card, _ := fetch(context.Background(), inspector.Target{
		Kind: tab.Kind, Target: tab.Target, Renderer: cardRenderer(tab), Title: tab.Title,
	})
	if card.FetchedAt.IsZero() {
		card.FetchedAt = t.now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if card.Err == "" && t.isActive(ctx, tab) {
		if js, err := inspector.Marshal(card); err == nil {
			_ = t.Store.SaveTabCache(ctx, tab.ProjectID, tab.WorkstreamBeadID, js, card.FetchedAt)
		}
	}
	t.mu.Lock()
	t.cards[tab.ID] = card
	delete(t.inflight, tab.ID)
	t.mu.Unlock()
	close(done)
}
