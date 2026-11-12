package beads

import (
	"errors"
	"testing"
)

func TestQualifiedClosureIdentityAndProvenance(t *testing.T) {
	a, b, c := qualifiedNode{1, "SYN-1"}, qualifiedNode{2, "FIX-2"}, qualifiedNode{3, "FIX-2"}
	nodes := map[qualifiedNode]Bead{a: {ID: a.ID}, b: {ID: b.ID}}
	edges := []qualifiedEdge{{a, b, "blocks"}}
	counts := map[string]int{a.ID: 1, b.ID: 1}
	if got, e := qualifiedClosure(nodes, edges, counts, []qualifiedNode{a}, 8); e != nil || got[b.ID] != b {
		t.Fatalf("qualified provenance: %v %v", got, e)
	}
	nodes[c] = Bead{ID: c.ID}
	counts[b.ID] = 2
	if _, e := qualifiedClosure(nodes, edges, counts, []qualifiedNode{a}, 8); !IsCrossProjectError(e) {
		t.Fatalf("later duplicate accepted: %v", e)
	}
	delete(nodes, c)
	counts[b.ID] = 1
	delete(nodes, b)
	if _, e := qualifiedClosure(nodes, edges, counts, []qualifiedNode{a}, 8); !errors.Is(e, ErrNotFound) || !IsCrossProjectError(e) {
		t.Fatalf("deleted endpoint: %v", e)
	}
	nodes[b] = Bead{ID: b.ID}
	wrong := []qualifiedEdge{{a, c, "blocks"}}
	if _, e := qualifiedClosure(nodes, wrong, counts, []qualifiedNode{a}, 8); !IsCrossProjectError(e) {
		t.Fatalf("wrong project accepted: %v", e)
	}
	if _, e := qualifiedClosure(nodes, []qualifiedEdge{{a, b, "parent-child"}}, counts, []qualifiedNode{a}, 8); !IsCrossProjectError(e) {
		t.Fatalf("foreign parent accepted: %v", e)
	}
	if _, e := qualifiedClosure(nodes, edges, counts, []qualifiedNode{a}, 1); !IsCrossProjectError(e) {
		t.Fatalf("bound accepted: %v", e)
	}
}

func TestQualifiedCycleTraversesProjectsAndRefusesExhaustion(t *testing.T) {
	a, b, c := qualifiedNode{1, "SYN-1"}, qualifiedNode{2, "FIX-2"}, qualifiedNode{3, "AUX-3"}
	edges := []qualifiedEdge{{b, c, "blocks"}, {c, a, "blocks"}}
	if e := qualifiedCycle(edges, a, b, "blocks", 8); !IsCrossProjectError(e) {
		t.Fatalf("global cycle accepted: %v", e)
	}
	if e := qualifiedCycle(edges, qualifiedNode{4, "OTHER-4"}, b, "blocks", 1); !IsCrossProjectError(e) {
		t.Fatalf("exhaustion accepted: %v", e)
	}
	if e := qualifiedCycle(edges, a, b, "related", 8); e != nil {
		t.Fatal(e)
	}
}

func TestQualifiedClosureDoesNotRequireUnrelatedIDsToBeUnique(t *testing.T) {
	a, b, local := qualifiedNode{1, "SYN-1"}, qualifiedNode{2, "FIX-2"}, qualifiedNode{1, "DUP-3"}
	nodes := map[qualifiedNode]Bead{a: {ID: a.ID}, b: {ID: b.ID}, local: {ID: local.ID}}
	// A duplicate elsewhere in registered storage is not reached by any edge.
	counts := map[string]int{a.ID: 1, b.ID: 1, local.ID: 2}
	got, e := qualifiedClosure(nodes, []qualifiedEdge{{a, b, "blocks"}}, counts, []qualifiedNode{a, local}, 8)
	if e != nil || got[local.ID] != local {
		t.Fatalf("unreferenced duplicate rejected: %v %v", got, e)
	}
	// The same local ID becomes referenced by a foreign component and must refuse.
	_, e = qualifiedClosure(nodes, []qualifiedEdge{{a, b, "blocks"}, {b, local, "blocks"}}, counts, []qualifiedNode{a, local}, 8)
	if !IsCrossProjectError(e) {
		t.Fatalf("reached ambiguous identity accepted: %v", e)
	}
}
