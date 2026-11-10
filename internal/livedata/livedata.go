// Package livedata is the UI-agnostic cross-fleet view the cockpit renders:
// projects, workstreams and their step trees, NEEDS YOU, live agent states,
// fleet metrics and unread mail. The TUI sidebar and the cmdrd HTTP API both
// build on it; it must never import bubbletea or lipgloss.
package livedata

import (
	"context"
	"errors"
	"fmt"
	"github.com/example-user/commander/internal/ownercompat"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// Data is the cross-fleet snapshot the Commander view renders against.
//
// Partial loads are tolerated: any per-source error is captured in LoadErr
// and whatever populated successfully is still returned.
type Data struct {
	OrchestrationMode  string
	CanonicalSnapshot  *Snapshot `json:"canonical_snapshot,omitempty"` // shared projection for terminal consumers
	workstreams        []Workstream
	Postgres           bool                      `json:"postgres,omitempty"`
	ObservedExecutions []store.ObservedExecution `json:"-"` // nil retains legacy projection; non-nil is current PG evidence
	Projects           []store.Project
	Objectives         []ObjectiveRow
	Operators          []OperatorRow
	Ranks              map[store.WorkstreamKey]int `json:"-"` // the user's priority order; see workstream_ranks
	BeadRanks          map[store.WorkstreamKey]int `json:"-"` // each bead's priority among its siblings; see bead_ranks
	Agents             map[string]store.AgentState // live state per agent mail role (hooks + scrape)
	Checkins           map[string]store.Checkin    // each Manager's next check-in on its running Operators (and Commander's)
	Finished           []store.CostEvent           // Operators whose process ended in the last FinishedFor
	Issues             []store.WatchIssue          // what the minute watch finds wrong across workstreams
	Metrics            FleetMetrics
	LoadedAt           time.Time
	LoadErr            error
	// ProjectTimes is how long each project's beads read took in this load.
	ProjectTimes map[string]time.Duration `json:"-"`
	// Phases is where this load's time went.
	Phases LoadPhases `json:"-"`
	// KeptLastGood: a project's read overlapped writes, so its rows are the last good
	// ones and may not show those writes. Quiet: LoadErr stays nil.
	KeptLastGood bool `json:"-"`
}

// Parents is each loaded bead's parent by its parent-child edge, for
// beads.Lineage: who owns a bead is decided from what was read, at no br cost.
func (d Data) Parents() beads.Parents {
	out := make(beads.Parents, len(d.Objectives))
	for _, r := range d.Objectives {
		if r.BeadID != "" {
			out[r.BeadID] = r.ParentBeadID
		}
	}
	return out
}

// ObjectiveRow is a flattened cross-project objective (a workstream or one of
// its steps). ProjectName is best-effort: the first linked project's name.
type ObjectiveRow struct {
	Withdrawn                                           bool  // the withdrawn label: a cancelled request, never anyone's move
	ProgressAt                                          int64 // last recorded progress (unix s)
	PlanningPending                                     bool
	ResultRunID, ResultReadyAt                          int64
	QuestionWaiting                                     bool
	HumanAnswerPending                                  bool
	ExecutionMode                                       string
	PlanRevision                                        int64
	ExecutionEligible                                   bool
	ExecutionReady, ExecutionReserved, ExecutionCapable bool
	ExecutionAttempts                                   int64
	ExecutionError                                      string
	PlanningCommander                                   string
	JurisdictionRevision                                int64
	WorkflowCheck                                       *workflowtemplate.Node
	GateChecks                                          []workflowtemplate.Node // open merge gates of this stage (gate beads are not rows)
	BeadID                                              string
	ParentBeadID                                        string
	ID                                                  int64
	Title                                               string
	ProjectName                                         string
	ProjectID                                           int64
	LinkedTasks                                         int
	Status                                              string
	Starred                                             bool
	Hold                                                bool            // the hold label on this bead
	HoldReason                                          string          // from its latest "hold by" comment
	CreatedAt                                           string          // RFC3339, from the bead; orders workstreams fleet-wide
	Ask                                                 string          // needs-user steps: what the user must do, from the needs_you table
	Note                                                string          // the full note behind the ask
	Decision                                            *store.Decision // the decision card behind the ask, if any
	AskSince                                            int64           // unix seconds the ask was raised (0 = no ask)
	State                                               string          // beads.State*; see beads.StepState
	Owner                                               string
	OwnerState                                          string // OwnerWorking/OwnerFinished/OwnerDied for an Operator commander spawned; "" = not one
	OwnerWhy                                            string // why the claim reads as it does when cmdrd corrected or settled it ("" = as recorded)
	OwnerSince                                          int64  // unix s its Operator stopped, when known
	Restarts                                            int    // automatic restarts cmdrd made of its Operator (auto-respawn / crash-respawn labels)
	PRRepo                                              string
	PR                                                  []string     // PR numbers of its pr: labels
	Tickets                                             []string     // Linear ids of its ticket:<ID> labels
	Review                                              string       // "4159" when the step is in review as that PR ("" = not in review)
	BlockedBy                                           []string     // bead IDs this step waits on ("blocks" dependencies)
	Until                                               string       // owned job: the finish line its owner keeps at ("merged"), from the until:<x> label
	Gate                                                bool         // a human approval gate (label gate)
	Approvers                                           []string     // who approves a gate (approver:<who> labels)
	AskAnswered                                         bool         // the last ask was answered; only an explicit re-flag raises another
	GateNote                                            string       // a gate's write-up (the bead description)
	ParkedUntil                                         int64        // unix s a parked (deferred) bead comes back (label parked-until:<unix>); 0 = none
	Waits                                               []beads.Wait // what its owner is waiting on (waiting:<since>:<kind>... labels), oldest first
	Gates                                               []store.Gate // checks the step must pass before it closes, in the order added
	GateIDs                                             []string     // the gate bead behind each of Gates ("" for a row not yet migrated from the gates table)
	GateViz                                             []int        // each gate bead's own viz:<n> label (0 = none), parallel to Gates
	Viz                                                 int          // the bead's own viz:<n> label (0 = none); see EffectiveViz
	Task                                                bool         // an Operator task bead (kind:task, not an objective, gate or track): agent-internal, not a step the user plans
	KindTask                                            bool         // labelled kind:task at all (also with kind:objective, e.g. converted then --no-workstream): never a workstream row, as wsmgr refuses to register one
	// Owns: a nested workstream with a Manager of its own (an open kind:workstream
	// bead under another, in a project whose nested Managers are on); see beads.Owner.
	Owns        bool
	Track       bool         // a nested workstream: a separate outcome with its own gate chain, under another workstream
	Rank        int          // its priority among its siblings (1 = first); 0 follows in the order added
	Superseded  bool         // dropped from the plan (label superseded/retired, or closed as superseded): never an approval
	Proposed    string       // a Manager's proposal Commander has not reviewed (beads.Proposed*): never NEEDS YOU
	ProposedAt  int64        // unix s it was proposed
	Lane        string       // the bead's lane label value (lane:<v>), for a step whose lane nothing else records
	Criteria    string       // what counts as passed (the bead's acceptance criteria)
	CloseReason string       // a closed bead's close reason, trimmed: what its Operator or Manager said it ended with
	PushCount   int          `json:"push_count,omitempty"`
	PushFirst   int64        `json:"push_first,omitempty"`
	SpecHash    string       // beads.SpecHash of the bead: title+description+acceptance, the fingerprint a card is raised against
	Basis       *store.Basis // what the open card was raised against (nil = raised before it was recorded)

	// What a closed row folded away and a reader still needs (attachFoldedOutcomes).
	FinishedTasks []FinishedTask // the latest finished Operator tasks under this closed row
	DoneBlockers  []string       // BlockedBy ids that are finished and not drawn: they hold nothing
}

// OperatorRow is one running operator joined from agent_runs. Status uses
// the view-layer vocabulary (working/blocked/needs-input/done/idle).
type OperatorRow struct {
	RunID       int64
	ProjectID   int64
	AgentName   string
	Status      string
	TaskBeadID  string
	TokensUsed  int64
	USDUsed     float64
	SpawnTime   time.Time
	SessionID   string
	ContainerID string
	TmuxPaneID  string
	ActiveAt    time.Time // last write to its transcript, as the minute watch saw it; zero when not yet seen
}

// FinishedFor is how long an Operator that ended stays visible in the app.
const FinishedFor = 10 * time.Minute

// diedFor is how far back a settled death's time and cause are looked up.
const diedFor = 24 * time.Hour

// FleetMetrics is the cross-fleet aggregate shown in the metrics card.
// RoleCost is a native chat role's spend; today = the last 24h, like the fleet total (est.: plan-equivalent, not billed).
type RoleCost struct {
	Role         string
	TodayUSD     float64
	SessionUSD   float64
	TotalUSD     float64
	InputTokens  int64
	OutputTokens int64
}

type FleetMetrics struct {
	TotalCostUSD24h float64
	TotalCostUSD7d  float64
	CostSeries24h   []float64 // spend per hour, oldest first, for the sparkline
	// Chat spend (Commander and Managers) is already inside the totals above.
	ChatCostUSD24h float64
	RoleCosts      []RoleCost // per chat role, plan-equivalent estimates
	AgentCount     int
	ActiveCount    int
	BlockedCount   int
	IdleRatio      float64
	ProjectCount   int
}

// Deps is what Load reads. Scrape, when set, reads live agent states off the
// chat panes; it is optional so tests and headless callers skip tmux.
type Deps struct {
	projectRows *projectRowsCache
	Store       *store.Store
	Beads       beads.Client
	Scrape      func(now time.Time) map[string]store.AgentState
	// OnlyProject, when set, limits the load to the project of that name (one
	// project read instead of the whole fleet).
	OnlyProject string
}

// SlowLoad is the load time worth a log line: the board stands still for as long
// as a load holds the API's lock.
const SlowLoad = 10 * time.Second

// LoadPhases is where one load's time went: the wait for Source.mu (before and
// after the load), the store and tmux reads before the beads, the beads read, the
// gate read, the other store reads, and the snapshot build.
type LoadPhases struct {
	Lock, PreRead, Beads, Gates, Store, Build time.Duration
}

func (p LoadPhases) String() string {
	ms := func(d time.Duration) string { return d.Round(time.Millisecond).String() }
	return "lock " + ms(p.Lock) + ", pre-read " + ms(p.PreRead) + ", beads " + ms(p.Beads) + ", gates " + ms(p.Gates) + ", store " + ms(p.Store) + ", build " + ms(p.Build)
}

// ErrNoStore is returned in Data.LoadErr when Load is called without a Store.
var ErrNoStore = dataError("livedata: store dependency is nil")

type dataError string

func (e dataError) Error() string { return string(e) }

// Load hits the live backends and returns a cross-fleet snapshot. Errors
// from individual sources collapse into LoadErr.
func Load(ctx context.Context, deps Deps) Data {
	out := Data{LoadedAt: time.Now()}

	if deps.Store == nil {
		out.LoadErr = ErrNoStore
		return out
	}

	out.Postgres = deps.Store.IsPostgres()
	mark := out.LoadedAt
	lap := func(phase *time.Duration) {
		now := time.Now()
		*phase += now.Sub(mark)
		mark = now
	}

	projects, err := deps.Store.ListProjects(ctx)
	if err != nil {
		out.LoadErr = err
	} else {
		if deps.OnlyProject != "" {
			projects = filterProject(projects, deps.OnlyProject)
		}
		out.Projects = projects
	}

	if ranks, rankErr := deps.Store.ListWorkstreamRanks(ctx); rankErr == nil {
		out.Ranks = ranks
	}
	if ranks, rankErr := deps.Store.ListBeadRanks(ctx); rankErr == nil {
		out.BeadRanks = ranks
	}

	hooks := map[string]store.AgentState{}
	if agents, agentErr := deps.Store.ListAgentStates(ctx); agentErr == nil {
		for _, a := range agents {
			hooks[a.Role] = a
		}
	}
	var scraped map[string]store.AgentState
	if deps.Scrape != nil {
		scraped = deps.Scrape(out.LoadedAt)
	}
	if out.Postgres {
		out.Agents = hooks
	} else {
		out.Agents = MergeAgentStates(hooks, scraped, out.LoadedAt)
	}

	projectNameByID := make(map[int64]string, len(projects))
	for _, p := range projects {
		projectNameByID[p.ID] = p.Name
	}
	lap(&out.Phases.PreRead)

	if deps.Beads != nil {
		objectives, times, kept, objErr := objectiveRowsTimed(ctx, deps.Beads, projects, deps.projectRows)
		out.Objectives, out.ProjectTimes, out.KeptLastGood = objectives, times, kept
		if objErr != nil && out.LoadErr == nil {
			out.LoadErr = objErr
		}
	} else {
		objectives, objErr := deps.Store.ListObjectives(ctx, "")
		if objErr != nil && out.LoadErr == nil {
			out.LoadErr = objErr
		} else if objErr == nil {
			out.Objectives = objectiveRowsFromStore(ctx, deps.Store, objectives, projectNameByID)
		}
	}
	lap(&out.Phases.Beads)
	mode, executionErr := attachExecutionState(ctx, deps.Store, out.Objectives)
	out.OrchestrationMode = mode
	if executionErr != nil && out.LoadErr == nil {
		out.LoadErr = executionErr
	}

	out.Objectives = MarkOperatorOwners(ctx, deps.Store, out.Projects, out.Objectives)
	if died, err := deps.Store.ListCostEventsSince(ctx, out.LoadedAt.Add(-diedFor).Unix(), store.EventOperatorDied); err == nil {
		out.Objectives = markDied(out.Objectives, died)
	}
	attachAsks(ctx, deps.Store, out.Objectives)
	lap(&out.Phases.Store)
	attachGates(ctx, deps.Store, deps.Beads, projects, out.Objectives)
	lap(&out.Phases.Gates)

	runs, runErr := deps.Store.ListRunningRuns(ctx)
	if runErr != nil && out.LoadErr == nil {
		out.LoadErr = runErr
	} else if runErr == nil {
		out.Operators = OperatorsFromRuns(runs)
		if watches, err := deps.Store.RunWatches(ctx); err == nil {
			for i, op := range out.Operators {
				if at := watches[op.RunID].ActiveAt; at > 0 {
					out.Operators[i].ActiveAt = time.Unix(at, 0)
				}
			}
		}
	}
	if checkins, err := deps.Store.Checkins(ctx); err == nil {
		out.Checkins = checkins
	}
	if fin, err := deps.Store.ListCostEventsSince(ctx, out.LoadedAt.Add(-FinishedFor).Unix(), store.EventOperatorExited, store.EventOperatorDied); err == nil {
		out.Finished = fin
	}
	if issues, err := deps.Store.WatchIssues(ctx); err == nil {
		out.Issues = issues
	}

	out.Metrics = fleetMetricsFromStore(ctx, deps.Store, out.Operators, len(out.Projects), out.LoadedAt)
	lap(&out.Phases.Store)

	return out
}

// objectiveRowsFromStore flattens objectives + their objective_tasks join
// counts into ObjectiveRow. ProjectName is taken from the first join row.
func objectiveRowsFromStore(ctx context.Context, st *store.Store, objs []store.Objective, projectNameByID map[int64]string) []ObjectiveRow {
	out := make([]ObjectiveRow, 0, len(objs))
	for _, o := range objs {
		row := ObjectiveRow{
			ID:     o.ID,
			Title:  o.Title,
			Status: o.Status,
		}

		joins, err := st.ListObjectiveTasksByObjective(ctx, o.ID)
		if err == nil {
			row.LinkedTasks = len(joins)
			if len(joins) > 0 {
				row.ProjectID = joins[0].ProjectID
				if name, ok := projectNameByID[joins[0].ProjectID]; ok {
					row.ProjectName = name
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// How a claimed step's Operator is doing, from its process alone: alive,
// exited after handing the step on (review or needs you: it reported), or
// exited with the step still open (it died).
const (
	OwnerWorking  = "working"
	OwnerFinished = "finished"
	OwnerDied     = "died"
)

// MarkOperatorOwners sets OwnerState on steps whose Operator commander spawned.
// The claim (the owner label on the bead) is the truth of who is on a step; the
// process only decorates it, so an exited Operator's claim stays, marked
// finished or died. Owners commander never spawned are left unmarked.
func MarkOperatorOwners(ctx context.Context, st *store.Store, projects []store.Project, rows []ObjectiveRow) []ObjectiveRow {
	running := make(map[int64]map[string]bool, len(projects))
	known := make(map[int64]map[string]bool, len(projects))
	onTask := make(map[int64]map[string]string, len(projects)) // task bead -> its running Operator
	for _, p := range projects {
		runs, err := st.ListAgentRunsByProject(ctx, p.ID, "")
		if err != nil {
			continue
		}
		running[p.ID], known[p.ID], onTask[p.ID] = map[string]bool{}, map[string]bool{}, map[string]string{}
		for _, r := range runs {
			name := beads.OwnerLabelName(r.AgentName) // owner labels are sanitized (no dots)
			known[p.ID][name] = true
			if r.Status == "running" {
				running[p.ID][name] = true
				onTask[p.ID][r.TaskBeadID] = name
			}
		}
	}
	for i := range rows {
		r := &rows[i]
		// The spawn writes the run row before the bead's owner label (and the
		// snapshot's bead read can predate that label): a running run on the
		// task is the claim, so a fresh Operator never reads as ownerless.
		if r.Owner == "" && r.Status != "closed" {
			if name := onTask[r.ProjectID][r.BeadID]; name != "" {
				r.Owner = name
			}
		}
		switch {
		case running[r.ProjectID][r.Owner]:
			r.OwnerState = OwnerWorking
		case known[r.ProjectID][r.Owner]:
			r.OwnerState = OwnerDied
			if r.State == beads.StateReview || r.State == beads.StateNeedsUser || r.State == beads.StateDone {
				r.OwnerState = OwnerFinished
			}
		}
	}
	return rows
}

// ClearFinishedOperatorOwners drops "→ operator-x" from steps whose Operator
// was spawned by commander but is no longer running, so a step shows its
// Operator only while it is live. Owners commander never spawned are left alone.
func ClearFinishedOperatorOwners(ctx context.Context, st *store.Store, projects []store.Project, rows []ObjectiveRow) []ObjectiveRow {
	finished := make(map[int64]map[string]bool, len(projects))
	for _, p := range projects {
		runs, err := st.ListAgentRunsByProject(ctx, p.ID, "")
		if err != nil {
			continue
		}
		running := map[string]bool{}
		known := map[string]bool{}
		for _, r := range runs {
			name := beads.OwnerLabelName(r.AgentName) // owner labels are sanitized (no dots)
			known[name] = true
			if r.Status == "running" {
				running[name] = true
			}
		}
		done := map[string]bool{}
		for name := range known {
			if !running[name] {
				done[name] = true
			}
		}
		finished[p.ID] = done
	}
	for i := range rows {
		if finished[rows[i].ProjectID][rows[i].Owner] {
			rows[i].Owner = ""
		}
	}
	return rows
}

// attachAsks fills Ask/Note/Decision on needs-user rows from the needs_you table.
func attachAsks(ctx context.Context, st *store.Store, rows []ObjectiveRow) {
	asks, err := st.ListNeedsYou(ctx)
	if err != nil || len(asks) == 0 {
		return
	}
	byKey := make(map[store.WorkstreamKey]store.NeedsYou, len(asks))
	for _, a := range asks {
		byKey[store.WorkstreamKey{ProjectID: a.ProjectID, BeadID: a.BeadID}] = a
	}
	for i := range rows {
		if a, ok := byKey[store.WorkstreamKey{ProjectID: rows[i].ProjectID, BeadID: rows[i].BeadID}]; ok && rows[i].State == beads.StateNeedsUser {
			rows[i].Ask, rows[i].Note, rows[i].AskSince = a.Ask, a.Note, a.Since
			rows[i].Basis = a.Basis
			if d, err := store.ParseDecision(a.Decision); a.Decision != "" && err == nil {
				rows[i].Ask = d.Question // the queue shows what the user is deciding, in full
				rows[i].Decision = d
			}
		}
	}
}

// attachGates fills Gates on the rows that have any: the gate beads of each
// project, then any gates-table rows `internal-gate migrate` has not moved yet.
func attachGates(ctx context.Context, st *store.Store, bc beads.Client, projects []store.Project, rows []ObjectiveRow) {
	type gate struct {
		g   store.Gate
		id  string
		viz int
	}
	byKey := map[store.WorkstreamKey][]gate{}
	if bc != nil {
		all := make([][]gates.Node, len(projects))
		var wg sync.WaitGroup
		for i, p := range projects {
			wg.Add(1)
			go func() {
				defer wg.Done()
				all[i], _ = gates.All(ctx, bc, p.Path, p.ID) // a failed read shows no gates
			}()
		}
		wg.Wait()
		for i, ns := range all {
			for _, n := range ns {
				k := store.WorkstreamKey{ProjectID: projects[i].ID, BeadID: n.Gate.BeadID}
				byKey[k] = append(byKey[k], gate{n.Gate, n.ID, n.Viz})
			}
		}
	}
	if legacy, err := st.ListAllGates(ctx); err == nil {
		for _, g := range legacy {
			k := store.WorkstreamKey{ProjectID: g.ProjectID, BeadID: g.BeadID}
			byKey[k] = append(byKey[k], gate{g: g})
		}
	}
	for i := range rows {
		for _, g := range byKey[store.WorkstreamKey{ProjectID: rows[i].ProjectID, BeadID: rows[i].BeadID}] {
			rows[i].Gates = append(rows[i].Gates, g.g)
			rows[i].GateIDs = append(rows[i].GateIDs, g.id)
			rows[i].GateViz = append(rows[i].GateViz, g.viz)
		}
	}
}

// starLabel marks an objective the user starred as a focus item.
const starLabel = "star"

// objectiveRowsFromBeads reads the projects side by side: a cold read (after a
// restart, with nothing cached) is a few br calls per project, and the app's
// start screen waits on it.
func objectiveRowsFromBeads(ctx context.Context, bc beads.Client, projects []store.Project) ([]ObjectiveRow, error) {
	return objectiveRowsFromBeadsCached(ctx, bc, projects, nil)
}

type projectRowsKey struct {
	id         int64
	path, name string
}

func rowsKey(p store.Project) projectRowsKey { return projectRowsKey{p.ID, p.Path, p.Name} }

type nativeProjectRows struct {
	revision int64
	rows     []ObjectiveRow
}

type projectRowsCache struct {
	mu     sync.Mutex
	native map[projectRowsKey]nativeProjectRows
	rows   map[projectRowsKey][]ObjectiveRow
	// misses counts consecutive ticks a project's read overlapped writes.
	misses map[projectRowsKey]int
}

// busyMissLimit is how many consecutive overlapped reads of one project make
// it degraded ("busy"): one overlap is normal on a busy board.
const busyMissLimit = 5

type projectReadError struct{ err error }

func (e *projectReadError) Error() string { return e.err.Error() }
func (e *projectReadError) Unwrap() error { return e.err }

// OnlyProjectReadErrors reports whether err is made up solely of per-project
// br read failures: the other projects' rows are still good, so a caller may
// use them and flag the failed projects instead of failing outright.
func OnlyProjectReadErrors(err error) bool {
	if err == nil {
		return false
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range multi.Unwrap() {
			if !OnlyProjectReadErrors(e) {
				return false
			}
		}
		return true
	}
	var pe *projectReadError
	return errors.As(err, &pe)
}

func objectiveRowsFromBeadsCached(ctx context.Context, bc beads.Client, projects []store.Project, cache *projectRowsCache) ([]ObjectiveRow, error) {
	rows, _, _, err := objectiveRowsTimed(ctx, bc, projects, cache)
	return rows, err
}

// objectiveRowsTimed also says how long each project's read took, and whether a
// read that overlapped writes quietly kept a project's last-good rows.
func objectiveRowsTimed(ctx context.Context, bc beads.Client, projects []store.Project, cache *projectRowsCache) ([]ObjectiveRow, map[string]time.Duration, bool, error) {
	nativeCache := cache
	if beads.PostgresConfigured() {
		cache = nil
	}
	if cache != nil {
		cache.mu.Lock()
		live := make(map[projectRowsKey]bool, len(projects))
		for _, p := range projects {
			live[rowsKey(p)] = true
		}
		for key := range cache.rows {
			if !live[key] {
				delete(cache.rows, key)
			}
		}
		cache.mu.Unlock()
	}
	rows := make([][]ObjectiveRow, len(projects))
	errs := make([]error, len(projects))
	took := make([]time.Duration, len(projects))
	var kept atomic.Bool
	var wg sync.WaitGroup
	for i, project := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			defer func() { took[i] = time.Since(start) }()
			if beads.PostgresConfigured() {
				errs[i] = beads.ReadPostgresSnapshot(ctx, func(readCtx context.Context) error {
					revision, err := beads.PostgresProjectRevision(readCtx, project.Path)
					if err != nil {
						return err
					}
					key := rowsKey(project)
					if nativeCache != nil {
						nativeCache.mu.Lock()
						cached, ok := nativeCache.native[key]
						nativeCache.mu.Unlock()
						if ok && cached.revision == revision {
							rows[i] = cloneObjectiveRows(cached.rows)
							return nil
						}
					}
					rows[i], err = projectObjectiveRows(readCtx, bc, project)
					if err == nil && nativeCache != nil {
						nativeCache.mu.Lock()
						if nativeCache.native == nil {
							nativeCache.native = map[projectRowsKey]nativeProjectRows{}
						}
						nativeCache.native[key] = nativeProjectRows{revision, cloneObjectiveRows(rows[i])}
						nativeCache.mu.Unlock()
					}
					return err
				})
			} else {
				errs[i] = beads.ReadConsistent(ctx, project.Path, func() error {
					var err error
					rows[i], err = projectObjectiveRows(ctx, bc, project)
					return err
				})
			}
			readErr := errs[i]
			if cache != nil {
				cache.mu.Lock()
				key := rowsKey(project)
				if cache.misses == nil {
					cache.misses = map[projectRowsKey]int{}
				}
				if errors.Is(errs[i], beads.ErrChangedDuringRead) {
					cache.misses[key]++
					if _, warm := cache.rows[key]; !warm {
						// No last-good rows to keep: an empty answer would make
						// the project vanish from the board.
						errs[i] = errors.New("not read yet (warming): beads changed during the first read")
					} else if cache.misses[key] < busyMissLimit {
						log.Printf("livedata: %s changed during read (miss %d), keeping last-good rows", project.Name, cache.misses[key])
						errs[i] = nil
						kept.Store(true)
					} else {
						errs[i] = errors.New("busy: beads changing continuously")
					}
				} else {
					delete(cache.misses, key)
				}
				var partial *beads.PartialDetailsError
				if errors.As(errs[i], &partial) && len(cache.rows[rowsKey(project)]) == 0 {
					// Cold start: no last-good rows, so show what was read.
				} else if beads.PostgresConfigured() && errs[i] != nil {
					rows[i] = nil
				} else if errs[i] != nil || errors.Is(readErr, beads.ErrChangedDuringRead) {
					rows[i] = slices.Clone(cache.rows[rowsKey(project)])
				} else {
					cache.rows[rowsKey(project)] = slices.Clone(rows[i])
				}
				cache.mu.Unlock()
			}
			if errs[i] != nil {
				errs[i] = &projectReadError{fmt.Errorf("project %s: %w", project.Name, errs[i])}
			}
		}()
	}
	wg.Wait()
	out := make([]ObjectiveRow, 0)
	times := make(map[string]time.Duration, len(projects))
	for i, p := range projects {
		if !beads.NestedManagersOn(p.Path) {
			for j := range rows[i] {
				rows[i][j].Owns = false // one Manager per root while the switch is off
			}
		}
		out = append(out, rows[i]...)
		times[p.Name] = took[i]
	}
	return out, times, kept.Load(), errors.Join(errs...)
}

func projectObjectiveRows(ctx context.Context, bc beads.Client, project store.Project) ([]ObjectiveRow, error) {
	var out []ObjectiveRow
	// Include closed beads so finished steps stay visible (✓) under an open
	// workstream; closed workstreams and their steps are dropped below.
	objectiveBeads, err := bc.List(ctx, project.Path, beads.ListOptions{Labels: []string{"kind:objective"}, Status: "all"})
	if err != nil {
		return nil, err
	}
	// Approval cards can be gate-only records, rather than objectives.
	approvalBeads, err := bc.List(ctx, project.Path, beads.ListOptions{LabelAny: []string{beads.GateLabel, gates.KindLabel}, Status: "all"})
	if err != nil {
		return nil, err
	}
	objectiveIDs := map[string]bool{}
	approvalOnly := map[string]bool{}
	for _, b := range objectiveBeads {
		objectiveIDs[b.ID] = true
	}
	for _, b := range approvalBeads {
		if !objectiveIDs[b.ID] && (slices.Contains(b.Labels, beads.GateLabel) || (gates.IsGate(b) && len(Approvers(b.Labels)) > 0)) {
			objectiveBeads = append(objectiveBeads, b)
			approvalOnly[b.ID] = true
			objectiveIDs[b.ID] = true
		}
	}
	taskBeads, err := bc.List(ctx, project.Path, beads.ListOptions{Labels: []string{"kind:task"}, Status: "all"})
	if err != nil {
		return nil, err
	}
	// Operational dependencies are real work even though they are not objectives
	// or Operator tasks. Keep them in the graph so label filtering cannot release
	// a task that still waits on an open operational bead.
	operational, err := bc.List(ctx, project.Path, beads.ListOptions{Labels: []string{"kind:operational"}, Status: "all"})
	if err != nil {
		return nil, err
	}
	taskBeads = append(taskBeads, operational...)
	childCount := make(map[string]int, len(objectiveBeads))
	for _, task := range taskBeads {
		if task.ParentID != "" && task.Status != "closed" {
			childCount[task.ParentID]++
		}
	}
	// Task beads (an Operator's unit of work) sit in the tree under their
	// step like any other bead: the tree is every bead, as it is.
	seen := make(map[string]bool, len(objectiveBeads))
	for _, o := range objectiveBeads {
		seen[o.ID] = true
	}
	for _, t := range taskBeads {
		if !seen[t.ID] {
			seen[t.ID] = true
			objectiveBeads = append(objectiveBeads, t)
		}
	}
	closed := make(map[string]bool, len(objectiveBeads))
	for _, objective := range objectiveBeads {
		if objective.Status == "closed" {
			closed[objective.ID] = true
		}
	}
	details, err := beadDetails(ctx, bc, project.Path, objectiveBeads)
	var partial *beads.PartialDetailsError
	if err != nil && !errors.As(err, &partial) {
		return nil, err
	}
	reviewBlocks := map[string]bool{}
	for _, b := range objectiveBeads {
		if workflowtemplate.IsReviewBlock(b) {
			reviewBlocks[b.ID] = true
		}
	}
	byID := map[string]beads.Bead{}
	for _, b := range objectiveBeads {
		byID[b.ID] = b
	}
	eligible := func(id string) bool {
		b := byID[id]
		if slices.Contains(b.Labels, "hold") {
			return false
		}
		seen := map[string]bool{id: true}
		d, ok := details[id]
		if !ok || d.Parent == "" {
			return false
		}
		for cur := d.Parent; cur != ""; {
			if seen[cur] {
				return false
			}
			seen[cur] = true
			a, ok := byID[cur]
			if !ok || !slices.Contains(a.Labels, "kind:objective") || slices.Contains(a.Labels, "kind:task") || beads.Superseded(a) || a.Status == "tombstone" || gates.IsGate(a) || slices.Contains(a.Labels, beads.GateLabel) || slices.Contains(a.Labels, "hold") || a.Status == "closed" || a.Status == beads.StatusDeferred {
				return false
			}
			ad, ok := details[cur]
			if !ok {
				return false
			}
			if ad.Parent == "" && slices.Contains(a.Labels, beads.TrackLabel) {
				return false
			}
			cur = ad.Parent
		}
		return true
	}
	var expected []beads.Bead
	for _, objective := range objectiveBeads {
		parent := parentOf(details, objective.ID)
		// Legacy parentless approval cards stay unpublished until their owning
		// Manager explicitly attaches them; never promote one to a workstream.
		if approvalOnly[objective.ID] && !eligible(objective.ID) {
			continue
		}
		if objective.Status == "closed" && (parent == "" || closed[parent]) {
			continue
		}
		if foldedUnderClosed(objective.ID, closed, func(id string) string { return parentOf(details, id) }) {
			continue
		}
		expected = append(expected, objective)
		hold, holdReason := slices.Contains(objective.Labels, beads.HoldLabel), ""
		if hold {
			holdReason = beads.HoldReason(details[objective.ID].Comments)
		}
		stepState, stepOwner := beads.StepState(objective.Status, objective.Labels)
		proposal, _ := beads.ProposalOf(objective.Labels)
		blockedBy := blockingDeps(details[objective.ID])
		ownerState := ""
		if stepOwner == "" && slices.Contains(objective.Labels, beads.OperatorDiedLabel) {
			ownerState = OwnerDied // cmdrd cleared the dead Operator's claim
		}
		out = append(out, ObjectiveRow{
			PlanRevision: objective.PlanRevision, ExecutionReady: objective.ExecutionReady, ExecutionReserved: objective.ExecutionReserved, ExecutionCapable: objective.ExecutionCapable, ExecutionAttempts: objective.ExecutionAttempts, ExecutionError: objective.ExecutionError,
			PlanningCommander: objective.PlanningCommander, JurisdictionRevision: objective.JurisdictionRevision,
			WorkflowCheck: workflowNode(objective, details[objective.ID], reviewBlocks),
			BeadID:        objective.ID,
			ParentBeadID:  parent,
			Title:         objective.Title,
			ProjectName:   project.Name,
			ProjectID:     project.ID,
			LinkedTasks:   childCount[objective.ID],
			Status:        objective.Status,
			Starred:       slices.Contains(objective.Labels, starLabel),
			Hold:          hold,
			HoldReason:    holdReason,
			Withdrawn:     slices.Contains(objective.Labels, withdrawnLabel),
			ProgressAt:    objective.ProgressAt,
			CreatedAt:     objective.CreatedAt,
			State:         stepState,
			Owner:         stepOwner,
			OwnerState:    ownerState,
			Restarts:      restartsOf(objective.Labels),
			Review:        ReviewPR(objective.Labels),
			PR:            PRLabels(objective.Labels),
			Tickets:       TicketLabels(objective.Labels),
			PRRepo:        ReviewRepo(objective.Labels),
			BlockedBy:     blockedBy,
			Until:         UntilLabel(objective.Labels),
			Gate:          slices.Contains(objective.Labels, beads.GateLabel) || approvalOnly[objective.ID],
			Approvers:     Approvers(objective.Labels),
			AskAnswered:   slices.Contains(objective.Labels, beads.DecisionAnsweredLabel),
			GateNote:      gateNote(objective),
			Waits:         beads.WaitsOf(objective.Labels),
			ParkedUntil:   beads.ParkedUntil(objective.Labels),
			Viz:           beads.VizOf(objective.Labels),
			Task:          (slices.Contains(objective.Labels, "kind:task") || slices.Contains(objective.Labels, "kind:operational")) && !slices.Contains(objective.Labels, "kind:objective") && !slices.Contains(objective.Labels, beads.GateLabel) && !beads.IsWorkstream(objective.Labels),
			KindTask:      slices.Contains(objective.Labels, "kind:task"),
			Track:         beads.IsNested(objective.Labels, parent),
			Owns:          parent != "" && beads.OwnsWork(objective),
			Superseded:    supersededBead(objective),
			Proposed:      proposal.Kind,
			ProposedAt:    proposal.At,
			Lane:          beads.LaneOf(objective.Labels),
			Criteria:      strings.TrimSpace(objective.AcceptanceCriteria),
			CloseReason:   closeReasonOf(objective),
			SpecHash:      beads.SpecHash(objective),
			PushCount:     beads.PushStateOf(objective.Labels).Count,
			PushFirst:     beads.PushStateOf(objective.Labels).First,
		})
	}
	ownByChain(out, objectiveBeads, details)
	attachFoldedSteps(out, objectiveBeads, details, reviewBlocks)
	attachMergeGates(ctx, bc, project.Path, out, reviewBlocks)
	// Rows from a partial read are for display (unread beads fall back to their
	// dotted-id parent); the error marks the project degraded until it clears.
	if partial != nil {
		attachFoldedOutcomes(out, objectiveBeads, details)
		attachFoldedBlocks(out, objectiveBeads, details, reviewBlocks)
		return out, partial
	}
	out = dropOrphans(out, closed)
	attachFoldedOutcomes(out, objectiveBeads, details)
	attachFoldedBlocks(out, objectiveBeads, details, reviewBlocks)
	if len(out) == 0 && expectsRows(expected, closed, func(id string) string { return parentOf(details, id) }) {
		return nil, errors.New("rows missing: open beads were listed but no rows were built")
	}
	return out, nil
}

// foldedUnderClosed reports whether any ancestor of id (real parent edges, the
// dotted id only when a bead was not read) is closed: a finished workstream's
// whole subtree, at any depth, is not drawn and must not be promoted to roots.
func foldedUnderClosed(id string, closed map[string]bool, parentOf func(string) string) bool {
	seen := map[string]bool{id: true}
	for p := parentOf(id); p != "" && !seen[p]; p = parentOf(p) {
		if closed[p] {
			return true
		}
		seen[p] = true
	}
	return false
}

// expectsRows reports whether the row builder should have produced a row: some
// bead is open (not closed, deferred or superseded) and no ancestor of it is
// closed. Children of a closed parent are folded away on purpose.
func expectsRows(objectives []beads.Bead, closed map[string]bool, parentOf func(string) string) bool {
	for _, o := range objectives {
		if o.Status == "closed" || o.Status == beads.StatusDeferred || supersededBead(o) {
			continue
		}
		folded := false
		seen := map[string]bool{o.ID: true}
		for p := parentOf(o.ID); p != "" && !seen[p]; p = parentOf(p) {
			if closed[p] {
				folded = true
				break
			}
			seen[p] = true
		}
		if !folded {
			return true
		}
	}
	return false
}

// blockingDeps is what a step waits on: its "blocks" dependencies.
func blockingDeps(d beads.Detail) []string {
	var out []string
	for _, d := range d.Deps {
		if d.Type == "blocks" {
			out = append(out, d.DependsOnID)
		}
	}
	return out
}

// fleetMetricsFromStore builds the cross-fleet metrics from the live
// operators + cost_events windowed sums, falling back to a sum over the
// operators' UsdUsed when the cost queries come back empty.
func fleetMetricsFromStore(ctx context.Context, st *store.Store, ops []OperatorRow, projectCount int, now time.Time) FleetMetrics {
	var spotCost float64
	var blocked, active int
	for _, op := range ops {
		spotCost += op.USDUsed
		switch op.Status {
		case "blocked":
			blocked++
		case "working":
			active++
		}
	}

	var cost24h, cost7d float64
	if st != nil {
		nowUnix := now.Unix()
		if v, err := st.SumUSDByWindow(ctx, nowUnix, 24); err == nil {
			cost24h = v
		}
		if v, err := st.SumUSDByWindow(ctx, nowUnix, 24*7); err == nil {
			cost7d = v
		}
	}
	if cost24h == 0 {
		cost24h = spotCost
	}
	if cost7d == 0 {
		cost7d = spotCost
	}

	var series []float64
	if st != nil {
		if v, err := st.SumUSDByHour(ctx, now.Unix(), 24); err == nil {
			series = v
		}
	}

	var chat24h float64
	var roleCosts []RoleCost
	if st != nil {
		nowUnix := now.Unix()
		if v, err := st.SumChatUSDSince(ctx, nowUnix-24*3600); err == nil {
			chat24h = v
			cost24h += v
		}
		if v, err := st.SumChatUSDSince(ctx, nowUnix-24*7*3600); err == nil {
			cost7d += v
		}
		if v, err := st.ChatUSDByHour(ctx, nowUnix, 24); err == nil && len(series) == len(v) {
			for i := range series {
				series[i] += v[i]
			}
		}
		if rows, err := st.ChatCostsByRole(ctx, nowUnix-24*3600); err == nil {
			for _, r := range rows {
				roleCosts = append(roleCosts, RoleCost(r))
			}
		}
	}

	idle := 0.0
	if n := len(ops); n > 0 {
		idle = float64(n-active-blocked) / float64(n)
	}

	return FleetMetrics{
		TotalCostUSD24h: cost24h,
		TotalCostUSD7d:  cost7d,
		CostSeries24h:   series,
		ChatCostUSD24h:  chat24h,
		RoleCosts:       roleCosts,
		AgentCount:      len(ops),
		ActiveCount:     active,
		BlockedCount:    blocked,
		IdleRatio:       idle,
		ProjectCount:    projectCount,
	}
}

// ReviewPR returns the PR number of a step in review: labelled "in-review", with
// the number in a "pr:<n>" label ("" when not in review, "?" with no number).
// Approvers are who approves a gate: the values of its approver:<who> labels.
func Approvers(labels []string) []string {
	var out []string
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, beads.ApproverLabelPrefix); ok {
			out = append(out, ownercompat.Normalize(v))
		}
	}
	return out
}

// UntilLabel is an owned job's finish line: the value of its until:<x> label.
func UntilLabel(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "until:"); ok {
			return v
		}
	}
	return ""
}

