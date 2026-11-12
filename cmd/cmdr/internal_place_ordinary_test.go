package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func TestOrdinaryPlacePreservesApproval(t *testing.T) {
	ctx := context.Background()
	m, st, _ := retirementFixture(t)
	_ = m.SetLabel(ctx, "/d", "sample-flow", beads.WorkstreamLabel, true)
	// Use the actual shape: ordinary stage and approval under an operational bead.
	m.Seed(beads.Bead{ID: "sample-wrapper", Status: "open", Labels: []string{"kind:operational", "owner:manager-sample-flow", (beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: "fixture reconciliation"}).Label()}})
	_ = m.SetParent(ctx, "/d", "sample-wrapper", "sample-flow")
	_ = m.SetParent(ctx, "/d", "sample-flow.2", "sample-wrapper")
	_ = m.SetParent(ctx, "/d", "sample-flow.3", "sample-wrapper")
	before := retirementSnapshot(t, m, st)
	original, _ := m.Show(ctx, "/d", "sample-flow.2")
	for _, id := range []string{"sample-flow.2", "sample-flow.3"} {
		old, err := placeOrdinary(ctx, m, "/d", id, "sample-flow", "manager-sample-flow", true)
		if err != nil || old != "sample-wrapper" {
			t.Fatalf("dry: %s %v", old, err)
		}
		if got := retirementSnapshot(t, m, st); got != before {
			t.Fatal("dry run changed approval")
		}
		if parentOfBead(t, m, id) != "sample-wrapper" {
			t.Fatal("dry run moved bead")
		}
		if _, err := placeOrdinary(ctx, m, "/d", id, "sample-flow", "manager-sample-flow", false); err != nil {
			t.Fatal(err)
		}
	}
	// Snapshot includes the gate parent, which is the sole intended difference.
	afterGate, _ := m.Show(ctx, "/d", "sample-flow.3")
	if parentOfBead(t, m, afterGate.ID) != "sample-flow" {
		t.Fatal("gate not moved")
	}
	moved, _ := m.Show(ctx, "/d", "sample-flow.2")
	original.ParentID = "sample-flow"
	if !reflect.DeepEqual(original, moved) {
		t.Fatal("stage content changed")
	}
	// Restore only in this synthetic fixture so the full approval/store snapshot
	// can compare card, generation, answer history and blocking edges byte for byte.
	_ = m.SetParent(ctx, "/d", "sample-flow.3", "sample-wrapper")
	if got := retirementSnapshot(t, m, st); got != before {
		t.Fatalf("approval changed\nbefore %s\nafter %s", before, got)
	}
}

func TestOrdinaryPlaceRefuses(t *testing.T) {
	for _, mode := range []string{"operator", "foreign", "conversion", "task", "ownership", "cycle", "mixed", "missing", "custody"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m, add := placeFixture(t)
			root := add("scope", "", beads.WorkstreamLabel)
			wrapper := add("internal", root, "kind:operational")
			step := add("step", wrapper)
			role := "manager-" + root
			parent := root
			if mode != "custody" {
				_ = m.SetLabel(ctx, "", wrapper, (beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: "fixture hold"}).Label(), true)
			}
			switch mode {
			case "operator":
				role = "operator-sample"
			case "foreign":
				role = "manager-foreign"
			case "conversion":
				_ = m.SetLabel(ctx, "", step, beads.WorkstreamLabel, true)
			case "task":
				_ = m.SetLabel(ctx, "", step, "kind:task", true)
			case "ownership":
				parent = add("other", "", beads.WorkstreamLabel)
			case "cycle":
				_ = m.DependOn(ctx, "", root, step)
			case "mixed":
				_ = add("nested", root, beads.WorkstreamLabel)
			case "custody":
				gate := add("approval", root, "gate", "approver:user")
				_ = m.DependOn(ctx, "", wrapper, gate)
			case "missing":
				parent = "absent"
			}
			before, _ := m.Details(ctx, "", []string{step, wrapper, root})
			_, err := placeOrdinary(ctx, m, "/d", step, parent, role, false)
			expected := map[string]string{"operator": "Operators", "foreign": "owning Manager", "conversion": "excluded", "task": "excluded", "ownership": "ownership transfer", "cycle": "cycle", "mixed": "", "missing": "", "custody": "unheld"}[mode]
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %q refusal: %v", expected, err)
			}
			after, _ := m.Details(ctx, "", []string{step, wrapper, root})
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal wrote changes")
			}
		})
	}
}

