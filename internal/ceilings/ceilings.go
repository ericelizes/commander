// Package ceilings is the cost-ceilings watchdog (U10).
//
// It enforces five independent bounds on every operator commander spawns:
//
//   - per-operator USD spent (Ceilings.MaxUSDPerOperator)
//   - per-operator tokens consumed (Ceilings.MaxTokensPerOperator)
//   - per-operator wallclock minutes (Ceilings.MaxWallclockMinutes)
//   - consecutive operator-side failures (Ceilings.MaxRetries)
//   - capacity caps per project + globally (concurrent running operators)
//
// Cost + tokens are tracked per-session by consuming opencode's nd-JSON event
// stream (`step_finish.part.cost` direct USD; `.part.tokens.{input,output}`
// per-step tokens). Polling `opencode stats` cannot isolate per-operator cost
// when two operators share a project — see share/contracts.md "Decision 2 —
// U10". Wallclock + retries tick on a separate 10s timer (non-cost dimensions
// have no event-stream signal).
//
// Lifecycle:
//
//	w := ceilings.NewWatchdog(...)
//	w.Start(ctx)              // background goroutines come up
//	w.Register(run)           // attach a per-operator consumer
//	...                        // events accumulate; ceilings enforce
//	w.Unregister(runID)       // detach when the operator exits
//	w.Stop()                  // join all goroutines
//
// Operator caps are per workstream, checked at spawn time
// (spawn.CheckWorkstreamCap), not here.
package ceilings

import (
	"context"
	"sync"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/docker"
	"github.com/example-user/commander/internal/opencode"
	"github.com/example-user/commander/internal/store"
)

// Limits names every ceiling the watchdog enforces, plus the per-call
// estimates used for pre-emptive halt math. Zero values are NOT magic —
// callers must populate via DefaultLimits() or by reading config.
//
// SingleCallUSDEstimate / SingleCallTokensEstimate are the "headroom" the
// watchdog reserves: when remaining budget drops below this, the operator is
// killed BEFORE the next call lands. This is the $47K LangChain incident
// fix — alerts after a 30s tick let single calls slip through.
type Limits struct {
	MaxUSDPerOperator        float64
	MaxTokensPerOperator     int64
	MaxWallclockMinutes      int
	MaxRetries               int
	SingleCallUSDEstimate    float64
	SingleCallTokensEstimate int64
}

// DefaultLimits returns sensible production defaults. Override individual
// fields after construction; do NOT mutate after Start.
func DefaultLimits() Limits {
	return Limits{
		MaxUSDPerOperator:        5.00,
		MaxTokensPerOperator:     200_000,
		MaxWallclockMinutes:      60,
		MaxRetries:               3,
		SingleCallUSDEstimate:    0.50,
		SingleCallTokensEstimate: 8_000,
	}
}

// WatchdogStore is the narrow projection of *store.Store the watchdog needs.
// Defined as an interface so unit tests substitute an in-memory fake.
type WatchdogStore interface {
	UpdateUsage(ctx context.Context, runID int64, tokens int64, usd float64) error
	MarkClosed(ctx context.Context, runID int64, status string) error
	IncrementFailures(ctx context.Context, runID int64) (int64, error)
	InsertCostEvent(ctx context.Context, e store.CostEvent) (int64, error)
	ListRunningRuns(ctx context.Context) ([]store.AgentRun, error)
}

// EventSource opens a per-operator nd-JSON stream. The default production
// implementation uses `docker logs -f <containerID>` for code-type runs and
// `tail -F <fifo>` for knowledge-type runs (the fifo is provisioned at U15
// spawn time via `tmux pipe-pane`). Tests inject a canned scanner.
//
// The returned io.ReadCloser is line-oriented nd-JSON: each line is one
// opencode.Event candidate. Close() must terminate the underlying process.
type EventSource interface {
	Open(ctx context.Context, st *runState) (lineReader, error)
}

// Watchdog is the running cost-ceiling enforcer. Construct via NewWatchdog
// then call Start to launch goroutines; Register/Unregister attach + detach
// per-operator consumers; Stop blocks until every goroutine joins.
type Watchdog struct {
	store    WatchdogStore
	docker   docker.Client
	opencode opencode.Client
	beads    beads.Client
	am       agentmail.Client
	limits   Limits
	source   EventSource

	// killPane kills a host operator's tmux pane. Overridable in tests.
	killPane func(ctx context.Context, target string) error

	// runs map[runID]*runState. sync.Map gives us race-free Range without
	// having to lock around iteration during the wallclock tick.
	runs sync.Map

	// wg waits for the tick loop + every per-operator consumer goroutine.
	wg sync.WaitGroup

	// rootCtx is set by Start; cancelled by Stop. Per-run consumers derive
	// their own ctx from this so Stop tears them all down at once.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// runCancels holds per-run cancel funcs so Unregister can stop one
	// consumer without affecting the rest.
	runCancelsMu sync.Mutex
	runCancels   map[int64]context.CancelFunc

	started bool
	startMu sync.Mutex
}