func ReviewPR(labels []string) string {
	if !slices.Contains(labels, "in-review") {
		return ""
	}
	for _, l := range labels {
		if n, ok := strings.CutPrefix(l, "pr:"); ok && n != "" {
			_, num := splitPRRef(n)
			return num
		}
	}
	return "?"
}

// ReviewRepo is the owner/name of the repo a step's PR is in when its label
// names one (pr:<owner/name>#N, a PR outside the project's own repo), else "".
func ReviewRepo(labels []string) string {
	for _, l := range labels {
		if n, ok := strings.CutPrefix(l, "pr:"); ok && n != "" {
			repo, _ := splitPRRef(n)
			return repo
		}
	}
	return ""
}

// splitPRRef splits "owner/name#N" into the repo and N; a bare "N" has no repo.
func splitPRRef(ref string) (repo, num string) {
	if r, n, ok := strings.Cut(ref, "#"); ok && strings.Contains(r, "/") {
		return r, n
	}
	return "", ref
}

// OperatorsFromRuns converts agent_runs rows into OperatorRow, normalizing
// the status to view-layer terms (see StatusFromAgentRun).
func OperatorsFromRuns(runs []store.AgentRun) []OperatorRow {
	out := make([]OperatorRow, 0, len(runs))
	for _, r := range runs {
		row := OperatorRow{
			RunID:      r.ID,
			ProjectID:  r.ProjectID,
			AgentName:  r.AgentName,
			Status:     StatusFromAgentRun(r),
			TaskBeadID: r.TaskBeadID,
			TokensUsed: r.TokensUsed,
			USDUsed:    r.UsdUsed,
			SpawnTime:  time.Unix(r.SpawnTime, 0),
			SessionID:  r.OpencodeSessionID,
		}
		if r.ContainerID.Valid {
			row.ContainerID = r.ContainerID.String
		}
		if r.TmuxPaneID.Valid {
			row.TmuxPaneID = r.TmuxPaneID.String
		}
		out = append(out, row)
	}
	return out
}

