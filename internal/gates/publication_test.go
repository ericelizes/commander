package gates

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func publicationGraph() *beads.MemClient {
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "sample", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "stage", ParentID: "sample", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "card", Title: "Choose pilot", Description: "Existing scope", Labels: []string{KindLabel, "gate-of:stage", "gate-name:pilot", "verifier:user", "approver:user"}})
	m.SetParent(context.Background(), "/p", "stage", "sample")
	return m
}

func TestPublicationPreservesGate(t *testing.T) {
	ctx := context.Background()
	m := publicationGraph()
	m.Seed(beads.Bead{ID: "prior", Status: "closed"})
	m.Edges = append(m.Edges, beads.Dep{IssueID: "card", DependsOnID: "prior", Type: "blocks"})
	before, _ := m.Show(ctx, "/p", "card")
	ds, _ := m.Details(ctx, "/p", []string{"card"})
	for range 2 {
		if err := Publish(ctx, m, "/p", "card", "sample"); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := m.Show(ctx, "/p", "card")
	after.ParentID = before.ParentID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("card content changed")
	}
	got, _ := m.Details(ctx, "/p", []string{"card"})
	if got["card"].Parent != "sample" || !reflect.DeepEqual(blocks(ds["card"]), blocks(got["card"])) {
		t.Fatal(got)
	}
}
func blocks(d beads.Detail) []beads.Dep {
	var out []beads.Dep
	for _, dep := range d.Deps {
		if dep.Type == "blocks" {
			out = append(out, dep)
		}
	}
	return out
}
func TestNewGateHasStageParent(t *testing.T) {
	ctx := context.Background()
	m := publicationGraph()
	id, err := Upsert(ctx, m, "/p", store.Gate{BeadID: "stage", Name: "pilot", Verifier: "user", Check: "Choose whether pilot proceeds"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Show(ctx, "/p", id)
	if b.ParentID != "sample" {
		t.Fatal(b)
	}
	ds, err := m.Details(ctx, "/p", []string{id})
	if err != nil || ds[id].Parent != "sample" {
		t.Fatal(ds, err)
	}
	// A guarded stage can wait on its sibling gate without a parent/wait cycle.
	m.Edges = append(m.Edges, beads.Dep{IssueID: "stage", DependsOnID: id, Type: "blocks"})
}
func TestPublicationRefusesUnsafeStructure(t *testing.T) {
	for _, mode := range []string{"held", "held-ancestor", "held-stage", "cycle", "ancestor-cycle", "wrong-parent", "unlabelled", "gate-parent", "track-root", "missing", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m := publicationGraph()
			parent := "sample"
			switch mode {
			case "held":
				m.SetLabel(ctx, "/p", "card", "hold", true)
			case "held-ancestor":
				m.SetLabel(ctx, "/p", "sample", "hold", true)
			case "held-stage":
				m.SetLabel(ctx, "/p", "stage", "hold", true)
			case "cycle":
				m.SetParent(ctx, "/p", "sample", "card")
			case "ancestor-cycle":
				m.SetParent(ctx, "/p", "sample", "stage")
			case "wrong-parent":
				m.SetParent(ctx, "/p", "card", "stage")
			case "unlabelled":
				m.SetLabel(ctx, "/p", "sample", "kind:objective", false)
			case "gate-parent":
				m.SetLabel(ctx, "/p", "sample", KindLabel, true)
			case "track-root":
				m.SetLabel(ctx, "/p", "sample", beads.TrackLabel, true)
			case "missing":
				m.SetParent(ctx, "/p", "sample", "absent")
			case "mismatch":
				// Existing alternative objective and parentless card isolate gate-of validation.
				m.Seed(beads.Bead{ID: "alternative", Labels: []string{"kind:objective"}})
				parent = "alternative"
			}
			if err := Publish(ctx, m, "/p", "card", parent); err == nil {
				t.Fatal("unsafe publication accepted")
			}
			ds, _ := m.Details(ctx, "/p", []string{"card"})
			if mode != "wrong-parent" && ds["card"].Parent != "" {
				t.Fatal("wrote parent on refusal")
			}
		})
	}
}

type publicationFailure struct {
	beads.Client
	failRead, failWrite bool
}

func (c *publicationFailure) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	if c.failRead {
		return nil, errors.New("read failed")
	}
	return c.Client.Details(ctx, dir, ids)
}
func (c *publicationFailure) SetParent(ctx context.Context, dir, id, parent string) error {
	if c.failWrite {
		return errors.New("write failed")
	}
	return c.Client.SetParent(ctx, dir, id, parent)
}
func TestPublicationDryRunAndRetry(t *testing.T) {
	ctx := context.Background()
	m := publicationGraph()
	c := &publicationFailure{Client: m}
	if err := PublishPlan(ctx, c, "/p", "card", "sample", true); err != nil {
		t.Fatal(err)
	}
	ds, _ := m.Details(ctx, "/p", []string{"card"})
	if ds["card"].Parent != "" {
		t.Fatal("dry run wrote")
	}
	c.failRead = true
	if Publish(ctx, c, "/p", "card", "sample") == nil {
		t.Fatal("ignored read error")
	}
	c.failRead = false
	c.failWrite = true
	if Publish(ctx, c, "/p", "card", "sample") == nil {
		t.Fatal("ignored write error")
	}
	c.failWrite = false
	if err := Publish(ctx, c, "/p", "card", "sample"); err != nil {
		t.Fatal(err)
	}
}

// bd refuses the parent write when an ancestor waits on the gate; the refusal
// names the way out and the write's own error stays visible.
type cycleOnParent struct{ beads.Client }

func (cycleOnParent) SetParent(context.Context, string, string, string) error {
	return errors.New("adding dependency would create a cycle")
}

func TestPublishCycleRefusalNamesTheRepair(t *testing.T) {
	ctx := context.Background()
	m := publicationGraph()
	err := Publish(ctx, cycleOnParent{m}, "/p", "card", "sample")
	if err == nil || !strings.Contains(err.Error(), "would create a cycle") || !strings.Contains(err.Error(), "--repair-ancestor-waits") {
		t.Fatalf("got %v", err)
	}
	m.SetParent(ctx, "/p", "sample", "card")
	err = PublishPlan(ctx, m, "/p", "card", "sample", true)
	if err == nil || !strings.Contains(err.Error(), "--repair-ancestor-waits") {
		t.Fatalf("parent-chain cycle: %v", err)
	}
}
