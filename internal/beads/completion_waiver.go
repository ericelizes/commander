package beads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// CompletionWaiver is an explicit typed no-PR request. It is never inferred
// from a freeform comment. Existing user/role and human-decision checks still
// apply; this request can waive only the merge rule's missing-PR finding.
type CompletionWaiver struct {
	Bead       string `json:"bead"`
	Definition string `json:"definition"`
	Reason     string `json:"reason"`
	Evidence   string `json:"evidence"`
}
type completionWaiversKey struct{}

func WithCompletionWaivers(ctx context.Context, waivers []CompletionWaiver) context.Context {
	copy := slices.Clone(waivers)
	return context.WithValue(ctx, completionWaiversKey{}, copy)
}
func completionWaiversOf(ctx context.Context) []CompletionWaiver {
	value, _ := ctx.Value(completionWaiversKey{}).([]CompletionWaiver)
	return slices.Clone(value)
}

// CompletionDefinition binds criteria, PRs, gate/workflow identity and graph
// edges. Operational status and a retirement written by this same typed flow
// are not new definitions. New holds and changed planning scope are included.
func CompletionDefinition(b Bead, d Detail) string {
	labels := []string{}
	for _, label := range b.Labels {
		if label == "hold" || label == "gate" || strings.HasPrefix(label, "kind:") || strings.HasPrefix(label, "approver:") || strings.HasPrefix(label, "verifier:") || strings.HasPrefix(label, "pr:") || strings.HasPrefix(label, "gate-of:") || strings.HasPrefix(label, "gate-name:") || strings.HasPrefix(label, "workflow-") {
			labels = append(labels, label)
		}
	}
	slices.Sort(labels)
	edges := slices.Clone(d.Deps)
	slices.SortFunc(edges, func(a, b Dep) int {
		if n := strings.Compare(a.Type, b.Type); n != 0 {
			return n
		}
		if n := strings.Compare(a.IssueID, b.IssueID); n != 0 {
			return n
		}
		return strings.Compare(a.DependsOnID, b.DependsOnID)
	})
	raw, _ := json.Marshal(struct {
		ID, Title, Description, Acceptance, Type, Parent, Commander string
		Labels                                                      []string
		Edges                                                       []Dep
	}{b.ID, b.Title, b.Description, b.AcceptanceCriteria, b.Type, d.Parent, b.PlanningCommander, labels, edges})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
