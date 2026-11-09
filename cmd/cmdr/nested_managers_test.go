package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func nestedTreeOn(t *testing.T, on bool) (*beads.MemClient, string) {
	t.Helper()
	tree := beads.NestedTree()
	dir := t.TempDir()
	if err := beads.SetNestedManagers(dir, on); err != nil {
		t.Fatal(err)
	}
	orig := newBeadsClientFn
	newBeadsClientFn = func() beads.Client { return tree }
	t.Cleanup(func() { newBeadsClientFn = orig })
	return tree, dir
}

// Route to the nearest owner, authorize the nearest or any owner above it.
func TestOwnerRoles_NearestOrAboveAuthorize(t *testing.T) {
	tree, dir := nestedTreeOn(t, true)
	roles, err := ownerRoles(context.Background(), tree, dir, beads.NestedStep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(roles, ",") != "manager-demo-a1,manager-demo-a,manager-demo-r" {
		t.Fatalf("roles %v", roles)
	}
	for role, ok := range map[string]bool{"manager-demo-a1": true, "manager-demo-a": true, "manager-demo-r": true, "manager-demo-b": false} {
		if roleAmong(role, roles) != ok {
			t.Errorf("%s: %v", role, !ok)
		}
	}
	_, off := nestedTreeOn(t, false)
	if roles, _ := ownerRoles(context.Background(), tree, off, beads.NestedStep); strings.Join(roles, ",") != "manager-demo-r" {
		t.Fatalf("switch off: %v", roles)
	}
}

// internal-gate publish: one table over the tree. A sibling's Manager is refused
// naming the nearest owner; the nearest and every owner above pass the authority
// check (what follows judges the card itself).
func TestGatePublish_NestedOwnerChainAuthorizes(t *testing.T) {
	_, dir := nestedTreeOn(t, true)
	for role, refused := range map[string]bool{
		"manager-demo-b":  true,
		"manager-demo-a1": false,
		"manager-demo-a":  false,
		"manager-demo-r":  false,
		"commander":       false,
	} {
		t.Setenv("COMMANDER_ROLE", role)
		err := gatePublish(context.Background(), dir, "demo-nogate", beads.NestedStep, true)
		got := err != nil && strings.Contains(err.Error(), "refused: only manager-demo-a1, Commander or the user")
		if got != refused {
			t.Errorf("%s: err %v, refused %v", role, err, refused)
		}
	}
}

func TestGateUnlink_OwnerRolesOfAGate(t *testing.T) {
	tree, dir := nestedTreeOn(t, true)
	tree.Seed(beads.Bead{ID: "demo-g", Labels: []string{"kind:objective", beads.GateLabel}})
	tree.Edges = append(tree.Edges, beads.Dep{IssueID: "demo-g", DependsOnID: beads.NestedA1, Type: "parent-child"})
	g, _ := tree.Show(context.Background(), dir, "demo-g")
	roles, err := gateOwnerRoles(context.Background(), tree, dir, *g)
	if err != nil || roles[0] != "manager-demo-a1" {
		t.Fatalf("roles %v %v", roles, err)
	}
	// A legacy parentless stage gate takes its stage's owners.
	tree.Seed(beads.Bead{ID: "demo-g2", Labels: []string{"kind:objective", beads.GateLabel, "gate-of:" + beads.EscapeLabel(beads.NestedBStep)}})
	g2, _ := tree.Show(context.Background(), dir, "demo-g2")
	if roles, err := gateOwnerRoles(context.Background(), tree, dir, *g2); err != nil || roles[0] != "manager-demo-b" {
		t.Fatalf("stage gate roles %v %v", roles, err)
	}
}

func TestWorkflowAddendum_OwnerBeadOf(t *testing.T) {
	owners := []string{"manager-demo-a1", "manager-demo-a", "manager-demo-r"}
	for actor, want := range map[string]string{"manager-demo-a1": "demo-a1", "manager-demo-r": "demo-r", "manager-demo-b": ""} {
		if got := ownerBeadOf(actor, owners); got != want {
			t.Errorf("%s: %q, want %q", actor, got, want)
		}
	}
}

func TestMoveCard_NestedOwner(t *testing.T) {
	tree, dir := nestedTreeOn(t, true)
	if got := beads.Owner(context.Background(), tree, dir, nil, nil, beads.NestedA1); got != beads.NestedA1 {
		t.Fatalf("a nested ancestor goes back under itself, got %s", got)
	}
}

func TestNestManagers_RefusesAgentsOtherThanCommander(t *testing.T) {
	for _, role := range []string{"manager-demo-r", "operator-op-x"} {
		t.Setenv("COMMANDER_ROLE", role)
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalNestManagers([]string{"--project", "x", "--apply"}, stdout, stderr); code == 0 {
			t.Fatalf("%s turned nested Managers on", role)
		}
		if _, e := read(); !strings.Contains(e, "only Commander or the user") {
			t.Fatalf("%s: %s", role, e)
		}
	}
}

func TestNestManagers_MentionsBeadWholeIDsOnly(t *testing.T) {
	for text, want := range map[string]bool{
		"look at demo-s now":   true,
		"demo-s.":              true,
		"(demo-s)":             true,
		"demo-s.1 is a child":  false,
		"demo-sx is different": false,
		"xdemo-s":              false,
	} {
		if got := mentionsBead(text, "demo-s"); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

// A nested Manager over the gate acts only on waiters in its own subtree: a
// waiter under a sibling nested workstream is that Manager's (or one above).
func TestGateUnlink_WaiterOutsideTheCallersSubtreeRefused(t *testing.T) {
	tree, dir := nestedTreeOn(t, true)
	home := t.TempDir()
	t.Setenv("COMMANDER_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.InsertProject(context.Background(), store.Project{Name: "demo", Type: "code", Path: dir, ActivatedAt: 1, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	tree.Seed(beads.Bead{ID: "demo-g", Labels: []string{"kind:objective", beads.GateLabel, "approver:user"}})
	tree.Seed(beads.Bead{ID: "demo-bx", Status: "closed", Labels: []string{"kind:objective", "superseded"}})
	tree.Edges = append(tree.Edges,
		beads.Dep{IssueID: "demo-g", DependsOnID: beads.NestedA1, Type: "parent-child"},
		beads.Dep{IssueID: "demo-bx", DependsOnID: beads.NestedB, Type: "parent-child"},
		beads.Dep{IssueID: "demo-bx", DependsOnID: "demo-g", Type: "blocks"})
	for role, refused := range map[string]bool{"manager-demo-a1": true, "manager-demo-b": true, "manager-demo-r": false} {
		t.Setenv("COMMANDER_ROLE", role)
		_, err := gateUnlink(context.Background(), st, "demo", "demo-g", []string{"demo-bx"}, "dropped plan")
		if got := err != nil && strings.Contains(err.Error(), "refused"); got != refused {
			t.Errorf("%s: err %v, want refused=%v", role, err, refused)
		}
	}
}

// A recorded addendum stays attributable after --undo or after its nested
// workstream closes: who may have owned the block is every workstream over it.
func TestWorkflowAddendum_RecordedOwnersSurviveUndoAndClose(t *testing.T) {
	tree, off := nestedTreeOn(t, false)
	ctx := context.Background()
	roles, err := recordedOwnerRoles(ctx, tree, off, beads.NestedStep, beads.NestedRoot)
	if err != nil || strings.Join(roles, ",") != "manager-demo-a1,manager-demo-a,manager-demo-r" {
		t.Fatalf("switch off: %v %v", roles, err)
	}
	roles, err = recordedOwnerRoles(ctx, tree, off, beads.NestedCStep, beads.NestedRoot)
	if err != nil || ownerBeadOf("manager-"+beads.NestedClosed, roles) != beads.NestedClosed {
		t.Fatalf("closed nested: %v %v", roles, err)
	}
	if ownerBeadOf("manager-"+beads.NestedB, roles) != "" {
		t.Fatalf("a sibling counted: %v", roles)
	}
}
