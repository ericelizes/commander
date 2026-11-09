package holding

import "testing"

func graphOf() *Graph {
	g := NewGraph(func(owner string) (string, bool) {
		if owner == "operator-a" {
			return "working", true
		}
		return "idle", true
	})
	add := func(b Bead, kids ...string) {
		g.Beads[b.ID] = b
		g.Children[b.ID] = kids
		for _, k := range kids {
			_ = k
		}
		if b.Parent != "" {
			g.Children[b.Parent] = append(g.Children[b.Parent], b.ID)
		}
		for _, bl := range b.BlockedBy {
			g.Dependents[bl] = append(g.Dependents[bl], b.ID)
		}
	}
	add(Bead{ID: "w", Status: "open"})
	add(Bead{ID: "a", Parent: "w", Status: "in_progress", Owner: "operator-a"})
	add(Bead{ID: "b", Parent: "w", Status: "open", BlockedBy: []string{"a"}})
	return g
}

func ids(v []Violation) []string {
	var out []string
	for _, x := range v {
		out = append(out, x.ID)
	}
	return out
}

func TestClosureCloseReleasingADependantIsUnheld(t *testing.T) {
	g := graphOf()
	got := NewlyUnheld(g, "a", map[string]Edit{"a": func(b *Bead) { b.Status = StatusClosed }})
	if len(got) != 1 || got[0].ID != "b" || got[0].Why != WhyNoHolder {
		t.Fatalf("closing the blocker leaves b unheld: %+v", got)
	}
	// With a holder written first nothing is left unheld.
	got = NewlyUnheld(g, "a", map[string]Edit{"a": func(b *Bead) { b.Status = StatusClosed }, "b": func(b *Bead) { b.Waits = 1 }})
	if len(got) != 0 {
		t.Fatalf("held in the same write: %+v", got)
	}
}

func TestClosureDroppingTheOnlyHolderOrLastChild(t *testing.T) {
	g := graphOf()
	if got := NewlyUnheld(g, "a", map[string]Edit{"a": func(b *Bead) { b.Owner = "" }}); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("owner none: %+v", got)
	}
	// Removing the dependant's blocker edge releases it.
	if got := NewlyUnheld(g, "b", map[string]Edit{"b": func(b *Bead) { b.BlockedBy = nil }}); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("dep rm: %+v", got)
	}
	// The parent was held by its children: closing both leaves it unheld.
	both := map[string]Edit{"a": func(b *Bead) { b.Status = StatusClosed }, "b": func(b *Bead) { b.Status = StatusClosed }}
	if got := NewlyUnheld(g, "a", both); len(got) != 1 || got[0].ID != "w" {
		t.Fatalf("last open child closed: %+v", got)
	}
}

func TestClosureLeavesAlreadyUnheldBeadsAlone(t *testing.T) {
	g := graphOf()
	g.Beads["b"] = Bead{ID: "b", Parent: "w", Status: "open"}
	if got := NewlyUnheld(g, "a", map[string]Edit{"a": func(b *Bead) { b.Status = StatusClosed }}); len(got) != 0 {
		t.Fatalf("b was unheld before this write: %+v", got)
	}
}

// Moving a parent's last open child away leaves the parent with nothing under it.
func TestClosureMoveAwayTheLastChild(t *testing.T) {
	g := NewGraph(func(string) (string, bool) { return "", true })
	g.Beads["p"] = Bead{ID: "p", Status: "open"}
	g.Beads["q"] = Bead{ID: "q", Status: "open", Waits: 1}
	g.Beads["k"] = Bead{ID: "k", Parent: "p", Status: "open", Waits: 1}
	g.Children["p"] = []string{"k"}
	move := map[string]Edit{"k": func(b *Bead) { b.Parent = "q" }}
	if got := ids(NewlyUnheld(g, "k", move)); len(got) != 1 || got[0] != "p" {
		t.Fatalf("moving k away from p: %v, want [p]", got)
	}
	if g.Children["p"][0] != "k" {
		t.Fatal("With changed the original graph")
	}
	g.Beads["k2"] = Bead{ID: "k2", Parent: "p", Status: "open", Waits: 1}
	g.Children["p"] = append(g.Children["p"], "k2")
	if got := NewlyUnheld(g, "k", move); len(got) != 0 {
		t.Fatalf("another child keeps p held: %v", got)
	}
}
