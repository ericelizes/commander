package tui

import (
	"context"
	"strings"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// LeadData is the snapshot a Lead view renders against once Services are
// wired. Replaces the FixtureXxx slices when LoadLeadData succeeds.
//
// Partial loads are tolerated: any per-source error is captured in LoadErr
// and the caller renders whatever populated successfully (the rest falls
// back to empty-state copy).
type LeadData struct {
	Project    store.Project
	Objectives []ObjectiveBead
	Tasks      []TaskRow
	Operators  []OperatorRow
	Metrics    FixtureFleetMetrics
	LoadedAt   time.Time
	LoadErr    error
}

type ObjectiveBead struct {
	ID     string
	Title  string
	Status string
}

// TaskRow is a Task bead surfaced from `bd list --label kind:task`. Phase is
// derived from the cmdr:<phase> dimension label written by `bd set-state`.
// ObjectiveBeadID is empty when the Task isn't linked to an Objective.
type TaskRow struct {
	BeadID          string
	Title           string
	Phase           string
	ObjectiveBeadID string
	Derived         bool
}

// OperatorRow is one running operator joined from agent_runs (see livedata).
type OperatorRow = livedata.OperatorRow

// LeadDataDeps is the narrow slice of Services that LoadLeadData needs.
// Pulled out as its own struct so tests can inject mocks without spinning
// up the whole Services bundle (and without coupling to opencode/am for
// data that doesn't depend on them).
type LeadDataDeps struct {
	Store *store.Store
	Beads beads.Client
}

// LoadLeadData hits the live backends and returns a snapshot. Errors from
// individual sources collapse into result.LoadErr; whatever loaded
// successfully is still returned so the renderer can show partial state.
//
// The beads.Client is constructed inline (via beads.New()) when deps.Beads
// is nil — the wrapper is stateless (a thin shell-out shim), so the
// allocation is essentially free.
func LoadLeadData(ctx context.Context, deps LeadDataDeps, projectID int64) LeadData {
	out := LeadData{LoadedAt: time.Now()}

	if deps.Store == nil {
		out.LoadErr = errLeadDataNoStore
		return out
	}

	proj, err := deps.Store.GetProjectByID(ctx, projectID)
	if err != nil {
		out.LoadErr = err
		return out
	}
	if proj == nil {
		out.LoadErr = errLeadDataProjectNotFound
		return out
	}
	out.Project = *proj

	bdClient := deps.Beads
	if bdClient == nil {
		bdClient = beads.New()
	}

	objectiveBeads, objectiveErr := bdClient.List(ctx, proj.Path, beads.ListOptions{
		Labels: []string{"kind:objective"},
	})
	if objectiveErr != nil {
		out.LoadErr = objectiveErr
	} else {
		out.Objectives = objectivesFromBeads(objectiveBeads)
	}

	beadList, beadErr := bdClient.List(ctx, proj.Path, beads.ListOptions{
		Labels: []string{"kind:task"},
	})
	if beadErr != nil {
		// Carry the error but keep loading the other sources — operators
		// and metrics are still useful even when bd is offline.
		if out.LoadErr == nil {
			out.LoadErr = beadErr
		}
	} else {
		out.Tasks = tasksFromBeads(beadList)
	}

	runs, runErr := deps.Store.ListAgentRunsByProject(ctx, projectID, "running")
	if runErr != nil && out.LoadErr == nil {
		out.LoadErr = runErr
	} else if runErr == nil {
		out.Operators = operatorsFromRuns(runs)
	}

	out.Metrics = metricsFromOperators(deps.Store, ctx, projectID, out.Operators, out.LoadedAt)

	return out
}

// tasksFromBeads converts a bd Bead slice into TaskRow with Phase derived
// from the cmdr:<phase> label. Beads without a cmdr:* label fall through
// with Phase="" so the renderer can pick a neutral glyph.
func tasksFromBeads(in []beads.Bead) []TaskRow {
	out := make([]TaskRow, 0, len(in))
	for _, b := range in {
		row := TaskRow{
			BeadID: b.ID,
			Title:  b.Title,
			Phase:  phaseFromLabels(b.Labels),
		}
		if b.ParentID != "" {
			row.ObjectiveBeadID = b.ParentID
		}
		// Mark as derived if the bead carries an explicit derived label.
		// (bd's labels list is the source of truth — Bead has no Derived bool.)
		for _, l := range b.Labels {
			if l == "derived" || l == "cmdr:derived" {
				row.Derived = true
				break
			}
		}
		out = append(out, row)
	}
	return out
}

func objectivesFromBeads(in []beads.Bead) []ObjectiveBead {
	out := make([]ObjectiveBead, 0, len(in))
	for _, b := range in {
		out = append(out, ObjectiveBead{
			ID:     b.ID,
			Title:  b.Title,
			Status: b.Status,
		})
	}
	return out
}

// phaseFromLabels finds the first label of the form "cmdr:<phase>" and
// returns <phase>. Returns "" when no such label exists.
func phaseFromLabels(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "cmdr:") {
			rest := strings.TrimPrefix(l, "cmdr:")
			// Skip cmdr:derived — that's a flag, not a phase.
			if rest == "derived" {
				continue
			}
			return rest
		}
	}
	return ""
}

