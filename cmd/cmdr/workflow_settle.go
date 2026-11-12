package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// workflowSettleResult is what settle-block did: the steps it closed as settled,
// the ones already closed (their evidence kept as history) and the block's close.
type workflowSettleResult struct {
	Block       string   `json:"block"`
	PR          string   `json:"pr"`
	Head        string   `json:"head"`
	Settled     []string `json:"settled"`
	Kept        []string `json:"kept"`
	BlockClosed bool     `json:"block_closed"`
	BlockError  string   `json:"block_error,omitempty"`
}

// settleCloseBlock closes the block through the normal close door (merge rule,
// gates, custody of what it releases). A var so tests can watch it.
var settleCloseBlock = func(project, block, reason string) error {
	return runQuiet(runInternalCloseObjective, []string{"--project", project, "--bead", block, "--reason", reason})
}

// settleWorkflowBlock closes the open steps of a review block whose PR merged
// outside the review. Every read is fresh and any failed read refuses before a
// write. It fabricates nothing: no writer binding, proof or check result; each
// step it closes records only that the PR merged at its head and the review was
// not completed. Steps already closed keep their evidence as it is.
func settleWorkflowBlock(ctx context.Context, bc beads.Client, dir, project, blockID, reason string) (*workflowSettleResult, error) {
	if callerIsOperator("") || workflowExecutionCaller() {
		return nil, fmt.Errorf("settling a review block belongs to the owning Manager, Commander or the user; Operators cannot settle it")
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) < 15 {
		return nil, fmt.Errorf("--reason must say why the review was not completed (15+ characters)")
	}
	bc = freshReader(bc)
	b, err := bc.Show(ctx, dir, blockID)
	if err != nil {
		return nil, err
	}
	if b == nil || !workflowtemplate.IsReviewBlock(*b) {
		return nil, fmt.Errorf("%s is not a pr-review block", blockID)
	}
	tpl, err := workflowtemplate.Definition(*b)
	if err != nil {
		return nil, err
	}
	root, err := workflowRoot(ctx, bc, dir, blockID)
	if err != nil {
		return nil, err
	}
	if err := authorizeWorkflowOwner(ctx, bc, dir, *b, root, "settling "+blockID, true); err != nil {
		return nil, err
	}
	pr := workflowtemplate.BlockPR(*b)
	h, err := workflowtemplate.ReadHead(ctx, workflowGH, pr)
	if err != nil {
		return nil, fmt.Errorf("could not read %s, nothing settled: %w", pr, err)
	}
	if h.State != "MERGED" {
		return nil, fmt.Errorf("%s is %s, not merged: an open PR is reviewed, not settled; nothing settled", pr, strings.ToLower(h.State))
	}
	nodes, err := workflowNodes(ctx, bc, dir, blockID)
	if err != nil {
		return nil, err
	}
	if err := settledTargets(ctx, nodes, project, workflowRepo(dir), *b); err != nil {
		return nil, err
	}
	key := workflowtemplate.Label(*b, workflowtemplate.LabelInstance)
	steps := map[string]workflowtemplate.Node{}
	for _, n := range nodes {
		step := workflowtemplate.Label(n.Bead, workflowtemplate.LabelStep)
		if step == "" || workflowtemplate.Label(n.Bead, workflowtemplate.LabelInstance) != key {
			continue
		}
		if _, dup := steps[step]; dup {
			return nil, fmt.Errorf("block %s has two beads for step %s; nothing settled", blockID, step)
		}
		if workflowtemplate.Label(n.Bead, workflowtemplate.LabelHash) != tpl.Hash() || n.Bead.ParentID != blockID {
			return nil, fmt.Errorf("workflow step %s does not match block %s; nothing settled", n.Bead.ID, blockID)
		}
		steps[step] = n
	}
	// A step's own gates (a gate-of gate has no blocks edge) must have passed, as
	// at every close door; read before the first write, any failed read refuses.
	for _, s := range settleOrder(tpl) {
		if n, ok := steps[s.ID]; ok && n.Bead.Status != "closed" {
			if err := requireGatesPassed(ctx, project, n.Bead.ID); err != nil {
				return nil, fmt.Errorf("%w; nothing settled", err)
			}
		}
	}
	res := &workflowSettleResult{Block: blockID, PR: pr, Head: h.SHA, Settled: []string{}, Kept: []string{}}
	closeReason := "settled: PR merged at " + h.SHA + ", review not completed"
	for _, s := range settleOrder(tpl) {
		n, ok := steps[s.ID]
		if !ok {
			continue // never made: nothing to close
		}
		if n.Bead.Status == "closed" {
			res.Kept = append(res.Kept, n.Bead.ID)
			continue
		}
		e := workflowtemplate.Evidence{Instance: key, Step: s.ID, Verifier: workflowtemplate.SettledVerifier, PR: pr, Head: h.SHA, Finding: workflowtemplate.SettledFinding(h.SHA, reason)}
		// A rerun after a failed close does not record the same settle twice.
		if !workflowtemplate.EvidenceFor(n, key, s.ID, workflowtemplate.SettledVerifier, pr, h.SHA) {
			if err := bc.AddComment(ctx, dir, n.Bead.ID, e.Comment()); err != nil {
				return res, fmt.Errorf("recording the settle on %s failed (settled so far: %v): %w", n.Bead.ID, res.Settled, err)
			}
		}
		if err := bc.CloseWithReason(ctx, dir, n.Bead.ID, closeReason); err != nil {
			return res, fmt.Errorf("closing %s failed (settled so far: %v); run the same command again: %w", n.Bead.ID, res.Settled, err)
		}
		res.Settled = append(res.Settled, n.Bead.ID)
	}
	if b.Status == "closed" {
		res.BlockClosed = true
		return res, nil
	}
	if err := settleCloseBlock(project, blockID, closeReason); err != nil {
		res.BlockError = err.Error()
		return res, fmt.Errorf("the steps are settled but closing block %s was refused: %w; close it with cmdr internal-close-objective --project %s --bead %s", blockID, err, project, blockID)
	}
	res.BlockClosed = true
	return res, nil
}