// NewWatchdog returns a stopped watchdog. None of the dependencies may be
// nil — callers wire fakes in tests, real clients in production.
func NewWatchdog(
	s WatchdogStore,
	d docker.Client,
	oc opencode.Client,
	b beads.Client,
	am agentmail.Client,
	limits Limits,
) *Watchdog {
	return &Watchdog{
		store:      s,
		docker:     d,
		opencode:   oc,
		beads:      b,
		am:         am,
		limits:     limits,
		source:     &dockerLogsSource{},
		killPane:   tmuxKillPane,
		runCancels: map[int64]context.CancelFunc{},
	}
}

// SetEventSource overrides the default docker-logs source. Used by tests to
// inject a fake stream.
func (w *Watchdog) SetEventSource(s EventSource) { w.source = s }

// Start launches the wallclock ticker. Subsequent calls are no-ops. The ctx
// scopes the lifetime of every goroutine the watchdog owns.
func (w *Watchdog) Start(ctx context.Context) error {
	w.startMu.Lock()
	defer w.startMu.Unlock()
	if w.started {
		return nil
	}
	w.rootCtx, w.rootCancel = context.WithCancel(ctx)
	w.started = true

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.tickLoop(w.rootCtx)
	}()
	return nil
}

// Stop cancels every goroutine the watchdog owns and blocks until they exit.
// Idempotent; safe to call before Start.
func (w *Watchdog) Stop() {
	w.startMu.Lock()
	if !w.started {
		w.startMu.Unlock()
		return
	}
	w.startMu.Unlock()
	if w.rootCancel != nil {
		w.rootCancel()
	}
	w.wg.Wait()
	w.startMu.Lock()
	w.started = false
	w.startMu.Unlock()
}

// Register attaches a per-operator consumer. The consumer goroutine reads
// the operator's nd-JSON stream, accumulates tokens/USD, persists running
// totals, and triggers kills on ceiling breach.
//
// projectName and projectPath are needed by the kill path to format the am
// subject + invoke `bd -C <path> set-state`. They are not on store.AgentRun
// (which only stores project_id) and must be threaded through by the caller.
//
// Calling Register before Start panics (programmer error — wire startup right).
func (w *Watchdog) Register(run store.AgentRun, projectName, projectPath string) {
	w.startMu.Lock()
	if !w.started {
		w.startMu.Unlock()
		panic("ceilings: Register called before Start")
	}
	w.startMu.Unlock()

	st := newRunState(run)
	st.SetProjectMeta(projectName, projectPath)
	w.runs.Store(run.ID, st)

	ctx, cancel := context.WithCancel(w.rootCtx)
	w.runCancelsMu.Lock()
	w.runCancels[run.ID] = cancel
	w.runCancelsMu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.consume(ctx, st)
	}()
}

// Unregister detaches a per-operator consumer. Safe to call multiple times.
// Removes the run from the live map BEFORE cancelling so concurrent
// RunUsage / Range observers do not see a half-torn-down state.
func (w *Watchdog) Unregister(runID int64) {
	w.runs.Delete(runID)
	w.runCancelsMu.Lock()
	cancel, ok := w.runCancels[runID]
	if ok {
		delete(w.runCancels, runID)
	}
	w.runCancelsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RunUsage returns the watchdog's live tokens/USD totals for one run. ok is
// false when the run is not registered (already unregistered or never was).
// The TUI calls this every render to display current consumption.
func (w *Watchdog) RunUsage(runID int64) (tokens int64, usd float64, ok bool) {
	v, found := w.runs.Load(runID)
	if !found {
		return 0, 0, false
	}
	st := v.(*runState)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.tokensUsed, st.usdUsed, true
}

// Limits returns the watchdog's configured limits. Read-only snapshot.
func (w *Watchdog) Limits() Limits { return w.limits }

// nowFunc is overridable in tests so wallclock checks are deterministic.
// Production reads time.Now.
var nowFunc = time.Now

// EventToken sums the input + output token counts on a step_finish event.
// Cache + reasoning tokens are intentionally excluded — see share/contracts.md
// "tokens.total may differ from input+output ... U10 uses input+output
// (matches what gets billed cumulatively per turn)".
func eventTokens(ev opencode.Event) int64 {
	return int64(ev.Part.Tokens.Input) + int64(ev.Part.Tokens.Output)
}
