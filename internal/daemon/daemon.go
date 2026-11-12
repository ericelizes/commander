// Package daemon implements U20: the commander always-on daemon.
//
// cmdrd is a separate Go binary that hosts the watchdog, scheduler, and inbox
// poller as long-lived background goroutines — independent of whether the TUI
// is running. When the TUI launches, it can attach over a Unix socket
// (~/.commander/cmdrd.sock) to read live state instead of polling SQLite.
//
// Lifecycle:
//
//	d, _ := daemon.New()
//	d.Run(ctx) // blocks until ctx cancellation, then graceful shutdown
//
// Run wires the dependency graph and blocks. Shutdown order is the reverse of
// startup so consumers stop before producers: inbox poller → scheduler →
// watchdog → socket → store.
//
// The daemon is intentionally read-only over the wire for U20. Spawn and other
// state mutations stay TUI-side via the Spawner; the daemon only exposes a
// minimal ping/state JSON-line protocol so the TUI (or `cmdrd status`) can
// observe live aggregates without re-opening SQLite. Write surface lands in a
// later iteration once the TUI's RPC needs are well-shaped.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/binstage"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/api"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/ceilings"
	"github.com/example-user/commander/internal/chat"
	"github.com/example-user/commander/internal/config"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/holding"
	"github.com/example-user/commander/internal/inbox"
	"github.com/example-user/commander/internal/layout"
	"github.com/example-user/commander/internal/linearsync"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/notify"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/rolemail"
	"github.com/example-user/commander/internal/scheduler"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/statusclassify"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/tmuxctl"
	"github.com/example-user/commander/internal/tui"
	"github.com/example-user/commander/internal/wake"
	"github.com/example-user/commander/internal/workflowtemplate"
	"github.com/example-user/commander/internal/wsmgr"
)

// wakeInterval is how often manager/operator mailboxes are checked so a new
// message nudges the agent's chat quickly.
const wakeInterval = 5 * time.Second

// scanInterval is how often the scheduler scans every activated project for
// ready Tasks. 60s matches scheduler.New's documented default; we set it
// explicitly so the daemon's behavior is obvious from this file alone.
const scanInterval = 60 * time.Second

// inboxInterval is how often the daemon's inbox poller hits am for fresh mail.
// Matches the TUI's BuildInboxPoller cadence so behavior is consistent across
// daemon and TUI.
const inboxInterval = 60 * time.Second

// commanderProject + commanderAgent are the am identity the daemon polls under.
// Same pair the TUI uses (BuildInboxPoller); the daemon takes it over so mail
// keeps flowing while the TUI is closed.
const (
	commanderProject = "commander"
	commanderAgent   = "commander"
)

// configSource adapts *config.Config to scheduler.ConfigSource. The scheduler
// reads it on every scan, so this is a thin field projection. A nil cfg (or no
// runs/beads to count with) means no auto-spawn and no Operator cap.
type configSource struct {
	cfg   *config.Config
	runs  spawn.RunLister
	beads beads.Client
}

func (c *configSource) AutoSpawnEnabled(projectName string) bool {
	if c.cfg == nil {
		return false
	}
	p, ok := c.cfg.Projects[projectName]
	if !ok {
		return false
	}
	return p.AutoSpawn
}

func (c *configSource) CheckCapacity(ctx context.Context, p store.Project, taskBeadID string) error {
	if c.cfg == nil || c.runs == nil || c.beads == nil {
		return nil
	}
	return spawn.CheckWorkstreamCap(ctx, c.runs, c.beads, p.ID, p.Path, taskBeadID, c.cfg.Concurrency.MaxOperatorsPerWorkstream)
}

// serverInbox adapts the am server to the one agentmail.Client method the
// inbox poller calls. agent is a ROLE ("commander"); it is resolved to the
// server-assigned am name via state.db. A role with no mapping yet (nobody has
// registered it) yields no mail rather than an error.
type serverInbox struct {
	agentmail.Client // unused methods
	server           *agentmail.ServerClient
	roles            interface {
		GetAmAgent(ctx context.Context, amProjectKey, role string) (string, error)
	}
	key string
}

func (s *serverInbox) Inbox(ctx context.Context, _, role string, since time.Time, opts agentmail.InboxOptions) ([]agentmail.Mail, error) {
	name, err := s.roles.GetAmAgent(ctx, s.key, role)
	if err != nil || name == "" {
		return nil, err
	}
	return s.server.FetchInbox(ctx, s.key, name, since, opts)
}

// spawnerMail is the am client cmdrd's spawner registers with: the server
// registrar keyed by the repo path when there is a server, else the CLI.
func spawnerMail(cli agentmail.Client, server agentmail.AgentRegistrar, names agentmail.RoleNames, key string) agentmail.Client {
	if server == nil {
		return cli
	}
	return &agentmail.ServerRegistrar{Client: cli, Server: server, Store: names, Key: key}
}

// newDaemonSpawner is cmdrd's spawner, registering through spawnerMail.
func newDaemonSpawner(st *store.Store, oc opencode.Client, bd beads.Client, cli agentmail.Client, server agentmail.AgentRegistrar, dk docker.Client, tx tmuxctl.Client, repo string) *spawn.Spawner {
	return spawn.New(st, oc, bd, spawnerMail(cli, server, st, repo), dk, tx, repo)
}

