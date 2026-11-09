package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/agentrt"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/reviewrun"
	"github.com/example-user/commander/internal/workflowtemplate"
)

var workflowGH workflowtemplate.GHRunner = workflowtemplate.RunGH

// workflowRepo is the project's GitHub repo, the one the display resolves bare pr:N labels through.
var workflowRepo = livedata.GitHubRepo

// workflowFreshDetails bypasses display caches for completion checks.
var workflowFreshDetails = beads.FreshDetails

// workflowNodes reads what a close of id is judged on and nothing else, fresh, so
// its cost follows the bead's own edges and never the size of the project. A failed
// read or a short answer refuses the close; only a bead confirmed gone is no node.
func workflowNodes(ctx context.Context, bc beads.Client, dir, id string) ([]workflowtemplate.Node, error) {
	me, err := bc.List(ctx, dir, beads.ListOptions{Status: "all", IDs: []string{id}})
	if err != nil {
		return nil, err
	}
	if len(me) == 0 {
		return nil, nil
	}
	// A generated step is judged only against its own snapshotted block.
	if workflowtemplate.Label(me[0], workflowtemplate.LabelStep) != "" {
		instance := workflowtemplate.Label(me[0], workflowtemplate.LabelInstance)
		if instance == "" {
			return nil, fmt.Errorf("workflow step %s has no block instance", id)
		}
		nodes := map[string]workflowtemplate.Node{}
		if err := readInstance(ctx, bc, dir, instance, nodes); err != nil {
			return nil, err
		}
		return sortedWorkflowNodes(nodes), nil
	}
	nodes := map[string]workflowtemplate.Node{}
	if err := readWorkflowNodes(ctx, bc, dir, me, nodes); err != nil {
		return nil, err
	}
	// A target is judged itself; a block's container on the targets that wait on it.
	judged := []string{id}
	if workflowtemplate.Label(me[0], workflowtemplate.LabelTemplate) != "" {
		edges, err := bc.Dependents(ctx, dir, id)
		if err != nil {
			return nil, err
		}
		judged = nil
		for _, e := range edges {
			if e.Type == "blocks" {
				judged = append(judged, e.IssueID)
			}
		}
		if err := readWorkflowIDs(ctx, bc, dir, judged, nodes); err != nil {
			return nil, err
		}
	}
	// Each judged bead is read through its waits (blocks plugged before it) and the
	// stage a gate guards (whose pr: labels count too).
	var related []string
	for _, j := range judged {
		n, ok := nodes[j]
		if !ok {
			continue
		}
		related = append(related, n.Waits...)
		if stage := beads.UnescapeLabel(workflowtemplate.Label(n.Bead, "gate-of:")); stage != "" {
			related = append(related, stage)
		}
	}
	if err := readWorkflowIDs(ctx, bc, dir, related, nodes); err != nil {
		return nil, err
	}
	// Every instance among those, with all its steps, evidence and edges.
	instances := map[string]bool{}
	for _, n := range nodes {
		if key := workflowtemplate.Label(n.Bead, workflowtemplate.LabelInstance); key != "" {
			instances[key] = true
		}
	}
	for key := range instances {
		if err := readInstance(ctx, bc, dir, key, nodes); err != nil {
			return nil, err
		}
	}
	return sortedWorkflowNodes(nodes), nil
}

// sortedWorkflowNodes keeps the rule's findings in one order from run to run.
func sortedWorkflowNodes(nodes map[string]workflowtemplate.Node) []workflowtemplate.Node {
	return slices.SortedFunc(maps.Values(nodes), func(a, b workflowtemplate.Node) int {
		return strings.Compare(a.Bead.ID, b.Bead.ID)
	})
}

func readInstance(ctx context.Context, bc beads.Client, dir, instance string, into map[string]workflowtemplate.Node) error {
	members, err := bc.List(ctx, dir, beads.ListOptions{Status: "all", Labels: []string{workflowtemplate.LabelInstance + instance}})
	if err != nil {
		return err
	}
	return readWorkflowNodes(ctx, bc, dir, members, into)
}