func TestOrdinaryPlaceScenario(t *testing.T) {
	var scenario struct{ Root, Wrapper, Step, Gate, Moving, Descendant string }
	data, err := os.ReadFile("../../tests/evals/scenarios/ordinary-place.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	m := setupGateProject(t)
	ctx := context.Background()
	for _, b := range []beads.Bead{
		{ID: scenario.Root, Status: "open", Labels: []string{"kind:objective", beads.WorkstreamLabel}},
		{ID: scenario.Wrapper, Status: "open", Labels: []string{"kind:operational", "owner:manager-sample-flow", (beads.Wait{Kind: beads.WaitUntil, Since: time.Now(), Until: time.Now().Add(time.Hour), Ref: "fixture reconciliation"}).Label()}},
		{ID: scenario.Step, Status: "open", Labels: []string{"kind:objective"}},
		{ID: scenario.Gate, Status: "open", Labels: []string{"kind:objective", "gate", "approver:user", "needs-user", "owner:user"}},
	} {
		m.Seed(b)
	}
	_ = m.SetParent(ctx, "/d", scenario.Wrapper, scenario.Root)
	for _, id := range []string{scenario.Step, scenario.Gate} {
		_ = m.SetParent(ctx, "/d", id, scenario.Wrapper)
	}
	_ = m.DependOn(ctx, "/d", scenario.Step, scenario.Gate)
	t.Setenv("COMMANDER_ROLE", "manager-"+scenario.Root)
	for _, id := range []string{scenario.Step, scenario.Gate} {
		args := []string{"--project", "manifest", "--bead", id, "--parent", scenario.Root, "--ordinary"}
		for _, dry := range []bool{true, false} {
			call := append([]string(nil), args...)
			if dry {
				call = append(call, "--dry-run")
			}
			out, stderr, read := withTempOutputFiles(t)
			code := runInternalPlace(call, out, stderr)
			o, e := read()
			t.Log(strings.TrimSpace(o))
			if code != 0 || !strings.Contains(o, `"ordinary":true`) {
				t.Fatalf("code=%d %s %s", code, o, e)
			}
			expected := scenario.Root
			if dry {
				expected = scenario.Wrapper
			}
			if parentOfBead(t, m, id) != expected {
				t.Fatal("wrong parent")
			}
		}
	}
	// The manifest also drives the refusal through the real private CLI handler.
	m.Seed(beads.Bead{ID: scenario.Moving, Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: scenario.Descendant, Status: "open", Labels: []string{"kind:objective"}})
	_ = m.SetParent(ctx, "/d", scenario.Moving, scenario.Wrapper)
	_ = m.SetParent(ctx, "/d", scenario.Descendant, scenario.Moving)
	_ = m.DependOn(ctx, "/d", scenario.Wrapper, scenario.Gate)
	before, _ := m.Details(ctx, "/d", []string{scenario.Moving, scenario.Descendant, scenario.Wrapper, scenario.Gate})
	for _, dry := range []bool{true, false} {
		args := []string{"--project", "manifest", "--bead", scenario.Moving, "--parent", scenario.Root, "--ordinary"}
		if dry {
			args = append(args, "--dry-run")
		}
		out, stderr, read := withTempOutputFiles(t)
		code := runInternalPlace(args, out, stderr)
		_, e := read()
		if code == 0 || !strings.Contains(e, scenario.Descendant) {
			t.Fatalf("expected descendant custody refusal: %d %s", code, e)
		}
		after, _ := m.Details(ctx, "/d", []string{scenario.Moving, scenario.Descendant, scenario.Wrapper, scenario.Gate})
		if !reflect.DeepEqual(before, after) {
			t.Fatal("handler refusal mutated graph")
		}
	}

	ds, _ := m.Details(ctx, "/d", []string{scenario.Step})
	found := false
	for _, e := range ds[scenario.Step].Deps {
		if e.Type == "blocks" && e.DependsOnID == scenario.Gate {
			found = true
		}
	}
	if !found {
		t.Fatal("lost approval block")
	}
}

// The moving stage and its wrapper remain held by children. Only the deep
// leaf loses the approval inherited from the wrapper after reparenting.
func TestOrdinaryPlaceDescendantCustody(t *testing.T) {
	for _, safe := range []bool{false, true} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("safe=%t/dry=%t", safe, dry), func(t *testing.T) {
				ctx := context.Background()
				m, add := placeFixture(t)
				root := add("scope", "", beads.WorkstreamLabel)
				wrapper := add("wrapper", root, "kind:operational")
				stage := add("stage", wrapper)
				middle := add("middle", stage)
				leaf := add("leaf", middle)
				gate := add("approval", root, "gate", "approver:user")
				_ = m.DependOn(ctx, "", wrapper, gate)
				if safe {
					_ = m.DependOn(ctx, "", leaf, gate)
				}
				ids := []string{root, wrapper, stage, middle, leaf, gate}
				before, _ := m.Details(ctx, "", ids)
				_, err := placeOrdinary(ctx, m, "/d", stage, root, "manager-"+root, dry)
				if safe && err != nil {
					t.Fatal(err)
				}
				if !safe && (err == nil || !strings.Contains(err.Error(), leaf)) {
					t.Fatalf("expected leaf refusal, got %v", err)
				}
				after, _ := m.Details(ctx, "", ids)
				if (!safe || dry) && !reflect.DeepEqual(before, after) {
					t.Fatal("refusal/dry run mutated graph")
				}
			})
		}
	}
}