// State is the read-only snapshot returned over the Unix socket. Kept narrow
// on purpose: callers that want richer state should re-open SQLite directly.
type State struct {
	Operators    int     `json:"operators"`     // count of running agent_runs
	USDDay       float64 `json:"usd_day"`       // sum(cost_events.usd) over last 24h
	AgentsActive int     `json:"agents_active"` // alias for operators today; reserved for future split
}

// Daemon is the long-lived runtime. Construct via New (production) or via
// the test seam in daemon_test.go (mocked clients + skipSocket=true).
type Daemon struct {
	executionWG       sync.WaitGroup
	orchestrationMode string // read from canonical database before starting workers
	store             *store.Store
	opencode          opencode.Client
	beads             beads.Client
	reads             beads.Client // cached, export-backed reader for periodic sweeps
	// invariantReads is the invariant watcher's own export reader (nil without one).
	invariantReads     beads.Client
	am                 agentmail.Client
	docker             docker.Client
	tmux               tmuxctl.Client
	spawner            *spawn.Spawner
	watchdog           *ceilings.Watchdog
	scheduler          *scheduler.Scheduler
	inbox              *inbox.Poller
	waker              *wake.Waker    // nil when the am server client can't be built
	chat               *chat.Engine   // native chat engine (headless Claude sessions for the app)
	ensured            *wsmgr.Manager // starts every open workstream's Manager natively; nil without mail
	settle             runSettler     // moves an Operator's bead on when its process ends
	watch              opWatch        // looks at running Operators every minute; wakes their Manager when needed
	live               *livedata.Source
	liveOps            *liveOperators
	health             *healthBoard
	supervisionDeliver func(context.Context, string, string) error
	invariant          *invariantWatcher // mails a Manager about open beads with no agent, wait or the user; nil without mail
	pitch              *pitchWatcher     // mails a Manager whose ready ask has no pitch; nil without mail
	gates              *gateWatcher      // re-checks condition gates; nil without mail
	waits              *waitWatcher      // wakes a Manager when the CI/review it waits on is done; nil without mail
	notifier           notify.Notifier
	cfg                *config.Config
	socket             *Socket

	// notifyEnabled gates the OS-notification fan-out. Default true; the
	// cmdrd `--notifications=false` flag flips it off for headless runs
	// where popping a Notification Center alert would be noise (CI, smoke
	// tests, ssh-only servers).
	notifyEnabled bool

	// home is the resolved ~/.commander directory. Used to derive socket and
	// log paths and (in tests) to point at a temp directory.
	home string

	// skipSocket disables the Unix socket listener and the HTTP API.
	// Production keeps it false; the daemon test sets it true so we don't bind
	// real listeners inside the test sandbox.
	skipSocket bool

	// alertEvery is how often the health alerts are checked (0: healthEvery).
	alertEvery time.Duration
}

// routedRoleMail is how a watcher mails. Every watcher goes through RoutedMail:
// what they name a Manager from (labels, parents, board rows) can outlive its
// workstream.
func routedRoleMail(managers *wsmgr.Manager, st rolemail.Store, server rolemail.Mailer, repo, from, importance string) wsmgr.SendFunc {
	return managers.RoutedMail(func(ctx context.Context, role, subject, body string) error {
		return rolemail.Send(ctx, st, server, repo, from, []string{role}, subject, body, agentmail.SendOptions{Importance: importance})
	})
}

