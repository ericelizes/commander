package wsmgr

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/mailstate"
	"github.com/example-user/commander/internal/tui"
)

// nestedFixture serves beads.NestedTree in a project whose switch is on (or off).
func nestedFixture(t *testing.T, on bool) (*fixture, *beads.MemClient) {
	t.Helper()
	f := newFixture(t)
	bc := beads.NestedTree()
	f.m.Beads = bc
	if err := beads.SetNestedManagers(f.proj.Path, on); err != nil {
		t.Fatal(err)
	}
	f.m.Numbered = func(context.Context) ([]tui.NumberedWorkstream, error) {
		return []tui.NumberedWorkstream{{N: 1, Project: f.proj.Name, BeadID: beads.NestedRoot, Title: "Root"}}, nil
	}
	f.m.Fresh = f.m.Numbered
	return f, bc
}

func TestStrictOwner_NearestOpenWorkstream(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	for id, want := range map[string]string{
		beads.NestedStep: beads.NestedA1, beads.NestedA1: beads.NestedA1, beads.NestedBStep: beads.NestedB,
		beads.NestedTStep: beads.NestedRoot, beads.NestedCStep: beads.NestedRoot, beads.NestedPStep: beads.NestedRoot,
		beads.NestedP1: beads.NestedRoot, beads.NestedP1Step: beads.NestedRoot,
	} {
		got, err := StrictOwner(ctx, bc, f.proj.Path, id)
		if err != nil || got != want {
			t.Errorf("StrictOwner(%s) = %s, %v; want %s", id, got, err, want)
		}
	}
	chain, err := OwnerChain(ctx, bc, f.proj.Path, beads.NestedStep)
	if err != nil || strings.Join(chain, ",") != strings.Join([]string{beads.NestedA1, beads.NestedA, beads.NestedRoot}, ",") {
		t.Fatalf("OwnerChain = %v, %v", chain, err)
	}
}

type failingList struct{ *beads.MemClient }

func (failingList) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	return nil, errors.New("busy")
}

// shortList answers a list with nothing, as a lagging export can.
type shortList struct{ *beads.MemClient }

func (shortList) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	return nil, nil
}

func TestStrictOwner_FailedOrShortLabelReadIsAnError(t *testing.T) {
	f, bc := nestedFixture(t, true)
	for _, c := range []beads.Client{failingList{bc}, shortList{bc}} {
		if got, err := StrictOwner(context.Background(), c, f.proj.Path, beads.NestedStep); err == nil {
			t.Fatalf("owner %s from an unreadable chain", got)
		}
	}
}

func TestStrictOwner_SwitchOffIsRoot(t *testing.T) {
	f, bc := nestedFixture(t, false)
	got, err := StrictOwner(context.Background(), failingList{bc}, f.proj.Path, beads.NestedStep)
	if err != nil || got != beads.NestedRoot {
		t.Fatalf("switch off: %s, %v (and no label read)", got, err)
	}
}

func TestValidateTarget_NestedWorkstreamIsAValidTarget(t *testing.T) {
	f, _ := nestedFixture(t, true)
	ctx := context.Background()
	w, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: beads.NestedA1, Title: "Nested A1"})
	if err != nil || w.AgentName != Role(beads.NestedA1) {
		t.Fatalf("prepare nested: %+v %v", w, err)
	}
	if row, _ := f.st.GetWorkstreamManager(ctx, f.proj.ID, beads.NestedA1); row == nil {
		t.Fatal("nested Manager not registered")
	}
	if !mailstate.HasCursor(Role(beads.NestedA1)) {
		t.Fatal("a nested Manager is registered with no mail cursor: its first mail would read as history")
	}
	if mailstate.HasCursor(Role(beads.NestedRoot)) {
		t.Fatal("cursor written for a role never prepared")
	}
}

func TestValidateTarget_StepUnderNestedIsRefusedNamingItsOwner(t *testing.T) {
	f, _ := nestedFixture(t, true)
	_, err := f.m.Prepare(context.Background(), Target{Project: f.proj, BeadID: beads.NestedStep})
	if !errors.Is(err, ErrInvalidRoute) || !strings.Contains(err.Error(), Role(beads.NestedA1)) {
		t.Fatalf("err = %v, want invalid route naming %s", err, Role(beads.NestedA1))
	}
}