// readWorkflowIDs reads the named beads not read yet. Never lists with no ids: that is the whole project.
func readWorkflowIDs(ctx context.Context, bc beads.Client, dir string, ids []string, into map[string]workflowtemplate.Node) error {
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		_, read := into[id]
		return read || id == ""
	})
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return nil
	}
	bs, err := bc.List(ctx, dir, beads.ListOptions{Status: "all", IDs: ids})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if slices.ContainsFunc(bs, func(b beads.Bead) bool { return b.ID == id }) {
			continue
		}
		// Exit 0 with an id left out is a short read until the bead's own read says
		// it is gone (a dangling wait or stage is no node, as in a whole-project list).
		b, err := beads.FreshShow(ctx, bc, dir, id)
		if errors.Is(err, beads.ErrNotFound) || (err == nil && b == nil) {
			continue
		}
		if err != nil {
			return fmt.Errorf("the list of %v left out %s and reading it failed: %w", ids, id, err)
		}
		return fmt.Errorf("the list of %v came back short: it left out %s, which exists. Nothing was closed; check COMMANDER_BEADS_BIN (bd takes --id a,b, br a repeated --id) and retry", ids, id)
	}
	return readWorkflowNodes(ctx, bc, dir, bs, into)
}

// readWorkflowNodes adds the fresh edges and comments of bs; a bead left without them is an error.
func readWorkflowNodes(ctx context.Context, bc beads.Client, dir string, bs []beads.Bead, into map[string]workflowtemplate.Node) error {
	bs = slices.DeleteFunc(slices.Clone(bs), func(b beads.Bead) bool {
		_, read := into[b.ID]
		return read
	})
	if len(bs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(bs))
	for _, b := range bs {
		ids = append(ids, b.ID)
	}
	details, err := workflowFreshDetails(ctx, bc, dir, ids)
	if err != nil {
		return err
	}
	for _, b := range bs {
		d, ok := details[b.ID]
		if !ok {
			return fmt.Errorf("missing workflow details for %s", b.ID)
		}
		into[b.ID] = workflowNode(b, d)
	}
	return nil
}

func workflowNode(b beads.Bead, d beads.Detail) workflowtemplate.Node {
	b.ParentID = d.Parent
	n := workflowtemplate.Node{Bead: b, Comments: d.Comments}
	for _, dep := range d.Deps {
		if dep.Type == "blocks" {
			n.Waits = append(n.Waits, dep.DependsOnID)
		}
	}
	return n
}

// checkWorkflowClose is shared by every completion door. The bead and all it is
// judged on (edges, comments, status) are read fresh, never from the display's
// export; evidence is recorded before the close and never inferred from closed status.
func checkWorkflowClose(ctx context.Context, bc beads.Client, dir, project, id string) error {
	bc = freshReader(bc)
	var seen settleSeen
	err := checkWorkflowCloseOnce(ctx, bc, dir, project, id, &seen)
	if err == nil || seen.pr == "" {
		return err
	}
	return settleHint(ctx, project, seen, err)
}

// settleSeen is the block a refused step belongs to, once the close read it.
type settleSeen struct{ block, pr string }

// settleHint names settle-block when a refused step's block PR has merged: no
// check of a merged PR can pass anymore, so only the owning Manager's settle is
// left. It reads GitHub only (the block was already read); a failed read leaves
// the refusal as it was.
func settleHint(ctx context.Context, project string, seen settleSeen, refused error) error {
	if h, err := workflowtemplate.ReadHead(ctx, workflowGH, seen.pr); err != nil || h.State != "MERGED" {
		return refused
	}
	return fmt.Errorf("%w. %s merged outside the review: if it will not be reviewed, the owning Manager settles the block (closes its open steps as settled, not reviewed, keeping earlier checks): cmdr workflow settle-block --project %s --block %s --reason '<why the review was not completed>'", refused, seen.pr, project, seen.block)
}

