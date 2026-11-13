// Package api is cmdrd's local HTTP API: the cockpit state as JSON
// (/api/snapshot), pushed over a WebSocket when it changes (/api/events), and
// an allow-listed way to run cmdr commands (/api/cmd). It binds to loopback and
// every /api request needs the token in $COMMANDER_HOME/api-token.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/overview"
	"github.com/example-user/commander/internal/place"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// DefaultInterval is how often the sources are polled for a changed snapshot.
const DefaultInterval = time.Second

// pingInterval keeps idle WebSocket connections alive and detects dead peers.
const pingInterval = 20 * time.Second

// Options configures a Server.
type Options struct {
	AutomaticGateRun gates.RunFn // optional automatic-check runner; production uses bounded gates.RunShell
	// WorkflowGH overrides only external GitHub reads in the shared close verifier.
	WorkflowGH workflowtemplate.GHRunner
	Changes    <-chan struct{} // committed database notifications; nil retains legacy polling
	StateStore *store.Store    // canonical Postgres state and replay endpoint

	OverviewGenerate overview.Generate  // optional read-only recap reader; nil uses the Commander Claude account
	Linear           *linearsync.Client // optional injected sprint reader
	Token            string
	Source           func(ctx context.Context) livedata.Snapshot // Version is set by the server
	Run              Runner                                      // runs allow-listed cmdr argv
	Interval         time.Duration                               // poll interval; 0 = DefaultInterval
	LiveData         func() (livedata.Data, bool)                // latest loaded Data for /api/livedata; nil = 503
	FinishBoard      func(*livedata.Snapshot, *livedata.Display) // a full build's finishing pass, for boards rebuilt after a write
	// MailToRole delivers a message the user typed in the app to a role that has no native
	// process yet (still a terminal chat): through the mail path, as the user. nil = none.
	MailToRole func(ctx context.Context, role, text string) error
	// WaitingMail lists role's unread mail (the app's "N waiting" popup). nil = 503.
	WaitingMail func(ctx context.Context, role string) ([]agentmail.Mail, error)
	AppDir      string // built app renderer (app/dist) served at /app/; "" = none
	// AppDevURL is the renderer's live Vite dev server: /app/ proxies to it
	// (HMR included) while it answers, else serves AppDir. "" = AppDir only.
	AppDevURL string
	Tabs      *Tabs        // inspector tabs (/api/tabs); nil = 503
	Pitches   PitchStore   // every version of every pitch (/api/pitches); nil = 503
	Chat      *chat.Engine // native chat engine; nil = /api/chat answers 503
	// ShotsDir is where /api/app/screenshot writes app.png and app-text.txt;
	// "" = 503.
	ShotsDir string
	// OpenTerminal runs a command in a new terminal window on the user's
	// screen (an account's login); nil = OpenLoginTerminal.
	OpenTerminal func(ctx context.Context, command string) error
	// ConfigPath is config.toml, read and edited by /api/settings; "" = 503.
	ConfigPath string
	// UsageByAgent is token use per account, agent and model since weekSince,
	// with the part since todaySince (unix s), for /api/usage/agents; nil = 503.
	UsageByAgent func(ctx context.Context, todaySince, weekSince int64) ([]store.AgentUsage, error)
	// Update serves /api/update (status, install); nil = 503.
	Update *Updater
	// Write runs a br write for /api/write; nil = beads.RunBr.
	Write WriteRunner
	// Batch runs a bd batch script for /api/write ops; nil = beads.RunBatch (bd only).
	Batch BatchRunner
	// Beads reads the bead state the write checks need; nil = beads.New().
	Beads beads.Client
	// LiveOwner answers whether an agent role runs, for an intent's holder check; nil = every
	// owner is taken as live (the check cannot say otherwise).
	LiveOwner func(owner string) (state string, known bool)
	// Places serves /api/places (paired Macs and their runs); nil = 503.
	Places *place.Service
	// PlacesHome holds pending pairing codes ($COMMANDER_HOME).
	PlacesHome string
}