func TestValidateTarget_TrackOnlyRefused(t *testing.T) {
	f, _ := nestedFixture(t, true)
	for _, id := range []string{beads.NestedTrack, beads.NestedClosed, beads.NestedParked} {
		if _, err := f.m.Prepare(context.Background(), Target{Project: f.proj, BeadID: id}); !errors.Is(err, ErrInvalidRoute) {
			t.Errorf("%s: err = %v, want invalid route", id, err)
		}
	}
}

func TestValidateTarget_NestedRefusedWhileSwitchOff(t *testing.T) {
	f, _ := nestedFixture(t, false)
	if _, err := f.m.Prepare(context.Background(), Target{Project: f.proj, BeadID: beads.NestedA}); !errors.Is(err, ErrInvalidRoute) || !strings.Contains(err.Error(), Role(beads.NestedRoot)) {
		t.Fatalf("switch off: err = %v, want the root's Manager", err)
	}
}

func TestOwners_ListsNestedOnlyWhenSwitchedOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		f, _ := nestedFixture(t, on)
		got, err := f.m.Owners(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		parent := map[string]string{}
		for _, tg := range got {
			ids = append(ids, tg.BeadID)
			parent[tg.BeadID] = tg.Parent
		}
		want := beads.NestedRoot
		if on {
			// Parents first; closed, parked and legacy tracks own nothing.
			want = strings.Join([]string{beads.NestedRoot, beads.NestedA, beads.NestedB, beads.NestedA1}, ",")
		}
		if strings.Join(ids, ",") != want {
			t.Fatalf("on=%v: owners %v, want %s", on, ids, want)
		}
		if on && (parent[beads.NestedA1] != beads.NestedA || parent[beads.NestedA] != beads.NestedRoot) {
			t.Fatalf("parents %v", parent)
		}
	}
}

func TestEnsureNative_StartsNestedFresh(t *testing.T) {
	f, _ := nestedFixture(t, true)
	n := &fakeNative{}
	f.m.Native = n
	if err := f.m.EnsureNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{beads.NestedRoot, beads.NestedB, beads.NestedA1} {
		if !n.started[Role(id)] {
			t.Errorf("%s not started: %v", Role(id), n.started)
		}
	}
}

func TestEnsureNative_SkipsSingleChildParent(t *testing.T) {
	f, _ := nestedFixture(t, true)
	n := &fakeNative{}
	f.m.Native = n
	if err := f.m.EnsureNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A has exactly one open nested child (A1), which stands in its place.
	if n.started[Role(beads.NestedA)] {
		t.Fatal("a parent with one nested child was started on its own")
	}
	// Mail still starts it.
	if err := f.m.EnsureRole(context.Background(), Role(beads.NestedA)); err != nil || !n.started[Role(beads.NestedA)] {
		t.Fatalf("mail did not start the parent: %v", err)
	}
}

func TestEnsureNative_SwitchOffStartsRootsOnly(t *testing.T) {
	f, _ := nestedFixture(t, false)
	n := &fakeNative{}
	f.m.Native = n
	if err := f.m.EnsureNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.started) != 1 || !n.started[Role(beads.NestedRoot)] {
		t.Fatalf("started %v, want the root only", n.started)
	}
}

func TestEnsureRole_NestedRegistered(t *testing.T) {
	f, _ := nestedFixture(t, true)
	ctx := context.Background()
	if _, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: beads.NestedB, Title: "Nested B"}); err != nil {
		t.Fatal(err)
	}
	n := &fakeNative{}
	f.m.Native = n
	if err := f.m.EnsureRole(ctx, Role(beads.NestedB)); err != nil || !n.started[Role(beads.NestedB)] {
		t.Fatalf("nested role not woken: %v %v", err, n.started)
	}
}

func TestEnsureRole_NestedClosedIsInvalidRoute(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	if _, err := f.m.Prepare(ctx, Target{Project: f.proj, BeadID: beads.NestedB, Title: "Nested B"}); err != nil {
		t.Fatal(err)
	}
	if err := bc.SetStatus(ctx, f.proj.Path, beads.NestedB, "closed"); err != nil {
		t.Fatal(err)
	}
	f.m.Native = &fakeNative{}
	if err := f.m.EnsureRole(ctx, Role(beads.NestedB)); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("closed nested woken: %v", err)
	}
}

