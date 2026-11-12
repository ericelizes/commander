package workflowclose

import (
	"context"
	"fmt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
	"slices"
	"strings"
)

// checkSettlement accepts an explicit unreviewed settlement only when its
// recorded identity and the block PR still match fresh merged facts. It
// never emits passing review evidence or manufactures a writer binding.
func (v Verifier) checkSettlement(ctx context.Context, bc beads.Client, dir, project string, n, block workflowtemplate.Node, instance, step string) (bool, error) {
	if !workflowtemplate.IsReviewBlock(block.Bead) {
		return false, nil
	}
	pr := workflowtemplate.BlockPR(block.Bead)
	var receipt *workflowtemplate.Evidence
	for _, comment := range n.Comments {
		e, ok := workflowtemplate.ParseEvidence(comment.Text)
		if ok && e.Verifier == workflowtemplate.SettledVerifier {
			copy := e
			receipt = &copy
		}
	}
	if receipt == nil {
		return false, nil
	}
	h, err := workflowtemplate.ReadHead(ctx, v.gh(), pr)
	if err != nil {
		return false, err
	}
	reason, ok := strings.CutPrefix(receipt.Finding, workflowtemplate.SettledFinding(h.SHA, ""))
	if h.State != "MERGED" || receipt.Instance != instance || receipt.Step != step || receipt.PR != pr || receipt.Head != h.SHA || !ok || len(strings.TrimSpace(reason)) < 15 {
		return false, fmt.Errorf("settlement receipt must name this step and the freshly merged PR head, with its reason")
	}
	nodes, err := v.Nodes(ctx, bc, dir, block.Bead.ID)
	if err != nil {
		return false, err
	}
	if err = v.SettledTargets(ctx, nodes, project, v.repo(dir), block.Bead); err != nil {
		return false, err
	}
	return true, nil
}
func (v Verifier) SettledTargets(ctx context.Context, nodes []workflowtemplate.Node, project, repo string, block beads.Bead) error {
	for _, n := range nodes {
		if !slices.Contains(n.Waits, block.ID) || !workflowtemplate.PluggedBefore(block, n.Bead.ID, project) || n.Bead.Status == "closed" {
			continue
		}
		prs := []string{workflowtemplate.BlockPR(block)}
		states := map[string]string{}
		for _, pr := range prs {
			h, err := workflowtemplate.ReadHead(ctx, v.gh(), pr)
			if err != nil {
				return fmt.Errorf("could not read %s, nothing settled: %w", pr, err)
			}
			states[pr] = h.State
		}
		if !workflowtemplate.Settled(prs, states) {
			return fmt.Errorf("%s still has a live PR (%v): its review is not settled by the merge; nothing settled", n.Bead.ID, prs)
		}
	}
	return nil
}
