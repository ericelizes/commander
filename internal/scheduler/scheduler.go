// Package scheduler implements U17 (auto-spawn with confirmation) and U18
// (capped no-confirmation auto-spawn).
//
// Once started, a Scheduler periodically scans every activated project for
// ready Tasks — bd issues labeled kind:task that have no associated
// agent_runs row yet — and proposes to spawn an operator for each. The
// proposal path is gated by the per-project AutoSpawn config flag:
//
//   - AutoSpawn = false (default): emit a ProposalEvent on Out(); the caller
//     surfaces it as a chat confirmation in the Lead view. the user's "yes"
//     turns into Confirm(), which triggers Spawner.Spawn. "no" turns into
//     Decline(), which suppresses re-proposing the same Task for 24h.
//   - AutoSpawn = true: bypass the chat prompt; the scheduler calls
//     Spawner.Spawn directly. The cost ceilings + per-workstream Operator cap
//     still apply (the cap is checked here; cost ceilings are enforced
//     downstream by the existing watchdog).
//
// The per-workstream cap is ConfigSource.CheckCapacity, asked for each ready
// Task before either path. A Task whose workstream is full is skipped for the
// current scan and tried again next interval.
//
// Decline persistence is intentionally in-memory only: the 24h suppression
// window is a soft UX nicety (avoid pestering the user about a Task he just
// said "not now" to), not a correctness requirement. A commander restart
// re-asks; that's acceptable. If we need durable suppression later, a
// declined_proposals table is the obvious next step.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
)

// declineWindow is how long a Decline'd Task is suppressed from re-proposal.
const declineWindow = 24 * time.Hour

// outBuffer is the buffered capacity of the proposal channel. On overflow
// the scheduler drops the oldest pending proposal so the latest scan still
// reaches the consumer.
const outBuffer = 32

// SchedulerStore is the narrow slice of *store.Store the scheduler needs.
// Defined as an interface so tests substitute a fake without standing up a
// real SQLite database. *store.Store satisfies this interface.
type SchedulerStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetAgentRunByTaskBeadID(ctx context.Context, taskBeadID string) (*store.AgentRun, error)
}

// SchedulerSpawner is the narrow slice of *spawn.Spawner the scheduler needs.
// *spawn.Spawner satisfies this interface.
type SchedulerSpawner interface {
	Spawn(ctx context.Context, req spawn.SpawnRequest) (*spawn.SpawnResult, error)
}

// ConfigSource exposes the bits of *config.Config the scheduler reads on
// every scan. Implementations should be safe to call from a background
// goroutine — i.e. read from a snapshot, not a mutable struct held under a
// non-locked pointer.
type ConfigSource interface {
	// AutoSpawnEnabled reports whether the per-project auto_spawn flag is
	// set. Unknown projects must return false (fail-safe).
	AutoSpawnEnabled(projectName string) bool
	// CheckCapacity refuses (non-nil) one more Operator for the Task when its
	// workstream already runs its cap of them (spawn.CheckWorkstreamCap).
	CheckCapacity(ctx context.Context, p store.Project, taskBeadID string) error
}

// ProposalEvent fires when the scheduler finds a ready Task. The same event
// shape is emitted regardless of AutoSpawn — the AutoSpawn field tells the
// caller which path the scheduler is on for telemetry/logging. AutoSpawn
// proposals are only emitted on Out() if the spawn fails; happy-path
// AutoSpawn skips the channel entirely (the operator just appears in the
// Lead view via the existing agent_runs path).
type ProposalEvent struct {
	ProjectID   int64
	ProjectName string
	ProjectPath string
	ProjectType string
	TaskBeadID  string
	TaskTitle   string
	AutoSpawn   bool
}

// Scheduler scans bd for ready Tasks and either proposes or auto-spawns.
type Scheduler struct {
	beads    beads.Client
	store    SchedulerStore
	spawner  SchedulerSpawner
	config   ConfigSource
	interval time.Duration

	out  chan ProposalEvent
	stop chan struct{}
	wg   sync.WaitGroup

	mu        sync.Mutex
	declined  map[string]time.Time // taskBeadID -> when declined
	now       func() time.Time
	startOnce sync.Once
	stopOnce  sync.Once
}

// New constructs a Scheduler. interval ≤ 0 falls back to 60s. The returned
// Scheduler is inert until Start is called.
func New(b beads.Client, s SchedulerStore, sp SchedulerSpawner, cfg ConfigSource, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Scheduler{
		beads:    b,
		store:    s,
		spawner:  sp,
		config:   cfg,
		interval: interval,
		out:      make(chan ProposalEvent, outBuffer),
		stop:     make(chan struct{}),
		declined: make(map[string]time.Time),
		now:      time.Now,
	}
}

// Start launches the scan loop in a background goroutine. Idempotent — a
// second Start call is a no-op. ctx cancellation and Stop both unwind the
// goroutine and close Out(). The first scan runs immediately so the caller
// doesn't wait a full interval for the initial pass.
func (s *Scheduler) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.loop(ctx)
	})
}