func TestOpen_StepUnderNestedOpensNestedManager(t *testing.T) {
	f, _ := nestedFixture(t, true)
	n := &fakeNative{}
	f.m.Native = n
	res, err := f.m.Open(context.Background(), f.proj.Name, beads.NestedStep, OpenOptions{StepID: beads.NestedStep})
	if err != nil {
		t.Fatal(err)
	}
	if res.BeadID != beads.NestedA1 || res.Role != Role(beads.NestedA1) || !res.Native {
		t.Fatalf("opened %+v, want the nested Manager of %s", res, beads.NestedA1)
	}
	if len(n.sent) != 1 || !strings.HasPrefix(n.sent[0], Role(beads.NestedA1)+": ") || !strings.Contains(n.sent[0], beads.NestedStep) {
		t.Fatalf("pointer %v", n.sent)
	}
}

func TestPreamble_NestedNamesParentAndChildren(t *testing.T) {
	pre := Preamble(Target{BeadID: beads.NestedA, Title: "Nested A", Parent: beads.NestedRoot, Children: []string{beads.NestedA1}})
	for _, want := range []string{"NEAREST workstream", "Your parent is " + Role(beads.NestedRoot), Role(beads.NestedA1), "cmdr board --role " + Role(beads.NestedA)} {
		if !strings.Contains(pre, want) {
			t.Errorf("preamble lacks %q:\n%s", want, pre)
		}
	}
	if top := Preamble(Target{BeadID: beads.NestedRoot}); strings.Contains(top, "Nested Managers") {
		t.Fatal("a top-level Manager with no nested children got the nested block")
	}
}

func TestOwnerOfRole_NestedRoutes(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	for role, want := range map[string]string{
		Role(beads.NestedA1):    Role(beads.NestedA1),   // an owner keeps its mail
		Role(beads.NestedStep):  Role(beads.NestedA1),   // a step goes to its nearest owner
		Role(beads.NestedCStep): Role(beads.NestedRoot), // under a closed nested: next open owner up
		Role(beads.NestedTStep): Role(beads.NestedRoot), // legacy track stays with its workstream
	} {
		got, why, err := f.m.OwnerOfRole(ctx, role)
		if err != nil || got != want {
			t.Errorf("OwnerOfRole(%s) = %q (%s), %v; want %s", role, got, why, err, want)
		}
	}
	// A nested workstream that closes routes up, not straight to Commander.
	if err := bc.SetStatus(ctx, f.proj.Path, beads.NestedA1, "closed"); err != nil {
		t.Fatal(err)
	}
	if got, _, err := f.m.OwnerOfRole(ctx, Role(beads.NestedA1)); err != nil || got != Role(beads.NestedA) {
		t.Fatalf("closed nested routed to %q, %v; want %s", got, err, Role(beads.NestedA))
	}
}

func TestRoutedMail_NestedStepReachesItsNestedManager(t *testing.T) {
	f, _ := nestedFixture(t, true)
	var to []string
	send := f.m.RoutedMail(func(_ context.Context, role, _, _ string) error { to = append(to, role); return nil })
	if err := send(context.Background(), Role(beads.NestedStep), "[wait] event", "b"); err != nil {
		t.Fatal(err)
	}
	if len(to) != 1 || to[0] != Role(beads.NestedA1) {
		t.Fatalf("sent to %v", to)
	}
}