// StatusFromAgentRun maps storage statuses to view-layer statuses.
func StatusFromAgentRun(r store.AgentRun) string {
	switch r.Status {
	case "running":
		return "working"
	case "killed-ceiling":
		return "blocked"
	case "closed", "exited":
		return "done"
	default:
		return r.Status
	}
}

// gateNote is the write-up behind an open gate: its bead description.
func gateNote(b beads.Bead) string {
	if b.Status == "closed" || (!slices.Contains(b.Labels, beads.GateLabel) && !(gates.IsGate(b) && len(Approvers(b.Labels)) > 0)) {
		return ""
	}
	return b.Description
}

// beadDetails is the edges of every bead, in one batched br call per project.
// A failed fetch rejects the project read; the Source keeps its last good rows.
func beadDetails(ctx context.Context, bc beads.Client, dir string, bs []beads.Bead) (map[string]beads.Detail, error) {
	ids := make([]string, len(bs))
	for i, b := range bs {
		ids[i] = b.ID
	}
	return bc.Details(ctx, dir, ids)
}

// parentOf is b's parent by its parent-child edge, else by its dotted id.
func parentOf(details map[string]beads.Detail, id string) string {
	if d, ok := details[id]; ok {
		return d.Parent
	}
	return beads.ParentOf(id)
}