// Stop ends the scan loop and closes Out(). Idempotent.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
	})
	s.wg.Wait()
}

// Out returns the receive-only proposal channel. Closed when the scheduler
// stops.
func (s *Scheduler) Out() <-chan ProposalEvent {
	return s.out
}

// Confirm is the U17 "yes" path: trigger a spawn for a previously-emitted
// proposal. Capacity is re-checked at confirm time because the user may have
// sat on the prompt long enough for other operators to fill the workstream.
func (s *Scheduler) Confirm(ctx context.Context, ev ProposalEvent) error {
	p := store.Project{ID: ev.ProjectID, Name: ev.ProjectName, Path: ev.ProjectPath, Type: ev.ProjectType}
	if err := s.config.CheckCapacity(ctx, p, ev.TaskBeadID); err != nil {
		return fmt.Errorf("scheduler: confirm: %w", err)
	}
	if _, err := s.spawner.Spawn(ctx, spawn.SpawnRequest{
		ProjectID:   ev.ProjectID,
		ProjectName: ev.ProjectName,
		ProjectPath: ev.ProjectPath,
		ProjectType: ev.ProjectType,
		TaskBeadID:  ev.TaskBeadID,
	}); err != nil {
		return fmt.Errorf("scheduler: confirm: spawn: %w", err)
	}
	return nil
}

// Decline is the U17 "not now" path: suppress this Task for the next 24h.
// Safe to call from any goroutine.
func (s *Scheduler) Decline(taskBeadID string) {
	s.mu.Lock()
	s.declined[taskBeadID] = s.now()
	s.mu.Unlock()
}

// loop runs the scan tick loop until ctx is canceled or Stop is called.
func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	defer close(s.out)

	// Run an immediate scan so the first tick isn't an interval-long wait.
	s.scan(ctx)

	t := time.NewTicker(s.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			s.scan(ctx)
		}
	}
}

// scan runs one pass over every project. Per-project errors are swallowed
// (logging is the caller's job) — one bad project shouldn't poison the
// whole loop.
func (s *Scheduler) scan(ctx context.Context) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, p := range projects {
		s.scanProject(ctx, p)
	}
}

// scanProject scans one project for ready Tasks and emits or spawns.
func (s *Scheduler) scanProject(ctx context.Context, p store.Project) {
	tasks, err := s.beads.List(ctx, p.Path, beads.ListOptions{
		Labels: []string{"kind:task"},
		Status: "open",
	})
	if err != nil {
		return
	}

	autoSpawn := s.config.AutoSpawnEnabled(p.Name)

	for _, t := range tasks {
		if !s.taskReady(ctx, t) {
			continue
		}
		if s.config.CheckCapacity(ctx, p, t.ID) != nil {
			continue
		}
		ev := ProposalEvent{
			ProjectID:   p.ID,
			ProjectName: p.Name,
			ProjectPath: p.Path,
			ProjectType: p.Type,
			TaskBeadID:  t.ID,
			TaskTitle:   t.Title,
			AutoSpawn:   autoSpawn,
		}
		if autoSpawn {
			if _, err := s.spawner.Spawn(ctx, spawn.SpawnRequest{
				ProjectID:   p.ID,
				ProjectName: p.Name,
				ProjectPath: p.Path,
				ProjectType: p.Type,
				TaskBeadID:  t.ID,
			}); err != nil {
				// Surface the failure on Out() so the Lead view can alert
				// the user — auto-spawn shouldn't fail silently.
				s.emit(ev)
				continue
			}
		} else {
			s.emit(ev)
		}
	}
}

// taskReady reports whether t is eligible: not declined within window, not
// already attached to a running operator. List queries already filter by
// status=open so we don't re-check that here.
func (s *Scheduler) taskReady(ctx context.Context, t beads.Bead) bool {
	if !hasLabel(t.Labels, "kind:task") {
		return false
	}
	s.mu.Lock()
	when, declined := s.declined[t.ID]
	if declined && s.now().Sub(when) >= declineWindow {
		delete(s.declined, t.ID)
		declined = false
	}
	s.mu.Unlock()
	if declined {
		return false
	}
	run, err := s.store.GetAgentRunByTaskBeadID(ctx, t.ID)
	if err != nil {
		return false
	}
	if run != nil {
		return false
	}
	return true
}

// emit ships an event to Out() without blocking the scan loop. On a full
// channel we drop the oldest pending event (read-then-discard) so the
// freshest proposal still lands.
func (s *Scheduler) emit(ev ProposalEvent) {
	select {
	case s.out <- ev:
	default:
		// Drop oldest, then push.
		select {
		case <-s.out:
		default:
		}
		select {
		case s.out <- ev:
		default:
		}
	}
}

// hasLabel reports whether labels contains target. Defensive — bd's --label
// filter already enforces the membership, but a misconfigured shim could
// surface unrelated beads.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, target) {
			return true
		}
	}
	return false
}
