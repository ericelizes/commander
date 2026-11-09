package tui

// Fixtures expose canned Go data used by U6-b/c/d view development and U6-h
// teatest snapshots. They mirror the shapes of store.Project / store.Objective
// / store.AgentRun / bd Task without importing those packages, so the tui
// package stays decoupled from store and so view code can be developed before
// U7/U8 swap fixtures for live data.
//
// Numbers come from the U6 plan's Commander view mockup: 3 projects (frontend
// code, survey knowledge, sandbox code), 6 operators across them, fleet
// aggregates 24h $1.85 / 7d $9.50 / idle ratio 38%. Aggregate numbers are
// declared directly (not summed) because the plan's mockup numbers don't
// arithmetically reconcile — fixture metrics are the contract for "what the
// rendered TUI should display," not derived state.

// FixtureProject mirrors store.Project. Type is "code" or "knowledge".
type FixtureProject struct {
	ID     int64
	Name   string
	Type   string
	Path   string
	Status string
}

// FixtureObjective mirrors store.Objective with its Task children flattened
// into a slice of bead IDs. The real store keeps the join in
// objective_tasks; fixtures inline it for view simplicity.
type FixtureObjective struct {
	ID          int64
	Title       string
	Status      string
	ProjectID   int64
	TaskBeadIDs []string
}

// FixtureTask is one row in the Lead view's task tree. Phase is the bd
// `cmdr:*` dimension label collapsed to a string. Derived flags Tasks that
// were spawned by another operator (sub-task in tree).
type FixtureTask struct {
	BeadID    string
	Title     string
	Phase     string
	ProjectID int64
	Derived   bool
}

// FixtureOperator mirrors store.AgentRun for the fleet panel. Status uses the
// view-layer vocabulary (working/blocked/needs-input/done/idle) rather than
// the storage vocabulary (running/killed-ceiling/closed/exited) so view code
// can switch on it without translation.
type FixtureOperator struct {
	BeadID       string
	AgentName    string
	SessionID    string
	Status       string
	ProjectID    int64
	USDUsed      float64
	TokensUsed   int
	WallclockMin int
}

// FixtureFleetMetrics is the aggregate rendered in Commander view's metrics
// card. The factory FixtureMetrics() returns one of these.
type FixtureFleetMetrics struct {
	TotalCostUSD24h float64
	TotalCostUSD7d  float64
	AgentCount      int
	ActiveCount     int
	BlockedCount    int
	IdleRatio       float64
}

// Canonical phase set — TestFixtures_Tasks_PhasesAreValid asserts every
// FixtureTask.Phase appears here, and views can render-switch over it.
var canonicalPhases = map[string]struct{}{
	"brainstorming": {},
	"planning":      {},
	"executing":     {},
	"reviewing":     {},
	"done":          {},
	"blocked":       {},
}

// IsValidPhase reports whether p is in the canonical phase set.
func IsValidPhase(p string) bool {
	_, ok := canonicalPhases[p]
	return ok
}

// Project IDs are stable across factories so cross-references hold.
const (
	fixtureProjectFrontend int64 = 1
	fixtureProjectSurvey   int64 = 2
	fixtureProjectSandbox  int64 = 3
)

// FixtureProjects returns the 3-project fleet: frontend (code), survey
// (knowledge), sandbox (code).
func FixtureProjects() []FixtureProject {
	return []FixtureProject{
		{
			ID:     fixtureProjectFrontend,
			Name:   "frontend",
			Type:   "code",
			Path:   "/code/frontend",
			Status: "active",
		},
		{
			ID:     fixtureProjectSurvey,
			Name:   "survey",
			Type:   "knowledge",
			Path:   "/knowledge/survey",
			Status: "active",
		},
		{
			ID:     fixtureProjectSandbox,
			Name:   "sandbox",
			Type:   "code",
			Path:   "/code/sandbox",
			Status: "active",
		},
	}
}

// FixtureObjectives returns 5 objectives spread across the 3 projects. Each
// references TaskBeadIDs that exist in FixtureTasks (test-enforced).
func FixtureObjectives() []FixtureObjective {
	return []FixtureObjective{
		{
			ID:          1,
			Title:       "Ship auth refactor",
			Status:      "open",
			ProjectID:   fixtureProjectFrontend,
			TaskBeadIDs: []string{"fro-001", "fro-002"},
		},
		{
			ID:          2,
			Title:       "Reduce p95 latency under 200ms",
			Status:      "open",
			ProjectID:   fixtureProjectFrontend,
			TaskBeadIDs: []string{"fro-003"},
		},
		{
			ID:          3,
			Title:       "Land Q3 roadmap doc",
			Status:      "open",
			ProjectID:   fixtureProjectSurvey,
			TaskBeadIDs: []string{"sur-001", "sur-002"},
		},
		{
			ID:          4,
			Title:       "Polish cover letter for Acme role",
			Status:      "open",
			ProjectID:   fixtureProjectSurvey,
			TaskBeadIDs: []string{"sur-003"},
		},
		{
			ID:          5,
			Title:       "Sandbox spike: bubbletea teatest patterns",
			Status:      "open",
			ProjectID:   fixtureProjectSandbox,
			TaskBeadIDs: []string{"sbx-001"},
		},
	}
}