// ownByChain keeps Owns only on rows beads.ChainOwners counts as owners, so a
// nested workstream under a closed or parked one owns nothing here either (the
// rule wsmgr routes and registers by).
func ownByChain(rows []ObjectiveRow, list []beads.Bead, details map[string]beads.Detail) {
	byID := make(map[string]beads.Bead, len(list))
	for _, b := range list {
		byID[b.ID] = b
	}
	for i := range rows {
		if !rows[i].Owns {
			continue
		}
		chain := []string{rows[i].BeadID}
		seen := map[string]bool{rows[i].BeadID: true}
		for p := parentOf(details, rows[i].BeadID); p != "" && !seen[p]; p = parentOf(details, p) {
			seen[p] = true
			chain = append(chain, p)
		}
		rows[i].Owns = slices.Contains(beads.ChainOwners(chain, byID), rows[i].BeadID)
	}
}

// supersededBead is a bead the plan dropped: labeled superseded or retired, or
// closed with a reason that says superseded. Closed is not approved for it.
func supersededBead(b beads.Bead) bool {
	return slices.Contains(b.Labels, beads.WithdrawnLabel) || beads.Superseded(b)
}

// closeReasonOf is a closed bead's close reason, from the list read already made
// (no extra br call), capped so a long report cannot swell every snapshot.
func closeReasonOf(b beads.Bead) string {
	if b.Status != "closed" {
		return ""
	}
	r := []rune(strings.TrimSpace(b.CloseReason))
	if len(r) > 400 {
		return string(r[:400]) + "…"
	}
	return string(r)
}

