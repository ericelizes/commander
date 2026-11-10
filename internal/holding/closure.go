package holding

import "sort"

// Edit changes a bead the way a write will leave it.
type Edit func(*Bead)

// Graph is the part of a project a write can disturb, read fresh by the writer
// (never from a display snapshot): the beads, who is a child of whom and who waits
// on whom. It is a World, so Held judges it exactly as the board does.
type Graph struct {
	Beads      map[string]Bead
	Titles     map[string]string
	Children   map[string][]string
	Dependents map[string][]string // bead -> beads that wait on it
	Live       func(owner string) (state string, known bool)
}

// NewGraph is an empty graph with the given live-agent source (nil: every owner is taken as live).
func NewGraph(live func(owner string) (string, bool)) *Graph {
	return &Graph{Beads: map[string]Bead{}, Titles: map[string]string{}, Children: map[string][]string{}, Dependents: map[string][]string{}, Live: live}
}

func (g *Graph) Bead(id string) (Bead, bool) { b, ok := g.Beads[id]; return b, ok }

func (g *Graph) HasOpenChild(id string) bool {
	for _, c := range g.Children[id] {
		if b, ok := g.Beads[c]; ok && b.Status != StatusClosed && b.Status != StatusDeferred {
			return true
		}
	}
	return false
}

func (g *Graph) OwnerLive(owner string) (string, bool) {
	if g.Live == nil {
		return "", false
	}
	return g.Live(owner)
}

// With is a copy of the graph with edits applied. An edit that moves a bead
// (changes its Parent) moves it between the parents' child lists too.
func (g *Graph) With(edits map[string]Edit) *Graph {
	out := *g
	out.Beads = make(map[string]Bead, len(g.Beads))
	out.Children = make(map[string][]string, len(g.Children))
	for p, cs := range g.Children {
		out.Children[p] = append([]string(nil), cs...)
	}
	for id, b := range g.Beads {
		old := b.Parent
		if e := edits[id]; e != nil {
			e(&b)
		}
		out.Beads[id] = b
		if b.Parent != old {
			if old != "" {
				out.Children[old] = without(out.Children[old], id)
			}
			if b.Parent != "" && !contains(out.Children[b.Parent], id) {
				out.Children[b.Parent] = append(out.Children[b.Parent], id)
			}
		}
	}
	return &out
}

func without(xs []string, x string) []string {
	out := xs[:0]
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Closure is the beads a write to id can leave unheld: itself, its ancestors (the
// last open child's parent), everything under it, the beads that wait on it and
// what is under those.
func (g *Graph) Closure(id string) []string {
	seen := map[string]bool{}
	var down func(string)
	down = func(x string) {
		if seen[x] {
			return
		}
		seen[x] = true
		for _, c := range g.Children[x] {
			down(c)
		}
	}
	for cur, n := id, 0; cur != "" && n < 64; n++ {
		seen[cur] = true
		b, ok := g.Beads[cur]
		if !ok {
			break
		}
		cur = b.Parent
	}
	down(id)
	for _, d := range g.Dependents[id] {
		down(d)
	}
	out := make([]string, 0, len(seen))
	for x := range seen {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

// Violation is a bead a write would leave with no holder.
type Violation struct {
	ID    string
	Title string
	Why   Why
}

// NewlyUnheld judges id's closure before and after the edits: the beads that were
// held (or closed) and would not be. A bead already unheld is not this write's doing.
func NewlyUnheld(g *Graph, id string, edits map[string]Edit) []Violation {
	after := g.With(edits)
	var out []Violation
	for _, x := range g.Closure(id) {
		b, ok := g.Beads[x]
		if !ok {
			continue
		}
		if _, held, _ := Held(b, g); !held && b.Status != StatusClosed {
			continue
		}
		a := after.Beads[x]
		if _, held, why := Held(a, after); !held && why != WhyClosed {
			out = append(out, Violation{ID: x, Title: g.Titles[x], Why: why})
		}
	}
	return out
}