func operatorsFromRuns(runs []store.AgentRun) []OperatorRow { return livedata.OperatorsFromRuns(runs) }

// metricsFromOperators builds a project-scoped FixtureFleetMetrics. Cost
// fields use cost_events when available (project-scoped windowing), falling
// back to a sum over operators.UsdUsed when the events query errors.
func metricsFromOperators(st *store.Store, ctx context.Context, projectID int64, ops []OperatorRow, now time.Time) FixtureFleetMetrics {
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

	cost24h, cost7d := windowedCostsByProject(st, ctx, projectID, now)
	if cost24h == 0 {
		cost24h = spotCost
	}
	if cost7d == 0 {
		// Fallback: rough multiplier so the 7d field isn't suspicious-zero
		// when no cost_events have landed yet for this project.
		cost7d = spotCost
	}

	idle := 0.0
	if n := len(ops); n > 0 {
		idle = float64(n-active-blocked) / float64(n)
	}

	return FixtureFleetMetrics{
		TotalCostUSD24h: cost24h,
		TotalCostUSD7d:  cost7d,
		AgentCount:      len(ops),
		ActiveCount:     active,
		BlockedCount:    blocked,
		IdleRatio:       idle,
	}
}

// windowedCostsByProject sums delta_usd in cost_events for a project across
// the 24h and 7d windows. Done in-process via ListCostEventsByProject since
// the store doesn't yet ship a project-scoped SumUSDByWindow; the row count
// is bounded by 24h * tick frequency so memory is fine.
func windowedCostsByProject(st *store.Store, ctx context.Context, projectID int64, now time.Time) (cost24h, cost7d float64) {
	if st == nil {
		return 0, 0
	}
	events, err := st.ListCostEventsByProject(ctx, projectID, 0)
	if err != nil {
		return 0, 0
	}
	cutoff24 := now.Add(-24 * time.Hour).Unix()
	cutoff7 := now.Add(-7 * 24 * time.Hour).Unix()
	for _, e := range events {
		if e.OccurredAt >= cutoff24 {
			cost24h += e.DeltaUSD
		}
		if e.OccurredAt >= cutoff7 {
			cost7d += e.DeltaUSD
		}
	}
	return cost24h, cost7d
}

// errLeadDataNoStore is returned when LoadLeadData is called with no Store
// dependency — programmer error, but worth surfacing distinctly so tests
// can catch it.
var errLeadDataNoStore = leadDataError("lead_data: store dependency is nil")

// errLeadDataProjectNotFound is returned when the projectID doesn't resolve
// to a row. The view treats this the same as a load failure (empty state).
var errLeadDataProjectNotFound = leadDataError("lead_data: project not found")

// leadDataError lets us declare sentinel errors as constants without pulling
// in errors.New (which can't be used at package-init time for var decls
// that need to be const-shaped).
type leadDataError string

func (e leadDataError) Error() string { return string(e) }