// FinishedTask is a finished Operator task that is no row of its own (its step closed
// too): its close reason is often the only record of what the step's work found.
type FinishedTask struct {
	BeadID  string `json:"id"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"`
}

// maxFoldedOutcomes and foldedOutcomeWindow bound what closed rows carry: the latest
// two finished tasks a step (by updated_at, which a close moves) closed in the last
// week, so the snapshot grows by a few lines for recently closed steps only.
const (
	maxFoldedOutcomes   = 2
	foldedOutcomeWindow = 7 * 24 * time.Hour
)

// attachFoldedOutcomes keeps, from the list already read (no extra call), what the
// closing of a step folds away and a reader still needs: the latest recent finished
// tasks with a close reason, on their nearest drawn ancestor, and which blockers of a row
// are finished beads that are not drawn (so nobody reads them as an outside wait).
func attachFoldedOutcomes(rows []ObjectiveRow, all []beads.Bead, details map[string]beads.Detail) {
	at := make(map[string]int, len(rows))
	for i, r := range rows {
		at[r.BeadID] = i
	}
	done := map[string]bool{}
	type folded struct {
		task    FinishedTask
		updated string
	}
	byRow := map[int][]folded{}
	for _, b := range all {
		if _, drawn := at[b.ID]; drawn {
			continue
		}
		if b.Status == "closed" || b.Status == beads.StatusDeferred || beads.Superseded(b) {
			done[b.ID] = true
		}
		reason := closeReasonOf(b)
		if reason == "" || beads.Superseded(b) || !slices.Contains(b.Labels, "kind:task") {
			continue
		}
		if at, err := time.Parse(time.RFC3339, b.UpdatedAt); err != nil || time.Since(at) > foldedOutcomeWindow {
			continue
		}
		seen := map[string]bool{b.ID: true}
		for p := parentOf(details, b.ID); p != "" && !seen[p]; p = parentOf(details, p) {
			seen[p] = true
			if i, ok := at[p]; ok {
				if rows[i].Status == "closed" {
					byRow[i] = append(byRow[i], folded{FinishedTask{BeadID: b.ID, Title: b.Title, Outcome: reason}, b.UpdatedAt})
				}
				break
			}
		}
	}
	for i, list := range byRow {
		slices.SortStableFunc(list, func(a, b folded) int { return strings.Compare(b.updated, a.updated) })
		for j := 0; j < len(list) && j < maxFoldedOutcomes; j++ {
			rows[i].FinishedTasks = append(rows[i].FinishedTasks, list[j].task)
		}
	}
	for i := range rows {
		for _, id := range rows[i].BlockedBy {
			if done[id] {
				rows[i].DoneBlockers = append(rows[i].DoneBlockers, id)
			}
		}
	}
}