// FixtureTasks returns 8 Tasks across all projects in mixed phases. BeadIDs
// match those referenced by FixtureObjectives.
func FixtureTasks() []FixtureTask {
	return []FixtureTask{
		{
			BeadID:    "fro-001",
			Title:     "Extract session middleware",
			Phase:     "executing",
			ProjectID: fixtureProjectFrontend,
		},
		{
			BeadID:    "fro-002",
			Title:     "Migrate token signing to ed25519",
			Phase:     "blocked",
			ProjectID: fixtureProjectFrontend,
		},
		{
			BeadID:    "fro-003",
			Title:     "Profile hot path in /search",
			Phase:     "planning",
			ProjectID: fixtureProjectFrontend,
		},
		{
			BeadID:    "fro-004",
			Title:     "Add regression test for token rotation",
			Phase:     "brainstorming",
			ProjectID: fixtureProjectFrontend,
			Derived:   true,
		},
		{
			BeadID:    "sur-001",
			Title:     "Outline Q3 themes",
			Phase:     "reviewing",
			ProjectID: fixtureProjectSurvey,
		},
		{
			BeadID:    "sur-002",
			Title:     "Collect peer feedback",
			Phase:     "done",
			ProjectID: fixtureProjectSurvey,
		},
		{
			BeadID:    "sur-003",
			Title:     "Draft Acme cover letter",
			Phase:     "executing",
			ProjectID: fixtureProjectSurvey,
		},
		{
			BeadID:    "sbx-001",
			Title:     "Reproduce teatest snapshot drift",
			Phase:     "brainstorming",
			ProjectID: fixtureProjectSandbox,
		},
	}
}

// FixtureOperators returns 6 operators matching the plan's mockup:
//   - frontend: 4 operators (1 blocked, 1 working, 2 idle)
//   - survey:   2 operators (1 working, 1 idle)
//   - sandbox:  0 operators
func FixtureOperators() []FixtureOperator {
	return []FixtureOperator{
		{
			BeadID:       "fro-001",
			AgentName:    "operator-extract-session",
			SessionID:    "ses_mer_001",
			Status:       "working",
			ProjectID:    fixtureProjectFrontend,
			USDUsed:      0.42,
			TokensUsed:   18_400,
			WallclockMin: 22,
		},
		{
			BeadID:       "fro-002",
			AgentName:    "operator-token-ed25519",
			SessionID:    "ses_mer_002",
			Status:       "blocked",
			ProjectID:    fixtureProjectFrontend,
			USDUsed:      0.31,
			TokensUsed:   12_900,
			WallclockMin: 14,
		},
		{
			BeadID:       "fro-003",
			AgentName:    "operator-search-profile",
			SessionID:    "ses_mer_003",
			Status:       "idle",
			ProjectID:    fixtureProjectFrontend,
			USDUsed:      0.27,
			TokensUsed:   11_200,
			WallclockMin: 41,
		},
		{
			BeadID:       "fro-004",
			AgentName:    "operator-token-regression",
			SessionID:    "ses_mer_004",
			Status:       "idle",
			ProjectID:    fixtureProjectFrontend,
			USDUsed:      0.20,
			TokensUsed:   8_700,
			WallclockMin: 33,
		},
		{
			BeadID:       "sur-003",
			AgentName:    "operator-acme-cover-letter",
			SessionID:    "ses_car_003",
			Status:       "working",
			ProjectID:    fixtureProjectSurvey,
			USDUsed:      0.55,
			TokensUsed:   24_100,
			WallclockMin: 18,
		},
		{
			BeadID:       "sur-001",
			AgentName:    "operator-q3-outline",
			SessionID:    "ses_car_001",
			Status:       "idle",
			ProjectID:    fixtureProjectSurvey,
			USDUsed:      0.25,
			TokensUsed:   10_400,
			WallclockMin: 47,
		},
	}
}

// FixtureMetrics returns aggregate fleet metrics matching the plan's
// Commander mockup: 3 projects, 6 operators, 1 blocked, idle ratio 38%,
// $1.85 24h, $9.50 7d.
//
// These numbers don't arithmetically derive from per-project costs in the
// mockup; we surface them verbatim because the rendered TUI must match the
// plan's visual contract, not the implied sums.
func FixtureMetrics() FixtureFleetMetrics {
	return FixtureFleetMetrics{
		TotalCostUSD24h: 1.85,
		TotalCostUSD7d:  9.50,
		AgentCount:      6,
		ActiveCount:     2,
		BlockedCount:    1,
		IdleRatio:       0.38,
	}
}