type ordinaryReadFailure struct {
	beads.Client
	target string
}

func (f ordinaryReadFailure) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	if id == f.target {
		return nil, errors.New("fixture subtree unreadable")
	}
	return f.Client.Dependents(ctx, dir, id)
}
func TestOrdinaryPlaceIncompleteSubtree(t *testing.T) {
	for _, mode := range []string{"unreadable", "missing", "cycle", "limit"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m, add := placeFixture(t)
			root := add("scope", "", beads.WorkstreamLabel)
			wrapper := add("wrapper", root, "kind:operational")
			stage := add("stage", wrapper)
			leaf := add("leaf", stage)
			_ = m.SetLabel(ctx, "", wrapper, "gate", true)
			var bc beads.Client = m
			switch mode {
			case "unreadable":
				bc = ordinaryReadFailure{m, leaf}
			case "missing":
				_ = m.DependOn(ctx, "", leaf, "absent")
			case "cycle":
				_ = m.DependOn(ctx, "", stage, leaf)
			case "limit":
				for n := 0; n < maxClosure; n++ {
					leaf = add(fmt.Sprintf("child %d", n), leaf)
				}
			}
			before, _ := m.List(ctx, "", beads.ListOptions{Status: "all"})
			for _, dry := range []bool{true, false} {
				if _, err := placeOrdinary(ctx, bc, "/d", stage, root, "manager-"+root, dry); err == nil {
					t.Fatal("allowed incomplete/cyclic/oversize subtree")
				}
				after, _ := m.List(ctx, "", beads.ListOptions{Status: "all"})
				if !reflect.DeepEqual(before, after) {
					t.Fatal("refusal mutated fixture")
				}
			}
		})
	}
}
