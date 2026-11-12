package beads

import (
	"errors"
	"fmt"
)

// qualifiedNode is deliberately private: public clients retain ID-only identities.
type qualifiedNode struct {
	Project int64
	ID      string
}
type qualifiedEdge struct {
	Source, Target qualifiedNode
	Kind           string
}

const qualifiedGraphLimit = 4096

// qualifiedClosure validates provenance before any IDs can be flattened. Counts
// include all registered projects, not just projects participating in this graph.
// An unrelated duplicate is harmless; a duplicate reached through a foreign
// component is not. Tombstones are absent from nodes and cannot satisfy an edge.
func qualifiedClosure(nodes map[qualifiedNode]Bead, edges []qualifiedEdge, counts map[string]int, roots []qualifiedNode, limit int) (map[string]qualifiedNode, error) {
	if limit <= 0 {
		return nil, errors.New("invalid qualified closure bound")
	}
	adjacency := map[qualifiedNode][]qualifiedNode{}
	foreign := map[qualifiedNode]bool{}
	for _, e := range edges {
		adjacency[e.Source] = append(adjacency[e.Source], e.Target)
		adjacency[e.Target] = append(adjacency[e.Target], e.Source)
		if e.Source.Project != e.Target.Project {
			foreign[e.Source], foreign[e.Target] = true, true
		}
	}
	out := map[string]qualifiedNode{}
	reached := map[qualifiedNode]bool{}
	for _, root := range roots {
		if reached[root] {
			continue
		}
		component := []qualifiedNode{}
		queue := []qualifiedNode{root}
		hasForeign := false
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			if reached[n] {
				continue
			}
			if len(reached) >= limit {
				return nil, qualifiedFailure("closure", n.ID, fmt.Errorf("qualified closure exceeds %d nodes", limit))
			}
			if _, ok := nodes[n]; !ok {
				return nil, qualifiedFailure("endpoint", n.ID, ErrNotFound)
			}
			reached[n] = true
			component = append(component, n)
			hasForeign = hasForeign || foreign[n]
			queue = append(queue, adjacency[n]...)
		}
		for _, n := range component {
			if hasForeign && counts[n.ID] != 1 {
				return nil, qualifiedFailure("identity", n.ID, errors.New("reached ID is ambiguous across registered projects"))
			}
			if prior, ok := out[n.ID]; ok && prior != n {
				return nil, qualifiedFailure("identity", n.ID, errors.New("qualified nodes cannot be flattened unambiguously"))
			}
			out[n.ID] = n
		}
	}
	for _, e := range edges {
		if reached[e.Source] && e.Kind == "parent-child" && e.Source.Project != e.Target.Project {
			return nil, qualifiedFailure("parent", e.Source.ID, errors.New("parent must belong to source project"))
		}
	}

	return out, nil
}

func qualifiedFailure(op, id string, err error) error {
	return &CrossProjectError{Operation: op, Bead: id, Err: err}
}

// qualifiedCycle uses qualified endpoints and refuses exhaustion rather than
// guessing acyclicity. Callers serialize graph and identity mutations together.
func qualifiedCycle(edges []qualifiedEdge, source, target qualifiedNode, kind string, limit int) error {
	adjacency := map[qualifiedNode][]qualifiedNode{}
	for _, e := range edges {
		if e.Kind == kind {
			adjacency[e.Source] = append(adjacency[e.Source], e.Target)
		}
	}
	seen := map[qualifiedNode]bool{}
	queue := []qualifiedNode{target}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n == source {
			return qualifiedFailure("cycle", source.ID, errors.New("dependency would introduce a cycle"))
		}
		if seen[n] {
			continue
		}
		if len(seen) >= limit {
			return qualifiedFailure("cycle", source.ID, fmt.Errorf("cycle traversal exceeds %d nodes", limit))
		}
		seen[n] = true
		queue = append(queue, adjacency[n]...)
	}
	return nil
}