// New wires a production daemon from ~/.commander/. Order matters: store first
// (so we can fail fast on a corrupt DB), then config (also fail-fast on a
// malformed file), then clients, then the runtime objects that depend on them.
//
// The returned Daemon is inert until Run is called — no goroutines are
// running, no socket is bound. This matches the TUI's services pattern.
func New() (*Daemon, error) {
	home, err := commanderHome()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		return nil, fmt.Errorf("daemon: create logs dir: %w", err)
	}

	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, fmt.Errorf("daemon: open store: %w", err)
	}

	chatEngine := newChatEngine(home, st)
	// Cost of chats that ran before cost tracking: read the logs once, off the
	// start path (idempotent; the logs are only read).
	go func() {
		if n, err := chatEngine.BackfillCosts(context.Background()); err != nil {
			log.Printf("chat: backfill costs: %v", err)
		} else if n > 0 {
			log.Printf("chat: backfill costs: %d turns read", n)
		}
	}()

	cfg, err := config.Load(filepath.Join(home, "config.toml"))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("daemon: load config: %w", err)
	}

	if loc, lerr := cfg.User.Location(); lerr != nil {
		log.Printf("board: %v; using the process zone", lerr)
	} else {
		livedata.SetBoardZone(loc)
	}
	livedata.LoadTicketPrefix(home)

	oc := opencode.New()
	bd := beads.New()
	am := agentmail.New()
	dk := docker.New()
	tx := tmuxctl.New()

	// commanderRepo is the absolute path to the commander checkout. The
	// spawner uses it to mount .opencode/agents/ read-only into operator
	// containers. We resolve it from the binary's own location (../..) so the
	// daemon doesn't depend on a specific install layout — works whether
	// cmdrd is run from /usr/local/bin via launchd or from ./bin/cmdrd in
	// dev. If we can't determine it, the spawn pipeline will fail loudly at
	// spawn time; that's a better signal than silently mounting nothing.
	commanderRepo := resolveCommanderRepo()

	// The spawner registers Operators (fresh, crash restarts, scheduler,
	// handoff) through the am server under the repo path, reusing a role's
	// stored mapping; the am CLI's bare project name is no am project.
	var spawnServer agentmail.AgentRegistrar
	if srv, err := agentmail.NewServerClientFromEnv(); err == nil {
		spawnServer = srv
	}
	sp := newDaemonSpawner(st, oc, bd, am, spawnServer, dk, tx, commanderRepo).WithDockerSandbox(cfg.OperatorSandbox == "docker").WithNativeExecution(chatEngine)
	wd := ceilings.NewWatchdog(st, dk, oc, bd, am, ceilingsLimitsFrom(cfg))
	// The periodic readers share one cache, so a project nobody wrote to costs
	// no br call however often they look.
	cached := beads.NewCached(bd)
	// The board reads each project's beads export instead of br: milliseconds,
	// no lock, so the board follows writes within one refresh.
	reads := beads.ExportReads(cached)
	sc := scheduler.New(cached, st, sp, &configSource{cfg: cfg, runs: st, beads: cached}, scanInterval)
	pl := inbox.NewPoller(am, commanderProject, commanderAgent, inboxInterval)

	// Wake agents on new mail. Needs the am server token; without it the
	// daemon still runs, just without wake-ups.
	var waker *wake.Waker
	var ensured *wsmgr.Manager
	var waits *waitWatcher
	var invariant *invariantWatcher
	var invariantReads beads.Client
	var pitch *pitchWatcher
	var liveGates *gateWatcher
	reported := newReportedRuns(time.Now)
	settle := runSettler{projects: st, events: st, beads: bd, spawner: sp, running: st.ListRunningRuns, model: cfg.Models.DefaultOperator, restarts: &restartBudget{}, reported: reported}
	liveRuns := spawn.NewLiveRuns(st)
	liveOps := &liveOperators{list: liveRuns.ListRunningRuns, now: time.Now, finished: liveRuns.Reaper.FinishedRunningRuns, report: settle.readReport, running: st.ListRunningRuns, reported: reported}
	// One snapshot source for the API and the watch (which judges workstreams by it).
	health := newHealthBoard(time.Now)
	live := &livedata.Source{
		Health: health.list,
		ManagerCapacity: func() map[string]string {
			if chatEngine == nil {
				return nil
			}
			return chatEngine.CapacityFailures()
		},
		OnSnapshot:  func(snap *livedata.Snapshot) { checkPlans(health, snap); checkStuck(health, snap, time.Now()) },
		Deps:        livedata.Deps{Store: st, Beads: reads, Scrape: livedata.ScrapeAgentStates, UsageCapacity: func() string { return chat.UsageCapacityCause(home, time.Now()) }, PlanningUsage: func(role string) string { return chat.PlanningUsageCause(home, role, time.Now()) }},
		UnreadEvery: 5 * time.Second, // Unread is set below from the waker's inbox reads
		WaitsChecked: func() time.Time {
			if waits == nil {
				return time.Time{}
			}
			return waits.CheckedAt()
		},
		PRHeads: livedata.NewPRHeadCache(workflowtemplate.RunGH, livedata.PRHeadTTL).Lookup,
		PRStates: func() map[string]string {
			if waits == nil {
				return nil
			}
			return waits.PRStates()
		},
		ReviewFacts: func() map[string]livedata.ReviewFact {
			if waits == nil {
				return nil
			}
			return waits.ReviewFacts()
		},
		PitchAsked: func() map[string]int64 { return pitch.Asked() },
		Live:       liveOps.get,
		Reported:   reported.list,
		Chats: func() map[string]livedata.ChatState {
			if chatEngine == nil {
				return nil
			}
			out := map[string]livedata.ChatState{}
			for role, c := range chatEngine.Live() {
				out[role] = livedata.ChatState{State: c.State, Detail: c.Detail}
			}
			return out
		},
	}
	if st.IsPostgres() {
		live.Deps.Scrape = nil
		liveOps.every = time.Second
	}

	watch := opWatch{store: st, issues: st, activeAt: transcriptActiveAt, now: time.Now, latest: func() (livedata.Snapshot, bool) {
		if st.IsPostgres() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return live.WatchSnapshot(ctx), true
		}
		data, ok := live.Last()
		if !ok {
			if err := live.LastLoadErr(); err != nil {
				return livedata.Snapshot{Degraded: &livedata.SnapDegraded{Reason: err.Error()}}, true
			}
			return livedata.Snapshot{}, false
		}
		snap := livedata.BuildSnapshot(data, nil)
		// Last keeps the previous good data across a failed read: say so.
		if err := live.LastLoadErr(); err != nil {
			snap.Degraded = &livedata.SnapDegraded{Reason: err.Error()}
		}
		return snap, true
	}}
	watch.manager = func(ctx context.Context, run store.AgentRun) string { return runManager(ctx, st, cached, run) }
	watch.degraded = &degradedAlert{home: home, names: st, key: commanderRepo, now: time.Now}
	if amServer, err := agentmail.NewServerClientFromEnv(); err != nil {
		log.Printf("daemon: mail wake disabled: %v", err)
	} else {
		dir := layout.NewDirector()
		managers := &wsmgr.Manager{Store: st, Beads: cached, Reg: amServer, AmKey: commanderRepo, Dir: dir, Numbered: liveWorkstreams(live)}
		deliverer := chatDeliverer{chat: chatEngine, tmux: &wake.TmuxDeliverer{Projects: st, Director: dir, Managers: managers}}
		if chatEngine != nil {
			chatEngine.SetUsageMoveHook(func(role, text string) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if err := managers.NoteOnWorkstream(ctx, role, text); err != nil {
					log.Printf("usage move note for %s: %v", role, err)
				}
			})
			managers.Native = nativeChats{e: chatEngine}
			deliverer.managers = managers
			ensured = managers
		}
		waker = wake.New(amServer, st, deliverer, commanderRepo, wakeInterval)
		live.Unread = waker.Unread
		watch.deliver = deliverer.Deliver
		waker.OnDeliver = func(role string, err error) {
			if err == nil {
				health.ok("mail:" + role)
				return
			}
			health.fail("mail:"+role, fmt.Sprintf("Mail can't reach %s: %v", role, err))
		}
		waker.OnHistory = func(held map[string]int, answers map[string][]string) {
			reportMailHistory(health, held, answers)
		}
		waker.OnStuck = func(role string, m agentmail.Mail, first bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// The sidebar row shows "✉ stuck" until the agent takes a turn again.
			_ = st.SetAgentState(ctx, role, store.AgentStuck, "mail waiting, input box has text")
			if first {
				notifyStuck(role, m)
			}
		}
		// The notification poller must use the same server + fleet am project
		// key + role->name mapping as everything else; the `am` CLI is refused
		// while the server runs and "commander" is not an am project slug.
		pl = inbox.NewPoller(&serverInbox{server: amServer, roles: st, key: commanderRepo}, commanderRepo, commanderAgent, inboxInterval)
		settle.mail = routedRoleMail(managers, st, amServer, commanderRepo, "cmdrd", "high")
		watch.mail = settle.mail
		health.alert = healthAlert(routedRoleMail(managers, st, amServer, commanderRepo, "cmdrd", "urgent"))
		watch.degraded.mail = amServer
		liveGates = &gateWatcher{
			projects: st, beads: bd, gh: runGH, run: gates.RunShell, repoOf: livedata.GitHubRepo, now: time.Now,
			last: map[string]gateSeen{}, failing: map[string]int{},
			degraded: func() bool { return live.LastLoadErr() != nil },
			mail:     routedRoleMail(managers, st, amServer, commanderRepo, "commander", "high"),
		}
		waits = &waitWatcher{
			projects: st, beads: reads, gh: runGH, repoOf: livedata.GitHubRepo, now: time.Now, woken: map[string]bool{},
			prodTagPrefix: os.Getenv("COMMANDER_PROD_TAG_PREFIX"),
			running: func(ctx context.Context) (map[string]bool, error) {
				runs, err := st.ListRunningRuns(ctx)
				if err != nil {
					return nil, err
				}
				out := make(map[string]bool, len(runs))
				for _, r := range runs {
					out[spawn.CanonicalRole(r.AgentName)] = true
				}
				return out, nil
			},
			mail: routedRoleMail(managers, st, amServer, commanderRepo, "commander", "high"),
		}
		invariant = newInvariantWatcher(watch.latest, waits.mail)
		pitch = newPitchWatcher(home, watch.latest, waits.mail)
		invariantReads = beads.ExportReads(beads.IsolatedIncremental(cached))
		invariant.fresh = func(ctx context.Context) (livedata.Snapshot, bool) {
			data, ok := loadInvariant(ctx, livedata.Deps{Store: st, Beads: invariantReads, Scrape: livedata.ScrapeAgentStates})
			data.Objectives = livedata.MarkReported(data.Objectives, reported.list(), nil)
			return livedata.BuildSnapshot(data, nil), ok
		}
	}

	// Canonical condition checks are database work. Optional Agent Mail must not
	// be able to disable them; durable supervision observes committed outcomes.
	if liveGates == nil && st.IsPostgres() {
		liveGates = &gateWatcher{projects: st, beads: bd, gh: runGH, run: gates.RunShell, repoOf: livedata.GitHubRepo, now: time.Now, last: map[string]gateSeen{}, failing: map[string]int{}}
	}

	return &Daemon{
		store:              st,
		opencode:           oc,
		beads:              bd,
		reads:              reads,
		invariantReads:     invariantReads,
		am:                 am,
		docker:             dk,
		tmux:               tx,
		spawner:            sp,
		watchdog:           wd,
		scheduler:          sc,
		inbox:              pl,
		waker:              waker,
		chat:               chatEngine,
		ensured:            ensured,
		settle:             settle,
		watch:              watch,
		supervisionDeliver: watch.deliver,
		live:               live,
		liveOps:            liveOps,
		health:             health,
		waits:              waits,
		invariant:          invariant,
		pitch:              pitch,
		gates:              liveGates,
		notifier:           notify.NewSystem(),
		cfg:                cfg,
		home:               home,
		notifyEnabled:      true,
	}, nil
}