// An open nested workstream under a parked one is out of play with it: owned
// by the root, never listed, never startable, and mail to it routes up.
func TestNestedUnderParked_OneRuleForRoutingAndRegistration(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	owners, err := f.m.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := NestedOwners(ctx, bc, f.proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range [][]Target{owners, nested} {
		for _, tg := range ts {
			if tg.BeadID == beads.NestedP1 {
				t.Fatalf("listed an owner under a parked workstream: %v", ts)
			}
		}
	}
	if got, err := StrictOwner(ctx, bc, f.proj.Path, beads.NestedP1Step); err != nil || got != beads.NestedRoot {
		t.Fatalf("StrictOwner = %s, %v", got, err)
	}
	if got, _, err := f.m.OwnerOfRole(ctx, Role(beads.NestedP1)); err != nil || got != Role(beads.NestedRoot) {
		t.Fatalf("mail to the out-of-play role routed to %q, %v", got, err)
	}
	// Unparked, it owns its steps and is listed: still one rule.
	if err := bc.SetStatus(ctx, f.proj.Path, beads.NestedParked, "open"); err != nil {
		t.Fatal(err)
	}
	if got, err := StrictOwner(ctx, bc, f.proj.Path, beads.NestedP1Step); err != nil || got != beads.NestedP1 {
		t.Fatalf("unparked: StrictOwner = %s, %v", got, err)
	}
	nested, _ = NestedOwners(ctx, bc, f.proj)
	if !slices.ContainsFunc(nested, func(tg Target) bool { return tg.BeadID == beads.NestedP1 }) {
		t.Fatalf("unparked: %v", nested)
	}
}

// A nested workstream under a plain step owns its work (beads.Owner says so),
// so the registration lists carry it too.
func TestNestedUnderAPlainStep_IsListed(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	bc.Seed(beads.Bead{ID: "demo-x", Title: "Nested under a step", Labels: []string{"kind:objective", beads.WorkstreamLabel}})
	bc.Edges = append(bc.Edges, beads.Dep{IssueID: "demo-x", DependsOnID: beads.NestedBStep, Type: "parent-child"})
	if got, err := StrictOwner(ctx, bc, f.proj.Path, "demo-x"); err != nil || got != "demo-x" {
		t.Fatalf("StrictOwner = %s, %v", got, err)
	}
	owners, err := f.m.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nested, err := NestedOwners(ctx, bc, f.proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range [][]Target{owners, nested} {
		if !slices.ContainsFunc(ts, func(tg Target) bool { return tg.BeadID == "demo-x" && tg.Parent == beads.NestedB }) {
			t.Fatalf("missing demo-x: %v", ts)
		}
	}
}

// reversedList answers lists in reverse order, as another backend may.
type reversedList struct{ *beads.MemClient }

func (r reversedList) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	l, err := r.MemClient.List(ctx, dir, o)
	slices.Reverse(l)
	return l, err
}

func TestNestedOwners_OrderDoesNotDependOnTheList(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ids := func(ts []Target) string {
		var s []string
		for _, tg := range ts {
			s = append(s, tg.BeadID)
		}
		return strings.Join(s, ",")
	}
	want := strings.Join([]string{beads.NestedA, beads.NestedB, beads.NestedA1}, ",")
	for _, c := range []beads.Client{bc, reversedList{bc}} {
		got, err := NestedOwners(context.Background(), c, f.proj)
		if err != nil || ids(got) != want {
			t.Fatalf("NestedOwners = %s, %v; want %s", ids(got), err, want)
		}
		f.m.Beads = c
		all, err := f.m.Owners(context.Background())
		if err != nil || ids(all) != beads.NestedRoot+","+want {
			t.Fatalf("Owners = %s, %v", ids(all), err)
		}
	}
}

// Under a closed step the board folds a nested workstream away; it owns nothing
// for actions either, and is never listed.
func TestNestedUnderAClosedStep_IsOutOfPlay(t *testing.T) {
	f, bc := nestedFixture(t, true)
	ctx := context.Background()
	bc.Seed(beads.Bead{ID: "demo-x", Title: "Nested under a closed step", Labels: []string{"kind:objective", beads.WorkstreamLabel}})
	bc.Edges = append(bc.Edges, beads.Dep{IssueID: "demo-x", DependsOnID: beads.NestedBStep, Type: "parent-child"})
	if err := bc.SetStatus(ctx, f.proj.Path, beads.NestedBStep, "closed"); err != nil {
		t.Fatal(err)
	}
	if got, err := StrictOwner(ctx, bc, f.proj.Path, "demo-x"); err != nil || got != beads.NestedB {
		t.Fatalf("StrictOwner = %s, %v; want the owner above the closed step", got, err)
	}
	nested, err := NestedOwners(ctx, bc, f.proj)
	if err != nil || slices.ContainsFunc(nested, func(tg Target) bool { return tg.BeadID == "demo-x" }) {
		t.Fatalf("listed under a closed step: %v %v", nested, err)
	}
}