// settledTargets refuses unless every open bead the block was plugged before is
// settled as the merge rule reads it (one merged PR, none live), each PR read
// fresh: a target with another live PR still needs its review.
func settledTargets(ctx context.Context, nodes []workflowtemplate.Node, project, repo string, block beads.Bead) error {
	return workflowVerifier().SettledTargets(ctx, nodes, project, repo, block)
}

// settleOrder is the template's steps with every step after the ones it waits
// on, so each close finds its predecessors closed.
func settleOrder(t *workflowtemplate.Template) []workflowtemplate.Step {
	waits := t.Waits()
	done := map[string]bool{}
	var out []workflowtemplate.Step
	for len(out) < len(t.Steps) {
		progressed := false
		for _, s := range t.Steps {
			if done[s.ID] || slices.ContainsFunc(waits[s.ID], func(d string) bool { return !done[d] }) {
				continue
			}
			done[s.ID] = true
			out = append(out, s)
			progressed = true
		}
		if !progressed { // a cycle cannot load; keep the written order for the rest
			for _, s := range t.Steps {
				if !done[s.ID] {
					out = append(out, s)
				}
			}
			break
		}
	}
	return out
}

// runWorkflowSettleBlock: `workflow settle-block --project P --block B --reason R`.
func runWorkflowSettleBlock(args []string, stdout, stderr *os.File) int {
	if callerIsOperator("") || workflowExecutionCaller() {
		emitErr(stderr, "settling a review block belongs to the owning Manager, Commander or the user; Operators cannot settle it")
		return 1
	}
	fs := flag.NewFlagSet("workflow settle-block", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project slug")
	block := fs.String("block", "", "the pr-review block whose PR merged outside the review")
	reason := fs.String("reason", "", "why the review was not completed (15+ characters, recorded on each settled step)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *project == "" || *block == "" || *reason == "" || fs.NArg() != 0 {
		emitErr(stderr, "required: --project P --block B --reason R")
		return 1
	}
	p, err := resolveProjectRecFn(*project)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	unlock := lockPlug("settle-" + *block)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), beadWriteTimeout)
	defer cancel()
	res, err := settleWorkflowBlock(ctx, newBeadsClientFn(), p.Path, p.Name, *block, *reason)
	if res != nil {
		_ = json.NewEncoder(stdout).Encode(res)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	return 0
}
