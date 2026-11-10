package livedata

import (
	"github.com/example-user/commander/internal/workflowtemplate"
	"maps"
	"slices"
)

// Workflow nodes are enriched with live PR state while building a frame. Keep
// that mutable enrichment private to the frame, including nested checks.
func copyWorkflowNode(n workflowtemplate.Node) workflowtemplate.Node {
	n.Bead.Labels = slices.Clone(n.Bead.Labels)
	n.Bead.Dependencies = slices.Clone(n.Bead.Dependencies)
	n.Waits = slices.Clone(n.Waits)
	n.Comments = slices.Clone(n.Comments)
	n.PRStates = maps.Clone(n.PRStates)
	n.Steps = copyWorkflowNodes(n.Steps)
	n.Blocks = copyWorkflowNodes(n.Blocks)
	return n
}
func copyWorkflowNodes(ns []workflowtemplate.Node) []workflowtemplate.Node {
	if ns == nil {
		return nil
	}
	out := make([]workflowtemplate.Node, len(ns))
	for i := range ns {
		out[i] = copyWorkflowNode(ns[i])
	}
	return out
}
func cloneObjectiveRows(rows []ObjectiveRow) []ObjectiveRow {
	out := slices.Clone(rows)
	for i := range out {
		out[i].GateChecks = copyWorkflowNodes(out[i].GateChecks)
		if n := out[i].WorkflowCheck; n != nil {
			c := copyWorkflowNode(*n)
			out[i].WorkflowCheck = &c
		}
	}
	return out
}