// Server holds the latest snapshot and fans changes out to WebSocket clients.
type Server struct {
	automaticGateChecks automaticGateFlights
	overviews           *overview.Cache
	overviewKick        atomic.Bool // a background first load for Overview is under way
	signins             signins
	opts                Options

	refreshMu    sync.Mutex  // one Source call at a time
	loadKept     atomic.Bool // the latest load was degraded, served last-good rows or was dropped
	loadStarted  time.Time   // when the latest Source call began (under refreshMu)
	failingSince time.Time   // first failed live-state read of the current streak (under refreshMu)

	slowLoad      time.Duration  // the load time worth a log line
	refreshBudget time.Duration  // bounds the load a settle starts
	settling      sync.WaitGroup // the loads settles started, which can outlive settleWait

	mu      sync.Mutex
	pending map[string]pendingPatch // writes the door answered that no load has read back yet
	cur     livedata.Snapshot
	hash    [32]byte
	version uint64
	subs    map[chan struct{}]struct{}

	appMu      sync.Mutex
	appClients []*appClient // connected app windows, oldest first
}

// New returns a Server; call Poll to keep it fresh.
func New(opts Options) *Server {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Linear == nil && opts.ConfigPath != "" {
		opts.Linear = linearsync.New(linearsync.Options{Home: filepath.Dir(opts.ConfigPath)})
	}
	machineHistory.start()
	return &Server{opts: opts, subs: map[chan struct{}]struct{}{}, overviews: overview.New(overviewGenerator(opts)), slowLoad: defaultSlowLoad, refreshBudget: defaultRefreshBudget}
}

// Refresh loads a snapshot and, when its content differs from the last one,
// bumps the version and notifies subscribers. It reports whether it changed.
func (s *Server) Refresh(ctx context.Context) bool {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(beads.WithReadCaller(ctx, "api-refresh"))
}

func (s *Server) refreshLocked(ctx context.Context) bool {
	s.loadStarted = time.Now()
	snap := s.opts.Source(ctx)
	complete := ctx.Err() == nil && snap.Degraded == nil && snap.Error == "" && !snap.KeptLastGood && !snap.PreserveVersion
	// Publish load evidence even when content is unchanged or a failed read
	// retains the previous rows. Connection pings are never load evidence.
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if complete {
			s.cur.LoadCompletedAt = time.Now()
		}
		s.cur.LoadError = !complete
		for ch := range s.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	if s.opts.StateStore != nil && (ctx.Err() != nil || snap.Degraded != nil || snap.KeptLastGood || snap.PreserveVersion) {
		now := time.Now()
		if s.failingSince.IsZero() {
			s.failingSince = now
		}
		// One slow read under load is not an outage: keep retrying (Poll runs
		// every second) and report unavailable only once reads keep failing.
		s.mu.Lock()
		healthy := s.version > 0 && s.cur.Degraded == nil
		s.mu.Unlock()
		if healthy && now.Sub(s.failingSince) < unavailableGrace {
			s.loadKept.Store(true)
			log.Printf("api: live state read failed after %s; retrying before reporting it unavailable", time.Since(s.loadStarted).Round(time.Millisecond))
			return false
		}
		snap = unavailablePostgresSnapshot()
	} else {
		s.failingSince = time.Time{}
	}
	// Its own context ended (a settle's budget, a shutdown): what it read is not
	// the board. Keep the one we have; a settle answers settled:false.
	if err := ctx.Err(); err != nil && s.opts.StateStore == nil {
		s.loadKept.Store(true)
		log.Printf("api: snapshot load ended after %s (%v), kept the last board (%s)", time.Since(s.loadStarted).Round(time.Millisecond), err, snap.LoadTimes())
		return false
	}
	if took := time.Since(s.loadStarted); took > s.slowLoad {
		log.Printf("api: snapshot load took %s (%s)", took.Round(time.Millisecond), snap.LoadTimes())
	}
	kept := snap.PreserveVersion || snap.KeptLastGood || snap.Degraded != nil
	s.loadKept.Store(kept)

	s.mu.Lock()
	defer s.mu.Unlock()
	// A clean load that began after a write has read it: its patch is done. A load that kept the
	// last rows (or any load begun before the write) shows the patched rows, not the pre-write ones.
	for id, p := range s.pending {
		if !kept && p.at.Before(s.loadStarted) || time.Since(p.at) > pendingPatchTTL {
			delete(s.pending, id)
		}
	}
	if s.opts.StateStore != nil && snap.Degraded != nil {
		s.pending = map[string]pendingPatch{}
	}
	snap = livedata.Patched(snap, s.pendingPatches(), time.Now().Unix(), s.opts.FinishBoard)
	snap.LoadCompletedAt, snap.LoadError = s.cur.LoadCompletedAt, s.cur.LoadError
	h := contentHash(snap)
	if s.version > 0 && h == s.hash {
		return false
	}
	if !snap.PreserveVersion || s.version == 0 {
		s.version++
	}
	snap.Version = s.version
	s.cur, s.hash = snap, h
	return true
}

