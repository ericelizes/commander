package tui

import "testing"

// TestFixtures_Projects_HasMixedTypes asserts the fixture fleet contains at
// least one code-type and one knowledge-type project, so view code paths for
// both branches get exercised.
func TestFixtures_Projects_HasMixedTypes(t *testing.T) {
	t.Parallel()

	projects := FixtureProjects()
	if len(projects) == 0 {
		t.Fatal("expected fixture projects, got none")
	}

	var sawCode, sawKnowledge bool
	for _, p := range projects {
		switch p.Type {
		case "code":
			sawCode = true
		case "knowledge":
			sawKnowledge = true
		default:
			t.Errorf("project %q has unexpected type %q", p.Name, p.Type)
		}
	}
	if !sawCode {
		t.Error("expected at least one code-type project")
	}
	if !sawKnowledge {
		t.Error("expected at least one knowledge-type project")
	}
}

// TestFixtures_Objectives_LinksToTasks asserts every TaskBeadID referenced
// by an objective resolves to a real FixtureTask. This is the contract that
// lets Lead view safely look up Tasks by bead-id.
func TestFixtures_Objectives_LinksToTasks(t *testing.T) {
	t.Parallel()

	tasks := FixtureTasks()
	taskByID := make(map[string]FixtureTask, len(tasks))
	for _, task := range tasks {
		taskByID[task.BeadID] = task
	}

	for _, obj := range FixtureObjectives() {
		if len(obj.TaskBeadIDs) == 0 {
			t.Errorf("objective %d (%q) has no task references", obj.ID, obj.Title)
			continue
		}
		for _, beadID := range obj.TaskBeadIDs {
			if _, ok := taskByID[beadID]; !ok {
				t.Errorf("objective %d references unknown task bead %q", obj.ID, beadID)
			}
		}
	}
}

// TestFixtures_Operators_AllHaveProjects asserts every operator's ProjectID
// resolves to a real fixture project.
func TestFixtures_Operators_AllHaveProjects(t *testing.T) {
	t.Parallel()

	projects := FixtureProjects()
	known := make(map[int64]struct{}, len(projects))
	for _, p := range projects {
		known[p.ID] = struct{}{}
	}

	for _, op := range FixtureOperators() {
		if _, ok := known[op.ProjectID]; !ok {
			t.Errorf("operator %q references unknown project id %d", op.AgentName, op.ProjectID)
		}
	}
}

// TestFixtures_Tasks_PhasesAreValid asserts every Task.Phase is in the
// canonical phase set so view code can render-switch over it exhaustively.
func TestFixtures_Tasks_PhasesAreValid(t *testing.T) {
	t.Parallel()

	for _, task := range FixtureTasks() {
		if !IsValidPhase(task.Phase) {
			t.Errorf("task %q has non-canonical phase %q", task.BeadID, task.Phase)
		}
	}
}

// TestFixtures_Metrics_AgreesWithCounts asserts the aggregate metrics match
// the operator slice on count-derivable fields (agent count, blocked count).
// Cost totals are fleet-level constants from the plan mockup and are not
// derived from per-operator usd_used (the plan's example numbers don't sum),
// so we only check costs are positive.
func TestFixtures_Metrics_AgreesWithCounts(t *testing.T) {
	t.Parallel()

	operators := FixtureOperators()
	metrics := FixtureMetrics()

	if metrics.AgentCount != len(operators) {
		t.Errorf("metrics.AgentCount = %d, want %d (len operators)", metrics.AgentCount, len(operators))
	}

	var blocked int
	var summedUSD float64
	for _, op := range operators {
		if op.Status == "blocked" {
			blocked++
		}
		summedUSD += op.USDUsed
	}
	if metrics.BlockedCount != blocked {
		t.Errorf("metrics.BlockedCount = %d, want %d", metrics.BlockedCount, blocked)
	}

	if metrics.TotalCostUSD24h <= 0 {
		t.Errorf("metrics.TotalCostUSD24h = %v, want > 0", metrics.TotalCostUSD24h)
	}
	if metrics.TotalCostUSD7d <= 0 {
		t.Errorf("metrics.TotalCostUSD7d = %v, want > 0", metrics.TotalCostUSD7d)
	}
	if metrics.TotalCostUSD7d < metrics.TotalCostUSD24h {
		t.Errorf("metrics.TotalCostUSD7d (%v) must be >= 24h (%v)", metrics.TotalCostUSD7d, metrics.TotalCostUSD24h)
	}
	if summedUSD <= 0 {
		t.Errorf("operators have zero total usd_used; expected positive sum for sanity")
	}

	if metrics.IdleRatio < 0 || metrics.IdleRatio > 1 {
		t.Errorf("metrics.IdleRatio = %v, want in [0,1]", metrics.IdleRatio)
	}
}