// SetNotificationsEnabled toggles the OS-notification fan-out. Call before
// Run; flipping at runtime is unsupported (the drain goroutine reads the
// field once at start). Wired to cmdrd's `--notifications` flag.
func (d *Daemon) SetNotificationsEnabled(enabled bool) {
	d.notifyEnabled = enabled
}

// Run starts every background subsystem and blocks until ctx is cancelled.
// Shutdown is graceful: dependents stop before their dependencies. Returns
// nil on a clean ctx-cancellation; a non-nil error indicates a startup
// failure that prevented the daemon from coming up.
func (d *Daemon) Run(ctx context.Context) error {
	control, err := d.store.OrchestrationControl(ctx)
	if err != nil {
		return fmt.Errorf("daemon: read orchestration control: %w", err)
	}
	d.orchestrationMode = control.Mode
	if d.waker != nil && d.store.IsPostgres() && d.orchestrationMode == "active" {
		// Commander is the only planner and execution agents have no mail:
		// retired Managers' inboxes are not polled.
		d.waker.Only = chat.IsCommanderRole
	}
	started := time.Now()
	defer beads.StopBackground(d.reads)
	defer beads.StopBackground(d.invariantReads)
	beads.SetDoorServer(true) // this process's own br writes join the door's line, not call it
	// The app first: while the API is down it shows its start screen. Chats are
	// picked back up in the background; chat requests wait for that.
	if d.chat != nil && !d.skipSocket {
		d.chat.ResumeRunning(ctx)
		if d.ensured != nil {
			go d.ensureManagers(ctx)
		}
		go d.chat.Heartbeat(ctx, time.Minute)
		if d.store.IsPostgres() {
			go d.store.MaintainEventHistory(ctx)
			worker := &statusclassify.Worker{Store: d.store, Engine: d.chat, Home: d.home}
			go worker.Run(ctx)
			if d.live != nil && d.orchestrationMode == "active" {
				describer := &statusclassify.Describer{Worker: worker, UserName: config.UserName(d.cfg), CachePath: filepath.Join(d.home, "status-lines.json"), OnReady: func() { d.store.NotifyStateChanged(context.Background()) }}
				d.live.Describe = describer.Describe
				go describer.Run(ctx)
			}
		}
		go chat.RunCaptureSweep(ctx, d.home, time.Hour)
	}
	if !d.skipSocket {
		d.startAPI(ctx)
		log.Printf("daemon: api up %s after start", time.Since(started).Round(time.Millisecond))
	}
	if err := d.watchdog.Start(ctx); err != nil {
		return fmt.Errorf("daemon: start watchdog: %w", err)
	}
	if d.store.IsPostgres() && d.orchestrationMode == "active" {
		d.executionWG.Add(1)
		go func() { defer d.executionWG.Done(); d.runExecution(ctx) }()
		if d.chat != nil {
			go executionRedirector{store: d.store, send: d.chat.Send}.run(ctx)
		}
	} else {
		d.scheduler.Start(ctx)
	}
	d.inbox.Start(ctx)
	if d.store != nil && !d.skipSocket {
		go d.reapHostRuns(ctx)
		if d.store.IsPostgres() && d.live != nil && d.live.Live != nil {
			go d.reconcilePostgresPresence(ctx)
			go d.observeLegacyPostgresOperators(ctx)
			go d.runSupervision(ctx)
		}
		sweepReads := d.reads
		if sweepReads == nil {
			sweepReads = d.beads
		}
		go worktreeSweeper{runs: d.store, projects: d.store, beads: sweepReads}.Run(ctx)
		go (&diskWatcher{home: d.home, health: d.health, projects: d.store, runs: d.store}).Run(ctx)
		go d.runTokenLedger(ctx)
	}
	if d.waker != nil {
		go d.waker.Run(ctx)
		if d.chat != nil {
			go managerSleeper{chat: d.chat, states: d.store.ListAgentStates, unread: d.waker.Unread,
				idle: managerIdleBeforeSleep, every: 2 * time.Minute, now: time.Now}.Run(ctx)
		}
	}
	if d.waits != nil && !d.skipSocket {
		go d.waits.Run(ctx)
	}
	if d.invariant != nil && !d.skipSocket && !(d.store.IsPostgres() && d.orchestrationMode == "active") {
		go d.invariant.Run(ctx)
	}
	if d.pitch != nil && !d.skipSocket {
		go d.pitch.Run(ctx)
	}
	if d.gates != nil && !d.skipSocket {
		go d.gates.Run(ctx)
	}
	if d.live != nil && !d.skipSocket {
		// Every 10s, whether or not the app is open: build the board, which
		// checks every plan against the rules and lists what keeps failing
		// (health).
		go func() {
			t := time.NewTicker(healthEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					// The API rebuilds on every relevant change; build here only
					// when nothing has for a whole health interval.
					if _, fresh := d.live.Recent(healthEvery); !fresh {
						timedHealthLoad(ctx, d.live.Snapshot, livedata.SlowLoad)
					}
				}
			}
		}()
	}
	if d.health != nil {
		// Alerts on their own loop: a hung mail server stalls these, no snapshot.
		go func() {
			every := d.alertEvery
			if every == 0 {
				every = healthEvery
			}
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					d.health.alertDue()
				}
			}
		}()
	}

	// Drain the inbox poller's Out channel so its bounded buffer doesn't
	// back up while no consumer is attached. U20's daemon doesn't surface
	// notifications yet (U21); for now we just keep the pipe flowing so the
	// poller never blocks. When U21 lands, this drain becomes a real fan-out
	// to the socket / OS notifier.
	go d.drainInbox(ctx)

	// Drain the scheduler's proposal channel for the same reason: the channel
	// is buffered + drops oldest on overflow, so reads are correctness-neutral
	// — but draining keeps the buffer empty so AutoSpawn=false projects never
	// silently lose proposals to the drop policy. (When the TUI is attached
	// it'll relay them; for now they go to /dev/null.)
	go d.drainProposals(ctx)

	if !d.skipSocket {
		sock, err := NewSocket(filepath.Join(d.home, "cmdrd.sock"), d.snapshotState)
		if err != nil {
			d.shutdown()
			return fmt.Errorf("daemon: open socket: %w", err)
		}
		d.socket = sock
		go func() {
			if err := sock.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				// Socket errors are non-fatal: the daemon's primary job is
				// keeping the watchdog/scheduler alive, and a wedged socket
				// shouldn't take those down. Log to stderr so launchd's
				// StandardErrorPath captures it.
				fmt.Fprintln(os.Stderr, "daemon: socket serve:", err)
			}
		}()
	}

	<-ctx.Done()
	// A subsystem stuck in a slow call (the mail server, a busy database) must
	// not hold the restart: launchd would wait up to 20s to kill us, with the
	// app on its start screen. Chats are detached and resume on the next start.
	stopped := make(chan struct{})
	go func() {
		d.shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownLimit):
		log.Printf("daemon: shutdown still waiting after %s; exiting anyway", shutdownLimit)
	}
	return nil
}