const defaultSlowLoad = livedata.SlowLoad

// liveReadTimeout bounds one live-state build; changeBatch gathers a burst of
// database changes into one rebuild.
const (
	liveReadTimeout = 8 * time.Second
	changeBatch     = 500 * time.Millisecond
	minRebuildGap   = time.Second
)

// boardBlind are tables whose changes the board never reads: token usage and
// supervision bookkeeping. A burst made only of these needs no rebuild.
var boardBlind = map[string]bool{"usage_turns": true, "bead_supervision": true, "supervision_attempts": true}

func boardReadsAny(entities []string) bool {
	if len(entities) == 0 {
		return true // a notification without a recorded event: rebuild to be safe
	}
	for _, e := range entities {
		if !boardBlind[e] {
			return true
		}
	}
	return false
}

// latestStateSeq is the newest state event's sequence number (0 when none).
func (s *Server) latestStateSeq(ctx context.Context) int64 {
	var seq int64
	_ = s.opts.StateStore.DB().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM state_events`).Scan(&seq)
	return seq
}

// unavailableGrace is how long live-state reads may keep failing before the
// app is told the state is unavailable.
const unavailableGrace = 10 * time.Second

// pendingPatchTTL bounds how long a patch outlives loads that kept the last rows (degraded, busy):
// past it the bead reads as the loads show it, so a later write to it (one with no intent, no patch
// of its own) can never stay hidden behind an old patch (m7).
const pendingPatchTTL = 2 * time.Minute

// pendingPatch is one bead's state after a write the door answered, and when.
type pendingPatch struct {
	patch livedata.StepPatch
	at    time.Time
}

func (s *Server) pendingPatches() map[string]livedata.StepPatch {
	out := make(map[string]livedata.StepPatch, len(s.pending))
	for id, p := range s.pending {
		out[id] = p.patch
	}
	return out
}

// patchNow lays a write's result on the current board and publishes it (a new version), so no
// reader after the door's answer sees the board from before the write. Pending until a clean load
// that began after it.
func (s *Server) patchNow(after map[string]livedata.StepPatch) {
	if len(after) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]pendingPatch{}
	}
	now := time.Now()
	for id, p := range after {
		s.pending[id] = pendingPatch{patch: p, at: now}
	}
	if s.version == 0 {
		return // nothing published yet: the first load shows it
	}
	snap := livedata.Patched(s.cur, s.pendingPatches(), now.Unix(), s.opts.FinishBoard)
	h := contentHash(snap)
	if h == s.hash {
		return
	}
	s.version++
	snap.Version = s.version
	s.cur, s.hash = snap, h
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// contentHash ignores the fields that change on every load.
func contentHash(s livedata.Snapshot) [32]byte {
	s.Version, s.GeneratedAt = 0, time.Time{}
	s.LoadCompletedAt, s.LoadError = time.Time{}, false
	raw, _ := json.Marshal(s)
	return sha256.Sum256(raw)
}

// Poll refreshes every interval until ctx is done.
func (s *Server) Poll(ctx context.Context) {
	changes := s.opts.Changes
	var cursor int64
	if s.opts.StateStore != nil {
		cursor = s.latestStateSeq(ctx)
	}
	skip := false
	var lastRefresh time.Time
	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()
	for {
		if !skip {
			lastRefresh = time.Now()
			readCtx := ctx
			cancel := func() {}
			if s.opts.StateStore != nil {
				readCtx, cancel = context.WithTimeout(ctx, liveReadTimeout)
			}
			s.Refresh(readCtx)
			cancel()
		}
		skip = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case _, ok := <-changes:
			if ok && s.opts.StateStore != nil {
				entities, next, err := s.opts.StateStore.StateEntitiesSince(ctx, cursor)
				if err == nil {
					cursor = next
					if !boardReadsAny(entities) {
						skip = true
						continue
					}
				}
			}
			if !ok {
				changes = nil
			} else {
				// Gather the burst, and leave at least minRebuildGap between
				// rebuilds: changes arrive several times a second.
				wait := max(changeBatch, minRebuildGap-time.Since(lastRefresh))
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			drain:
				for {
					select {
					case _, ok := <-changes:
						if !ok {
							changes = nil
							break drain
						}
					default:
						break drain
					}
				}
			}
		}
	}
}

// current returns the latest snapshot, loading one if none exists yet.
func (s *Server) current(ctx context.Context) livedata.Snapshot {
	s.mu.Lock()
	have := s.version > 0
	s.mu.Unlock()
	if !have {
		// Not the request's own context: a client that hangs up mid-build would
		// cancel it, and the failed (empty) snapshot would be served to everyone.
		s.Refresh(context.WithoutCancel(ctx))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *Server) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

// Handler routes the API. "/" is an unauthenticated placeholder page.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", servePlaceholder)
	mux.Handle("POST /api/overview", s.auth(http.HandlerFunc(s.serveOverview)))
	mux.Handle("GET /api/snapshot", s.auth(http.HandlerFunc(s.serveSnapshot)))
	mux.Handle("GET /api/events", s.auth(http.HandlerFunc(s.serveEvents)))
	mux.Handle("GET /api/state/events", s.auth(http.HandlerFunc(s.serveStateEvents)))
	mux.Handle("GET /api/livedata", s.auth(http.HandlerFunc(s.serveLiveData)))
	mux.Handle("POST "+beads.AutomaticGatePath, s.auth(http.HandlerFunc(s.serveAutomaticGateCheck)))
	mux.Handle("POST "+beads.PublicationPath, s.auth(http.HandlerFunc(s.servePublication)))
	mux.Handle("POST "+DecisionReconciliationPath, s.auth(http.HandlerFunc(s.serveDecisionReconciliation)))
	mux.Handle("POST "+beads.DoorPath, s.auth(http.HandlerFunc(s.serveWrite)))
	mux.Handle("POST "+beads.PlanPath, s.auth(http.HandlerFunc(s.serveWrite)))
	mux.Handle("POST "+beads.PlanProposePath, s.auth(http.HandlerFunc(s.serveWrite)))
	mux.Handle("POST /api/cmd", s.auth(http.HandlerFunc(s.serveCmd)))
	mux.Handle("GET /app", http.RedirectHandler("/app/", http.StatusMovedPermanently))
	mux.Handle("GET /app/", appHandler(s.opts.AppDir, s.opts.AppDevURL))
	s.registerTabs(mux)
	s.registerAppShot(mux)
	s.chatRoutes(mux)
	s.jurisdictionRoutes(mux)
	s.settingsRoutes(mux)
	mux.Handle("POST /api/linear/key", s.auth(http.HandlerFunc(s.serveLinearKey)))
	mux.Handle("DELETE /api/linear/key", s.auth(http.HandlerFunc(s.serveLinearKey)))
	mux.Handle("GET /api/linear", s.auth(http.HandlerFunc(s.serveLinear)))
	mux.Handle("GET /api/workflows", s.auth(http.HandlerFunc(s.serveWorkflows)))
	// Report files for workstream tabs. The app's tab browser cannot send the
	// token; a report's name is its content hash, unguessable, on loopback.
	mux.Handle("GET /reports/{name}", http.HandlerFunc(s.serveReport))
	s.machineRoutes(mux)
	s.updateRoutes(mux)
	s.placeRoutes(mux)
	mux.Handle("GET /api/usage/agents", s.auth(http.HandlerFunc(s.serveUsageAgents)))
	mux.Handle("GET /api/pitches", s.auth(http.HandlerFunc(s.servePitches)))
	return cors(mux)
}

// localOrigin reports whether a browser Origin is a page served from this
// machine (http://localhost:*, http://127.0.0.1:*).
func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1":
		return true
	}
	return false
}

// cors allows only local origins: their requests get CORS headers and
// preflights succeed; any other Origin is refused outright.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !localOrigin(origin) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+OriginHeader)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth requires the token as "Authorization: Bearer <token>" or ?token=. A POST
// takes the header only: a query string leaks into logs and proxies.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := ""
		if r.Method != http.MethodPost {
			got = r.URL.Query().Get("token")
		}
		if h, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = strings.TrimSpace(h)
		}
		if s.opts.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "missing or bad token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	snap := s.current(r.Context())
	status := http.StatusOK
	if s.opts.StateStore != nil && snap.Degraded != nil {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, snap)
}

// Event is a full snapshot WebSocket frame. Unchanged content uses a separate
// freshness frame carrying load_completed_at and load_error only.
type Event struct {
	Type     string            `json:"type"`
	Snapshot livedata.Snapshot `json:"snapshot"`
}

// serveEvents sends the snapshot on connect and again on every change.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"},
	})
	if err != nil {
		return // Accept already wrote the error response
	}
	defer c.CloseNow()

	sub := s.subscribe()
	defer s.unsubscribe(sub)
	ctx := c.CloseRead(r.Context()) // handles pings/close from the client

	var sent uint64
	var sentHash [32]byte
	var sentLoad time.Time
	var sentError bool
	send := func() error {
		snap := s.current(ctx)
		hash := contentHash(snap)
		if snap.Version == sent && hash == sentHash {
			if snap.LoadCompletedAt.Equal(sentLoad) && snap.LoadError == sentError {
				return nil
			}
			sentLoad, sentError = snap.LoadCompletedAt, snap.LoadError
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return wsjson.Write(wctx, c, map[string]any{"type": "freshness", "load_completed_at": sentLoad, "load_error": sentError})
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		sent, sentHash = snap.Version, hash
		sentLoad, sentError = snap.LoadCompletedAt, snap.LoadError
		return wsjson.Write(wctx, c, Event{Type: "snapshot", Snapshot: snap})
	}
	if send() != nil {
		return
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub:
			if send() != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// appFiles serves the built app renderer as a plain page. The files are not
// secret (no token inside), so they need no auth; the page gets the token
// from its one-time link (#token=) and every /api call still checks it.
func appFiles(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dir == "" {
			http.Error(w, "app not built: run `cd app && npm i && npm run build`", http.StatusNotFound)
			return
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			http.Error(w, "app not built: run `cd app && npm i && npm run build`", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const placeholderPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Commander API</title>
<style>
:root { color-scheme: light dark; }
body { margin: 0; padding: 24px 16px; font: 15px/1.5 ui-monospace, Menlo, monospace; background: Canvas; color: CanvasText; }
code { font-weight: 600; }
</style></head>
<body>
<h1>cockpit api up</h1>
<p>Every <code>/api</code> request needs the token from <code>$COMMANDER_HOME/api-token</code>,
as <code>Authorization: Bearer &lt;token&gt;</code> or <code>?token=&lt;token&gt;</code>.</p>
<ul>
<li><a href="/api/snapshot">GET /api/snapshot</a>: the cockpit state as JSON</li>
<li>GET /api/events: WebSocket, the snapshot on connect and on every change</li>
<li>POST /api/cmd <code>{"argv": [...]}</code>: run an allow-listed cmdr command</li>
<li><a href="/app/">/app/</a>: the Commander app as a plain page (open it from <code>cmdr app --web</code>, which adds the token)</li>
<li><a href="/api/chat">GET /api/chat</a>: native chat sessions; GET /api/chat/&lt;role&gt;/events?since=N (WebSocket); POST /api/chat/&lt;role&gt;/start|stop|send|interrupt ({"by":"&lt;agent role&gt;"}: an agent's mail, followed by a labelled note)|approve|answer|handover|popout|runtime ({"runtime":"claude|codex","continue":bool})|account ({"account":"&lt;name&gt;","continue":bool}); POST /api/chat/accounts ({"name":..}: create, open its login in Terminal); POST /api/chat/accounts/&lt;name&gt;/login; POST|GET|DELETE /api/chat/accounts/&lt;name&gt;/signin (sign in from the app: start, state, cancel); POST /api/chat/accounts/&lt;name&gt;/signin/code ({"code":..}); POST /api/chat/accounts/&lt;name&gt;/signout</li>
</ul>
</body></html>
`

func servePlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(placeholderPage))
}

// serveLiveData returns the full loaded Data (the cockpit's own sidebar, title
// bar and inspector read this instead of each querying br and state.db).
func (s *Server) serveLiveData(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveData == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "livedata not available"})
		return
	}
	s.current(r.Context()) // make sure a first load happened
	d, ok := s.opts.LiveData()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "no data loaded yet"})
		return
	}
	if d.Postgres && d.LoadErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "current database state unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, livedata.ToWire(d))
}
