package main

import (
	"context"
	"encoding/json"
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
				_ = m.DependOn(ctx, "", wrapper, step)
			case "missing":
				parent = "absent"
			}
			before, _ := m.Details(ctx, "", []string{step, wrapper, root})
			if _, err := placeOrdinary(ctx, m, "/d", step, parent, role, false); err == nil {
				t.Fatal("allowed unsafe move")
			}
			after, _ := m.Details(ctx, "", []string{step, wrapper, root})
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal wrote changes")
			}
		})
	}
}

func TestOrdinaryPlaceScenario(t *testing.T) {
	var scenario struct{ Root, Wrapper, Step, Gate string }
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