// shutdownLimit is how long Run waits for its subsystems to stop.
const shutdownLimit = 5 * time.Second

// shutdown tears down every subsystem in reverse-startup order. Idempotent —
// calling multiple times is safe.
func (d *Daemon) shutdown() {
	d.executionWG.Wait() // finish bounded startup cleanup before closing its store
	if d.chat != nil {
		d.chat.Close() // processes finish their turn and exit; they resume on the next start
	}
	if d.socket != nil {
		_ = d.socket.Close()
	}
	if d.inbox != nil {
		d.inbox.Stop()
	}
	if d.scheduler != nil {
		d.scheduler.Stop()
	}
	if d.watchdog != nil {
		d.watchdog.Stop()
	}
	if d.store != nil {
		_ = d.store.Close()
	}
}

// snapshotState builds the read-only State payload returned over the socket.
// Errors are swallowed and surface as zero values: the socket is a "best
// effort" observability surface, not a system-of-record.
func (d *Daemon) snapshotState() State {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var s State
	if d.store != nil {
		if n, err := d.store.CountAllRuns(ctx, "running"); err == nil {
			s.Operators = n
			s.AgentsActive = n
		}
		if usd, err := d.store.SumUSDByWindow(ctx, time.Now().Unix(), 24); err == nil {
			s.USDDay = usd
		}
	}
	return s
}