func filterProject(projects []store.Project, name string) []store.Project {
	var only []store.Project
	for _, p := range projects {
		if p.Name == name {
			only = append(only, p)
		}
	}
	return only
}

// ProjectOfBead names the project whose name prefixes the bead id
// ("commander-il9.91" -> "commander"), the longest match; "" when none does.
func ProjectOfBead(projects []store.Project, beadID string) string {
	best := ""
	for _, p := range projects {
		if strings.HasPrefix(beadID, p.Name+"-") && len(p.Name) > len(best) {
			best = p.Name
		}
	}
	return best
}

// dropOrphans takes a shelved subtree off the board: a row goes when it is
// closed and its parent is not a row, or when any ancestor (by parent edge,
// then dotted id) is a closed objective. An open row whose parent was never
// listed stays, so unparented work is not lost.
func dropOrphans(rows []ObjectiveRow, closed map[string]bool) []ObjectiveRow {
	parent := make(map[string]string, len(rows))
	for _, r := range rows {
		parent[r.BeadID] = r.ParentBeadID
	}
	up := func(id string) string {
		if p, ok := parent[id]; ok {
			return p
		}
		if i := strings.LastIndex(id, "."); i > 0 {
			return id[:i]
		}
		return ""
	}
	shelved := func(id string) bool {
		for p := up(id); p != ""; p = up(p) {
			if closed[p] {
				return true
			}
		}
		return false
	}
	for {
		present := make(map[string]bool, len(rows))
		for _, r := range rows {
			present[r.BeadID] = true
		}
		kept := rows[:0:0]
		for _, r := range rows {
			orphan := r.ParentBeadID != "" && !present[r.ParentBeadID]
			if (orphan && r.Status == "closed") || shelved(r.BeadID) {
				continue
			}
			kept = append(kept, r)
		}
		if len(kept) == len(rows) {
			return kept
		}
		rows = kept
	}
}
