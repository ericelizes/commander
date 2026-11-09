package spawn

import (
	"context"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

type fakeRuns []store.AgentRun

func (f fakeRuns) ListRunningRuns(context.Context) ([]store.AgentRun, error) { return f, nil }

// capBeads: workstreams ws-a (steps ws-a.1, ws-a.2) and ws-b; ws-x.1 was moved
// under ws-a by a parent-child edge, so it counts for ws-a, not ws-x.
func capBeads(wsALabels ...string) *beads.MemClient {
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "ws-a", Labels: wsALabels})
	m.Seed(beads.Bead{ID: "ws-a.1"})
	m.Seed(beads.Bead{ID: "ws-a.1.1"})
	m.Seed(beads.Bead{ID: "ws-a.1.2"})
	m.Seed(beads.Bead{ID: "ws-a.2"})
	m.Seed(beads.Bead{ID: "ws-a.2.1"})
	m.Seed(beads.Bead{ID: "ws-b"})
	m.Seed(beads.Bead{ID: "ws-b.1"})
	m.Seed(beads.Bead{ID: "ws-b.1.1"})
	m.Seed(beads.Bead{ID: "ws-x"})
	m.Seed(beads.Bead{ID: "ws-x.1"})
	m.Edges = append(m.Edges, beads.Dep{IssueID: "ws-x.1", DependsOnID: "ws-a.2", Type: "parent-child"})
	return m
}

func runsOn(project int64, tasks ...string) fakeRuns {
	var out fakeRuns
	for _, t := range tasks {
		out = append(out, store.AgentRun{ProjectID: project, TaskBeadID: t, AgentName: "operator-" + t, Status: "running"})
	}
	return out
}

func TestWorkstreamCapDefaultEnforced(t *testing.T) {
	ctx := context.Background()
	runs := runsOn(1, "ws-a.1.1", "ws-a.2.1", "ws-b.1.1")
	if err := CheckWorkstreamCap(ctx, runs, capBeads(), 1, "/p", "ws-a.1", 3); err != nil {
		t.Fatalf("2 of 3 running in ws-a should leave room: %v", err)
	}
	err := CheckWorkstreamCap(ctx, runs, capBeads(), 1, "/p", "ws-a.1", 2)
	if err == nil || !strings.Contains(err.Error(), "ws-a already has 2 running operators (max 2): operator-ws-a.1.1, operator-ws-a.2.1;") {
		t.Fatalf("want a cap refusal for ws-a, got %v", err)
	}
	// ws-b runs one: a full ws-a does not hold it back.
	if err := CheckWorkstreamCap(ctx, runs, capBeads(), 1, "/p", "ws-b.1", 2); err != nil {
		t.Fatalf("ws-b is under its cap: %v", err)
	}
}

func TestWorkstreamCapCountsByRealParentAndProject(t *testing.T) {
	ctx := context.Background()
	// ws-x.1 lives under ws-a; the same ids in project 2 are another project's.
	runs := append(runsOn(1, "ws-a.1.1", "ws-x.1"), runsOn(2, "ws-a.1.2", "ws-a.2.1")...)
	err := CheckWorkstreamCap(ctx, runs, capBeads(), 1, "/p", "ws-a.2", 2)
	if err == nil || !strings.Contains(err.Error(), "has 2 running") {
		t.Fatalf("want 2 counted in ws-a (moved bead included, other project excluded), got %v", err)
	}
}

func TestWorkstreamCapLabelOverrides(t *testing.T) {
	ctx := context.Background()
	runs := runsOn(1, "ws-a.1.1", "ws-a.1.2", "ws-a.2.1")
	if err := CheckWorkstreamCap(ctx, runs, capBeads(beads.MaxOperatorsLabelPrefix+"4"), 1, "/p", "ws-a.1", 2); err != nil {
		t.Fatalf("max-operators:4 should raise the default 2: %v", err)
	}
	err := CheckWorkstreamCap(ctx, runs, capBeads(beads.MaxOperatorsLabelPrefix+"1"), 1, "/p", "ws-a.1", 8)
	if err == nil || !strings.Contains(err.Error(), "(max 1)") {
		t.Fatalf("max-operators:1 should lower the default 8, got %v", err)
	}
}

func TestWorkstreamCapZeroMeansNone(t *testing.T) {
	runs := runsOn(1, "ws-a.1.1", "ws-a.1.2")
	if err := CheckWorkstreamCap(context.Background(), runs, capBeads(), 1, "/p", "ws-a.1", 0); err != nil {
		t.Fatalf("no default and no label means no cap: %v", err)
	}
}

func TestWorkstreamCapIgnoresBadLabel(t *testing.T) {
	if got := WorkstreamCap([]string{"max-operators:zero", "max-operators:-2"}, 8); got != 8 {
		t.Fatalf("WorkstreamCap = %d, want the default 8", got)
	}
}

func nestedCapDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := beads.SetNestedManagers(dir, true); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A nested owner has its own cap: the default counts only the Operators it owns,
// so Operators under a sibling do not fill it (the root's label raised above the
// default, which otherwise bounds the whole tree).
func TestWorkstreamCap_NestedOwnCap(t *testing.T) {
	ctx := context.Background()
	dir := nestedCapDir(t)
	runs := runsOn(1, beads.NestedBStep, beads.NestedBStep+"-2")
	tree := beads.NestedTree()
	if err := tree.SetLabel(ctx, dir, beads.NestedRoot, beads.MaxOperatorsLabelPrefix+"10", true); err != nil {
		t.Fatal(err)
	}
	tree.Seed(beads.Bead{ID: beads.NestedBStep + "-2"})
	tree.Edges = append(tree.Edges, beads.Dep{IssueID: beads.NestedBStep + "-2", DependsOnID: beads.NestedB, Type: "parent-child"})
	if err := CheckWorkstreamCap(ctx, runs, tree, 1, dir, beads.NestedStep, 2); err != nil {
		t.Fatalf("B's Operators filled A1's cap: %v", err)
	}
	if err := CheckWorkstreamCap(ctx, runs, tree, 1, dir, beads.NestedBStep, 2); err == nil || !strings.Contains(err.Error(), "workstream "+beads.NestedB+" already has 2") {
		t.Fatalf("want B's own cap, got %v", err)
	}
}

// A max-operators label on a parent caps the sum of its subtree.
func TestWorkstreamCap_ParentCapBoundsSubtree(t *testing.T) {
	ctx := context.Background()
	dir := nestedCapDir(t)
	tree := beads.NestedTree()
	if err := tree.SetLabel(ctx, dir, beads.NestedRoot, beads.MaxOperatorsLabelPrefix+"2", true); err != nil {
		t.Fatal(err)
	}
	runs := runsOn(1, beads.NestedStep, beads.NestedBStep)
	err := CheckWorkstreamCap(ctx, runs, tree, 1, dir, beads.NestedStep, 8)
	if err == nil || !strings.Contains(err.Error(), "workstream "+beads.NestedRoot+" already has 2 running operators (max 2)") {
		t.Fatalf("want the root's subtree cap, got %v", err)
	}
}

// Turning nesting on never multiplies a tree's bound: with no label the default
// caps the root's whole subtree, as with the switch off.
func TestWorkstreamCap_DefaultBoundsTheRootsSubtree(t *testing.T) {
	ctx := context.Background()
	runs := runsOn(1, beads.NestedStep, beads.NestedBStep)
	for _, on := range []bool{false, true} {
		dir := t.TempDir()
		if err := beads.SetNestedManagers(dir, on); err != nil {
			t.Fatal(err)
		}
		err := CheckWorkstreamCap(ctx, runs, beads.NestedTree(), 1, dir, beads.NestedP1Step, 2)
		if err == nil || !strings.Contains(err.Error(), "workstream "+beads.NestedRoot+" already has 2 running operators (max 2)") {
			t.Fatalf("on=%v: want the root's default over its subtree, got %v", on, err)
		}
	}
}

// A root's label bounds its subtree for a spawn on the root's own step too, not
// only for one under a nested owner.
func TestWorkstreamCap_RootCapHoldsForItsOwnSteps(t *testing.T) {
	ctx := context.Background()
	dir := nestedCapDir(t)
	tree := beads.NestedTree()
	if err := tree.SetLabel(ctx, dir, beads.NestedRoot, beads.MaxOperatorsLabelPrefix+"3", true); err != nil {
		t.Fatal(err)
	}
	runs := runsOn(1, beads.NestedStep, beads.NestedA1, beads.NestedBStep)
	err := CheckWorkstreamCap(ctx, runs, tree, 1, dir, beads.NestedTStep, 8)
	if err == nil || !strings.Contains(err.Error(), "workstream "+beads.NestedRoot+" already has 3 running operators (max 3)") {
		t.Fatalf("a spawn on the root's own step passed its cap: %v", err)
	}
}

func TestManagerRole_NestedTask(t *testing.T) {
	ctx := context.Background()
	tree := beads.NestedTree()
	if got := ManagerRole(ctx, tree, t.TempDir(), beads.NestedStep); got != "manager-"+beads.NestedRoot {
		t.Fatalf("switch off: %s, want the root's Manager", got)
	}
	if got := ManagerRole(ctx, tree, nestedCapDir(t), beads.NestedStep); got != "manager-"+beads.NestedA1 {
		t.Fatalf("switch on: %s, want the nested Manager", got)
	}
}