// drainInbox is the inbox poller's terminal consumer. It both keeps the
// bounded channel from wedging AND, since U21, fans out OS notifications for
// the two alert classes the human cares about while the TUI is closed:
//
//  1. Ceiling-hit / operator-killed mail (subject begins "ceiling-hit" or
//     "operator <agent> killed:" — the latter is what ceilings.notifyLead
//     emits today). Title=From, body="<subject> · <body>".
//  2. Mail sent with am Importance="urgent". Title="commander · <project>",
//     body="<from>: <subject>".
//
// The two classes can overlap (a ceiling-hit IS sent with urgent importance);
// we de-dupe by checking ceiling-hit first and skipping the urgent branch on
// match. notifier errors are logged + swallowed: a stuck osascript shouldn't
// take down the daemon.
//
// drainProposals: same drain shape, no notification fan-out. Exits when ctx
// is cancelled OR when the channel closes (i.e. when the producer has been
// Stop()'d).
func (d *Daemon) drainInbox(ctx context.Context) {
	if d.inbox == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-d.inbox.Out:
			if !ok {
				return
			}
			if !d.notifyEnabled || d.notifier == nil {
				continue
			}
			d.dispatchNotification(ctx, n)
		}
	}
}

// dispatchNotification classifies an inbox Notification and emits at most
// one OS notification per call. Extracted from drainInbox for testability —
// daemon tests inject a MockNotifier and call this directly with hand-built
// inbox.Notification values, sidestepping the am poller entirely.
func (d *Daemon) dispatchNotification(ctx context.Context, n inbox.Notification) {
	subject := n.Subject

	// Class 1: ceiling-hit. The watchdog's notifyLead emits subject lines
	// shaped "operator <agent> killed: <reason>". We also accept a bare
	// "ceiling-hit" prefix in case a future emitter uses the canonical
	// label directly. Match is case-insensitive on the prefix only.
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "ceiling-hit") || strings.HasPrefix(lower, "operator killed:") || isOperatorKilled(lower) {
		title := n.From
		body := subject
		if n.Body != "" {
			body = subject + " · " + n.Body
		}
		if err := d.notifier.Notify(ctx, title, body); err != nil {
			log.Printf("daemon: notify ceiling-hit: %v", err)
		}
		return
	}

	// Class 2: urgent importance, regardless of subject shape. Catches the
	// am-mail urgency channel that U14 surfaces.
	if strings.EqualFold(n.Importance, "urgent") {
		title := "commander"
		if n.Project != "" {
			title = "commander · " + n.Project
		}
		body := subject
		if n.From != "" {
			body = n.From + ": " + subject
		}
		if err := d.notifier.Notify(ctx, title, body); err != nil {
			log.Printf("daemon: notify urgent-mail: %v", err)
		}
	}
}

// isOperatorKilled matches the shape "operator <name> killed:" produced by
// ceilings.notifyLead. Pulled out so dispatchNotification reads as a clean
// chain of HasPrefix calls.
func isOperatorKilled(lowerSubject string) bool {
	return strings.HasPrefix(lowerSubject, "operator ") && strings.Contains(lowerSubject, " killed:")
}