func checkWorkflowCloseOnce(ctx context.Context, bc beads.Client, dir, project, id string, seen *settleSeen) error {
	b, err := bc.Show(ctx, dir, id)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("cannot verify missing bead %s", id)
	}
	target := workflowtemplate.Node{Bead: *b}
	step := workflowtemplate.Label(*b, workflowtemplate.LabelStep)
	managed := workflowtemplate.Label(*b, workflowtemplate.LabelTemplate) != ""
	repo := workflowRepo(dir)
	if !managed && !workflowtemplate.MergeTarget(target) {
		held, err := workflowtemplate.BlockPluggedBefore(ctx, bc, dir, project, *b)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
	}
	nodes, err := workflowNodes(ctx, bc, dir, id)
	if err != nil {
		return err
	}
	byID := map[string]workflowtemplate.Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	n, ok := byID[id]
	if !ok {
		return fmt.Errorf("workflow bead %s missing from fresh list", id)
	}
	if managed && step != "" {
		block, ok := byID[n.Bead.ParentID]
		if !ok {
			return fmt.Errorf("workflow step %s has no block", id)
		}
		if workflowtemplate.IsReviewBlock(block.Bead) {
			seen.block, seen.pr = block.Bead.ID, workflowtemplate.BlockPR(block.Bead)
		}
		tpl, err := workflowtemplate.Definition(block.Bead)
		if err != nil {
			return err
		}
		binding, err := workflowtemplate.Binding(block.Comments)
		if err != nil {
			return err
		}
		if binding.PR != workflowtemplate.BlockPR(block.Bead) {
			return fmt.Errorf("writer binding PR differs from block; ask the owning Manager to run cmdr workflow bind-writers --project %s --block %s --writers <original-task,actual-fix-task> --head <current-full-sha>", project, block.Bead.ID)
		}
		instance := workflowtemplate.Label(block.Bead, workflowtemplate.LabelInstance)
		if instance == "" || workflowtemplate.Label(*b, workflowtemplate.LabelInstance) != instance || workflowtemplate.Label(*b, workflowtemplate.LabelHash) != tpl.Hash() {
			return fmt.Errorf("workflow step identity does not match block")
		}
		var verifier string
		for _, s := range tpl.Steps {
			if s.ID == step {
				verifier = s.Verifier
			}
		}
		if verifier == "" {
			return fmt.Errorf("unknown workflow step %s", step)
		}
		for _, dep := range tpl.Waits()[step] {
			found := false
			for _, other := range nodes {
				if workflowtemplate.Label(other.Bead, workflowtemplate.LabelInstance) == instance && workflowtemplate.Label(other.Bead, workflowtemplate.LabelStep) == dep {
					for _, wait := range n.Waits {
						if wait == other.Bead.ID && other.Bead.Status == "closed" {
							found = true
						}
					}
				}
			}
			if !found {
				return fmt.Errorf("workflow step %s still needs closed predecessor %s", id, dep)
			}
		}
		// The newest well-formed proof counts. A malformed one cannot be deleted, so it must not block the step for good.
		var proof *workflowtemplate.Proof
		ignored := ""
		for _, c := range n.Comments {
			if raw, ok := strings.CutPrefix(c.Text, workflowtemplate.ProofPrefix); ok {
				var p workflowtemplate.Proof
				if err := json.Unmarshal([]byte(raw), &p); err != nil {
					ignored = fmt.Sprintf("ignored a malformed review proof on %s (%v)", id, err)
					continue
				}
				if p.ReviewBead == "" || p.Head == "" {
					ignored = fmt.Sprintf("ignored a review proof on %s with no review_bead or head", id)
					continue
				}
				proof = &p
			}
		}
		if ignored != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", ignored)
		}
		reader := func(ctx context.Context, reviewID string) (workflowtemplate.Review, error) {
			r, err := bc.Show(ctx, dir, reviewID)
			if err != nil {
				return workflowtemplate.Review{}, err
			}
			if r == nil || r.ID == id {
				return workflowtemplate.Review{}, fmt.Errorf("independent review bead required")
			}
			root, err := workflowRoot(ctx, bc, dir, block.Bead.ID)
			if err != nil {
				return workflowtemplate.Review{}, err
			}
			roles, err := workflowRolesInScope(ctx, bc, dir, project, root, reviewID)
			if err != nil {
				return workflowtemplate.Review{}, err
			}
			for _, task := range binding.Tasks {
				writers, err := workflowRolesInScope(ctx, bc, dir, project, root, task)
				if err != nil {
					return workflowtemplate.Review{}, err
				}
				if roleOverlap(roles, writers) {
					return workflowtemplate.Review{}, fmt.Errorf("reviewer must differ from writer task %s", task)
				}
			}
			if strings.Contains(step, "adversarial") {
				found := false
				for _, s := range nodes {
					if workflowtemplate.Label(s.Bead, workflowtemplate.LabelInstance) != instance || workflowtemplate.Label(s.Bead, workflowtemplate.LabelStep) != "review" {
						continue
					}
					for _, c := range s.Comments {
						e, ok := workflowtemplate.ParseEvidence(c.Text)
						if !ok || e.Binding != binding.Revision || e.Head != binding.Head || e.PR != binding.PR || e.Proof == nil {
							continue
						}
						first, err := workflowRolesInScope(ctx, bc, dir, project, root, e.Proof.ReviewBead)
						if err != nil {
							return workflowtemplate.Review{}, err
						}
						found = true
						if roleOverlap(roles, first) {
							return workflowtemplate.Review{}, fmt.Errorf("adversarial reviewer must differ from first reviewer")
						}
					}
				}
				if !found {
					return workflowtemplate.Review{}, fmt.Errorf("current primary review evidence required")
				}
			}
			cs, err := bc.Comments(ctx, dir, reviewID)
			if err != nil {
				return workflowtemplate.Review{}, err
			}
			review := workflowtemplate.Review{ID: r.ID, Status: r.Status, Owner: strings.Join(roles, ",")}
			for _, c := range cs {
				review.Comments = append(review.Comments, c.Text)
			}
			if err = loadReviewResults(ctx, project, reviewID, &review); err != nil {
				return workflowtemplate.Review{}, err
			}
			review.ValidateAddendum = func(ctx context.Context, h workflowtemplate.Head, p *workflowtemplate.Proof) error {
				return resolveWorkflowAddendum(ctx, bc, dir, project, block.Bead, id, binding, h, p, review)
			}
			return review, nil
		}
		e, err := workflowtemplate.Verify(ctx, workflowGH, reader, verifier, workflowtemplate.BlockPR(block.Bead), instance, step, proof)
		if err != nil {
			if ignored != "" {
				return fmt.Errorf("%w (%s)", err, ignored)
			}
			return err
		}
		if e.Head != binding.Head {
			return fmt.Errorf("stale writer binding: ask the owning Manager to run cmdr workflow bind-writers --project %s --block %s --writers <original-task,actual-fix-task> --head %s", project, block.Bead.ID, e.Head)
		}
		e.Binding = binding.Revision
		if verifier == "head-current" {
			for _, other := range nodes {
				if workflowtemplate.Label(other.Bead, workflowtemplate.LabelInstance) != instance {
					continue
				}
				sid := workflowtemplate.Label(other.Bead, workflowtemplate.LabelStep)
				if sid == "" || sid == step {
					continue
				}
				v := ""
				for _, s := range tpl.Steps {
					if s.ID == sid {
						v = s.Verifier
					}
				}
				if other.Bead.Status != "closed" || !workflowtemplate.BoundEvidenceFor(other, block, instance, sid, v, e.PR, e.Head) {
					return fmt.Errorf("final check: %s has unfinished or stale evidence for PR head %s", other.Bead.ID, e.Head)
				}
			}
		}
		return bc.AddComment(ctx, dir, id, e.Comment())
	}
	// A container closes only after its entire block passes the same merge rule.
	scope := map[string]bool{id: true}
	if managed {
		scope = map[string]bool{}
		for _, m := range nodes {
			for _, dep := range m.Waits {
				if dep == id {
					scope[m.Bead.ID] = true
				}
			}
		}
		if len(scope) == 0 {
			return fmt.Errorf("workflow block has no guarded merge target")
		}
		for i := range nodes {
			if nodes[i].Bead.ID == id {
				nodes[i].Bead.Status = "closed"
			}
		}
	}
	byID = map[string]workflowtemplate.Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	prs := map[string]string{}
	for i := range nodes {
		if !scope[nodes[i].Bead.ID] {
			continue
		}
		pr := workflowtemplate.TargetPR(nodes[i], byID, project, repo)
		if pr == "" {
			continue
		}
		prs[nodes[i].Bead.ID] = pr
		nodes[i].PRStates = map[string]string{}
		for _, other := range append([]string{pr}, workflowtemplate.TargetPRs(nodes[i], byID, project, repo)...) {
			if _, read := nodes[i].PRStates[other]; read {
				continue
			}
			h, err := workflowtemplate.ReadHead(ctx, workflowGH, other)
			if err != nil {
				return err
			}
			nodes[i].PRStates[other] = h.State
			if other == pr {
				nodes[i].Head = h.SHA
			}
		}
	}
	findings := workflowtemplate.MergeRule(project, repo, nodes, scope)
	if len(findings) > 0 {
		return fmt.Errorf("%s", findings[0].Text)
	}
	for _, n := range nodes {
		if !scope[n.Bead.ID] || n.Head == "" {
			continue
		}
		pr := prs[n.Bead.ID]
		h, err := workflowtemplate.ReadHead(ctx, workflowGH, pr)
		if err != nil {
			return err
		}
		if h.SHA != n.Head {
			return fmt.Errorf("PR head changed during merge review check; retry")
		}
		finding := "pr-review topology and current-head evidence checked"
		if workflowtemplate.Settled(workflowtemplate.TargetPRs(n, byID, project, repo), n.PRStates) {
			finding = "PR merged: the review block was not checked"
		}
		e := workflowtemplate.Evidence{Instance: workflowtemplate.Label(byID[id].Bead, workflowtemplate.LabelInstance), Step: "merge-rule", Verifier: "merge-review", PR: pr, Head: h.SHA, Finding: finding}
		if err := bc.AddComment(ctx, dir, id, e.Comment()); err != nil {
			return err
		}
	}
	return nil
}

// loadReviewResults reads what judging a review's typed results needs: the task's
// actual runs and the original launch record each result names.
func loadReviewResults(ctx context.Context, project, task string, review *workflowtemplate.Review) error {
	var err error
	for _, c := range review.Comments {
		if _, present, _ := workflowtemplate.ParseReviewResult(c); present {
			review.Runs, err = workflowResultRuns(ctx, project, task)
			if err != nil {
				return err
			}
			break
		}
	}
	review.Recorded = make(map[string]reviewrun.Recorded)
	for _, c := range review.Comments {
		if result, present, parseErr := workflowtemplate.ParseReviewResult(c); present {
			if parseErr == nil {
				if original, loadErr := reviewrun.ReadRecorded(filepath.Join(agentrt.CommanderHome(), "review-executions"), result.RunID, result.ManifestHash); loadErr == nil {
					review.Recorded[result.ManifestHash] = original
				}
			}
		}
	}
	return nil
}
