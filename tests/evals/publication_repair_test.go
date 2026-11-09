package evals

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
)

// Five invented wrapper waits exercise the reported shape without real board data.
func TestPublicationRepairScenario(t *testing.T) {
	raw, err := os.ReadFile("scenarios/publication-repair.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Root, Leaf, Gate, Sibling, Reason string
		Ancestors                         []string
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: s.Root, Labels: []string{"kind:objective"}})
	prev := s.Root
	for i, id := range append(append([]string{}, s.Ancestors...), s.Leaf) {
		labels := []string{"kind:objective"}
		if i == 0 {
			labels = append(labels, "kind:track")
		}
		m.Seed(beads.Bead{ID: id, Labels: labels})
		m.SetParent(ctx, "/fixture", id, prev)
		m.DependOn(ctx, "/fixture", id, s.Gate)
		prev = id
	}
	m.Seed(beads.Bead{ID: s.Gate, Title: "Approve sample pilot", Description: "Original bounded pilot scope", Labels: []string{"kind:gate", "gate-of:" + s.Leaf, "verifier:user", "approver:user"}})
	m.AddComment(ctx, "/fixture", s.Gate, "Original user decision history")
	before, _ := m.Show(ctx, "/fixture", s.Gate)
	parent := s.Ancestors[len(s.Ancestors)-1]
	run := func(actor string, dry bool) error {
		return gates.RepairPublication(ctx, m, "/fixture", s.Gate, parent, s.Ancestors, s.Reason, actor, dry)
	}
	if err = run("manager-foreign", false); err == nil {
		t.Fatal("foreign manager accepted")
	}
	// A sibling under a wrapper is work the wait still guards.
	m.Seed(beads.Bead{ID: s.Sibling, Labels: []string{"kind:objective"}})
	m.SetParent(ctx, "/fixture", s.Sibling, s.Ancestors[2])
	if err = run("manager-"+s.Root, false); err == nil || !strings.Contains(err.Error(), "would release live "+s.Sibling) {
		t.Fatalf("sibling with no wait of its own: %v", err)
	}
	t.Log("sibling with no wait of its own refused before any write")
	m.DependOn(ctx, "/fixture", s.Sibling, s.Gate)
	if err = run("manager-"+s.Root, true); err != nil {
		t.Fatal(err)
	}
	ds, _ := m.Details(ctx, "/fixture", append([]string{s.Gate, s.Leaf}, s.Ancestors...))
	for _, id := range s.Ancestors {
		if len(ds[id].Deps) != 2 {
			t.Fatal("dry run changed waits")
		}
	}
	if ds[s.Gate].Parent != "" {
		t.Fatal("dry run published")
	}
	t.Log("dry-run: five ancestor removals validated; no writes")
	for range 2 {
		if err = run("manager-"+s.Root, false); err != nil {
			t.Fatal(err)
		}
	}
	ds, _ = m.Details(ctx, "/fixture", append([]string{s.Gate, s.Leaf}, s.Ancestors...))
	for _, id := range s.Ancestors {
		for _, e := range ds[id].Deps {
			if e.Type == "blocks" && e.DependsOnID == s.Gate {
				t.Fatal("ancestor still waits")
			}
		}
	}
	if ds[s.Gate].Parent != parent {
		t.Fatal("not published")
	}
	protected := false
	for _, e := range ds[s.Leaf].Deps {
		protected = protected || e.Type == "blocks" && e.DependsOnID == s.Gate
	}
	if !protected {
		t.Fatal("lost leaf wait")
	}
	after, _ := m.Show(ctx, "/fixture", s.Gate)
	after.ParentID = before.ParentID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed card")
	}
	notes, _ := m.Comments(ctx, "/fixture", s.Gate)
	if len(notes) != 3 || notes[0].Text != "Original user decision history" {
		t.Fatal(notes)
	}
	t.Log("published original card; removed only five ancestor waits; protected leaf and approval history preserved; retry idempotent")
	// The protected real chain is never an acceptable cycle-breaking shortcut.
	m.DependOn(ctx, "/fixture", s.Gate, s.Leaf)
	if err = run("manager-"+s.Root, true); err == nil {
		t.Fatal("accepted real gate chain cycle")
	}
	t.Log("real gate-chain cycle refused")
}