func (d *Daemon) drainProposals(ctx context.Context) {
	if d.scheduler == nil {
		return
	}
	out := d.scheduler.Out()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-out:
			if !ok {
				return
			}
		}
	}
}

// commanderHome returns the resolved ~/.commander path. COMMANDER_HOME
// override matches install.sh so dev can point at a sandbox without touching
// the real layout.
func commanderHome() (string, error) {
	if v := os.Getenv("COMMANDER_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("daemon: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".commander"), nil
}

// resolveCommanderRepo best-effort resolves the commander checkout path from
// the binary location. cmdrd lives at `<repo>/bin/cmdrd` in dev or at
// `/usr/local/bin/cmdrd` (a symlink to the repo binary) in prod. We walk up
// from the executable's path until we find a directory containing go.mod with
// our module name. Returns "" if we can't find one — the spawner will then
// fail loudly when it tries to mount agent prompts.
func resolveCommanderRepo() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if repo := binstage.RepoHint(exe); repo != "" {
		return repo
	}
	// Resolve symlinks so /usr/local/bin/cmdrd → /tmp/example/commander/bin/cmdrd.
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	dir := filepath.Dir(resolved)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// ceilingsLimitsFrom maps the user's config.Ceilings into a ceilings.Limits. The single-call estimates aren't user-facing yet
// (they're a watchdog implementation detail), so we keep the production
// defaults from ceilings.DefaultLimits().
func ceilingsLimitsFrom(cfg *config.Config) ceilings.Limits {
	def := ceilings.DefaultLimits()
	if cfg == nil {
		return def
	}
	return ceilings.Limits{
		MaxUSDPerOperator:        cfg.Ceilings.MaxUSDPerOperator,
		MaxTokensPerOperator:     int64(cfg.Ceilings.MaxTokensPerOperator),
		MaxWallclockMinutes:      cfg.Ceilings.MaxWallclockMinutes,
		MaxRetries:               cfg.Ceilings.MaxRetries,
		SingleCallUSDEstimate:    def.SingleCallUSDEstimate,
		SingleCallTokensEstimate: def.SingleCallTokensEstimate,
	}
}

// notifyStuck mails Commander once: an agent has unread mail it was never woken
// for, because its input box holds text that is not a stray keystroke.
func notifyStuck(role string, m agentmail.Mail) {
	bin, err := exec.LookPath("cmdr")
	if err != nil {
		return
	}
	body := fmt.Sprintf("[system] %s has had unread mail for over %s (%q from %s) but its chat input box holds text, so it was not woken. Clear the input box (Ctrl-U) in that window, or the mail waits. The sidebar row shows \"✉ stuck\".",
		role, wake.StuckAfter, m.Subject, m.From)
	_ = exec.Command(bin, "internal-mail", "--from", role, "--to", "commander",
		"--subject", "✉ stuck: "+role+" has unread mail but a non-empty input box", "--body", body, "--importance", "high").Run()
}

// startAPI serves the cockpit state over HTTP on loopback (internal/api) for
// the app and the localhost page. It is non-fatal: without it the daemon's
// watchdog and scheduler still run.
func (d *Daemon) startAPI(ctx context.Context) {
	token, err := api.EnsureToken(d.home)
	if err != nil {
		log.Printf("daemon: api disabled: token: %v", err)
		return
	}
	opts := d.apiOptions(token)
	if d.store.IsPostgres() {
		opts.StateStore = d.store
		opts.Changes = d.store.WatchState(ctx)
		opts.Interval = 30 * time.Second
	}
	srv := api.New(opts)
	port := 0
	if d.cfg != nil {
		port = d.cfg.API.Port
	}
	url, err := api.Start(ctx, d.home, port, srv)
	if err != nil {
		log.Printf("daemon: api disabled: %v", err)
		return
	}
	log.Printf("daemon: api on %s (token in %s)", url, filepath.Join(d.home, "api-token"))
	if opts.Update != nil {
		opts.Update.Boot() // serving now: settle the install job a previous daemon left
	}
}

// apiOptions is what the API serves; Update is wired here (nil without a known checkout, then /api/update is 503).
func (d *Daemon) apiOptions(token string) api.Options {
	src := d.live
	lin := linearsync.New(linearsync.Options{Home: d.home})
	return api.Options{Token: token, Source: withBoards(d.home, src.Snapshot, &sprintFeed{lin: lin}), Linear: lin, Run: api.ExecRunner(cmdrPath()), AppDir: appDir(), AppDevURL: appDevURL(), Tabs: &api.Tabs{Store: d.store}, Pitches: d.store, LiveData: src.Last, FinishBoard: src.FinishBoard, Interval: time.Second, Chat: d.chat, ShotsDir: filepath.Join(d.home, "shots"), ConfigPath: filepath.Join(d.home, "config.toml"), MailToRole: mailUserToRole, WaitingMail: livedata.NewUnreadListFunc(d.store), UsageByAgent: d.store.UsageByAgent, Update: d.newUpdater(), LiveOwner: d.liveOwner}
}

// appDir is the built app renderer the API serves at /app/:
// $COMMANDER_APP_DIR, else <repo>/app/dist ("" when the repo is unknown).
func appDir() string {
	if dir := os.Getenv("COMMANDER_APP_DIR"); dir != "" {
		return dir
	}
	if repo := resolveCommanderRepo(); repo != "" {
		return filepath.Join(repo, "app", "dist")
	}
	return ""
}

// appDevURL is the live renderer dev server /app/ prefers while it answers:
// $COMMANDER_APP_DEV_URL ("off" = never), else 127.0.0.1:5173.
func appDevURL() string {
	switch v := os.Getenv("COMMANDER_APP_DEV_URL"); v {
	case "":
		return api.DefaultAppDevURL
	case "off":
		return ""
	default:
		return v
	}
}

// cmdrPath is the cmdr binary the API runs: the one next to cmdrd, else cmdr
// on PATH.
func cmdrPath() string {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if p := filepath.Join(filepath.Dir(exe), "cmdr"); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("cmdr"); err == nil {
		return p
	}
	return "cmdr"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// mailUserToRole sends a message the user typed in the app to a role that is not
// running natively yet: mail from "user", which the wake loop and the mail hook
// deliver to its terminal session.
func mailUserToRole(ctx context.Context, role, text string) error {
	bin, err := exec.LookPath("cmdr")
	if err != nil {
		return err
	}
	subject := "[from the user, via the app] " + firstLine(text, 80)
	out, err := exec.CommandContext(ctx, bin, "internal-mail", "--from", "user", "--to", role,
		"--subject", subject, "--body", text, "--importance", "high").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// reapHostRuns closes agent_runs rows of Operators whose process ended, at
// startup and every minute, so they stop counting against their workstream's
// Operator cap, and moves each one's bead on (review, or open again). It also
// kills pipe-pane writers no running Operator owns (spawn.ReapPipeWriters).
func (d *Daemon) reapHostRuns(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	// Runs found dead in the first sweep were killed while cmdrd was down
	// (restart, reboot); later ones died while it watched.
	first := true
	sleep := sleepWatch{now: time.Now}
	var retries settleRetries
	battery := newBatteryWatcher(d.settle.mail)
	for {
		gap, woke := sleep.tick()
		retries.run(ctx)
		battery.check(ctx)
		d.settle.reported.beginSweep()
		// Before the sweep, so a run past its final report is closed and settled this minute.
		d.settle.endFinishedRuns(ctx, nil)
		res, err := spawn.ReapHostRuns(ctx, d.store)
		if len(res.ClosedRuns) > 0 || err != nil {
			log.Printf("daemon: closed operator runs %v: %d exited, %d died (err=%v)", res.ClosedRuns, len(res.Exited), len(res.Died), err)
		}
		if woke && len(res.ClosedRuns) > 0 {
			log.Printf("daemon: woke after a %s gap: settling %d closed runs", gap.Round(time.Second), len(res.ClosedRuns))
			d.settle.afterWake(ctx, res, gap, retries.add)
		} else {
			d.settle.settleReaped(ctx, res, first, retries.add)
		}
		d.settle.reported.endSweep()
		if first {
			// After the first settle, so it only finds claims nothing settled.
			d.settle.sweepOrphans(ctx, d.store)
		}
		// After the runs above are closed, so their writers go this sweep.
		if pids, err := spawn.ReapPipeWriters(ctx, d.store); len(pids) > 0 || err != nil {
			log.Printf("daemon: killed %d orphaned pipe-pane writers %v (err=%v)", len(pids), pids, err)
		}
		first = false
		if d.store.IsPostgres() && d.orchestrationMode == "active" {
			watch := d.watch
			watch.deliver = nil
			watch.mail = nil
			watch.pass(ctx)
		} else {
			d.watch.pass(ctx)
		}
		sleep.done()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// liveWorkstreams numbers the workstreams from the snapshot cmdrd already
// keeps. The default (tui.ListNumberedWorkstreams) reads cmdrd's API only
// inside tmux, so from cmdrd it opened a second state.db pool and re-read every
// project from br, uncached, every ensureInterval: under load that read alone
// outlasted the minute it was given.
func liveWorkstreams(live *livedata.Source) func(context.Context) ([]tui.NumberedWorkstream, error) {
	return func(context.Context) ([]tui.NumberedWorkstream, error) {
		data, ok := live.Last()
		if !ok {
			return nil, errors.New("no snapshot loaded yet")
		}
		return tui.NumberWorkstreams(data), nil
	}
}

// ensureInterval is how often cmdrd checks that every open workstream has a
// Manager running natively (a workstream created while it runs gets one).
const ensureInterval = 30 * time.Second

// ensureManagers starts the native chat of every open workstream's Manager,
// once the chats that were running before a restart are back (so a surviving
// host is adopted, not duplicated), then again every ensureInterval.
func (d *Daemon) ensureManagers(ctx context.Context) {
	if d.store.IsPostgres() && d.orchestrationMode == "active" {
		return
	}
	if err := d.chat.WaitResumed(ctx); err != nil {
		return
	}
	t := time.NewTicker(ensureInterval)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if err := d.ensured.EnsureNative(c); err != nil {
			log.Printf("daemon: ensure managers: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// liveOwner answers the write door's holder check (an intent): an Operator holds only while its
// process is alive (the snapshot's own liveness look); a Manager's or Commander's name holds
// nothing by itself. known is false when liveness could not be looked up.
func (d *Daemon) liveOwner(owner string) (string, bool) {
	if holding.IsManager(owner) {
		return "idle", true
	}
	if d.liveOps == nil {
		return "", false
	}
	names, _, ok := d.liveOps.get()
	if !ok {
		return "", false
	}
	for _, n := range names {
		if spawn.CanonicalRole(n) == spawn.CanonicalRole(owner) {
			return "working", true
		}
	}
	return "", true
}
