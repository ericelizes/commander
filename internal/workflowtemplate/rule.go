package workflowtemplate

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/example-user/commander/internal/beads"
)

// Node is the common authoritative/display projection used by the merge rule.
// Head/PRStates are fresh at a write door; a display may omit them and fail closed.
type Node struct {
	Bead     beads.Bead      `json:"bead"`
	Waits    []string        `json:"waits,omitempty"`
	Comments []beads.Comment `json:"comments,omitempty"`
	Head     string          `json:"head,omitempty"` // of TargetPR
	// PRStates is GitHub's state (OPEN, MERGED, CLOSED) of each of TargetPRs; a PR missing here counts as open.
	PRStates map[string]string `json:"pr_states,omitempty"`
	// Steps of a closed block, which the board folds away; the rule still reads them.
	Steps []Node `json:"steps,omitempty"`
	// Blocks this bead waits on that the board folds away because an ancestor of
	// theirs closed; drawn nowhere, the rule still reads them and their Steps.
	Blocks []Node `json:"blocks,omitempty"`
}

func Label(b beads.Bead, prefix string) string {
	for _, l := range b.Labels {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return v
		}
	}
	return ""
}

// The write boundary and workflow rules share one merge-target classifier.
func inPlay(b beads.Bead) bool    { return beads.WorkflowInPlay(b) }
func MergeGate(n Node) bool       { return beads.WorkflowMergeGate(n.Bead) }
func MergeTarget(n Node) bool     { return beads.WorkflowMergeTarget(n.Bead) }
func Guardable(b beads.Bead) bool { return beads.WorkflowGuardable(b) }

// IsReviewBlock is a plugged pr-review container (not one of its steps).
func IsReviewBlock(b beads.Bead) bool {
	return Label(b, LabelTemplate) == "pr-review" && Label(b, LabelStep) == ""
}

// PluggedBefore is whether block was plugged in front of target: its instance key
// binds the two. A bead merely downstream of another target's block is not guarded.
func PluggedBefore(block beads.Bead, target, project string) bool {
	return IsReviewBlock(block) && Label(block, LabelInstance) == Key(Params{Project: project, Parent: block.ParentID, Before: target, PR: BlockPR(block)})
}

func pluggedBlocks(n Node, byID map[string]Node, project string) []Node {
	var out []Node
	for _, id := range n.Waits {
		if b, ok := byID[id]; ok && PluggedBefore(b.Bead, n.Bead.ID, project) {
			out = append(out, b)
		}
	}
	return out
}

// BlockPluggedBefore reads only b's edges, so an ordinary close never lists the
// project. A failed read is an error, never "no block".
func BlockPluggedBefore(ctx context.Context, bc beads.Client, dir, project string, b beads.Bead) (bool, error) {
	blocks, err := BlocksPluggedBefore(ctx, bc, dir, project, b)
	return len(blocks) > 0, err
}

// BlocksPluggedBefore is every review block plugged in front of b, read like BlockPluggedBefore.
func BlocksPluggedBefore(ctx context.Context, bc beads.Client, dir, project string, b beads.Bead) ([]beads.Bead, error) {
	if !inPlay(b) {
		return nil, nil
	}
	deps, err := bc.Deps(ctx, dir, b.ID)
	if err != nil {
		return nil, err
	}
	var out []beads.Bead
	for _, d := range deps {
		if d.Type != "blocks" {
			continue
		}
		block, err := pluggedBlock(ctx, bc, dir, project, d.DependsOnID, b.ID)
		if err != nil {
			return nil, err
		}
		if block != nil {
			out = append(out, *block)
		}
	}
	return out, nil
}

// IsPluggedBlock reads whether blockID is the review block plugged before target.
func IsPluggedBlock(ctx context.Context, bc beads.Client, dir, project, blockID, target string) (bool, error) {
	block, err := pluggedBlock(ctx, bc, dir, project, blockID, target)
	return block != nil, err
}

func pluggedBlock(ctx context.Context, bc beads.Client, dir, project, blockID, target string) (*beads.Bead, error) {
	o, err := beads.FreshShow(ctx, bc, dir, blockID)
	if err != nil {
		return nil, err
	}
	if o == nil || !IsReviewBlock(*o) {
		return nil, nil
	}
	details, err := beads.FreshDetails(ctx, bc, dir, []string{o.ID})
	if err != nil {
		return nil, err
	}
	o.ParentID = details[o.ID].Parent
	if !PluggedBefore(*o, target, project) {
		return nil, nil
	}
	return o, nil
}

// Candidate is a bead the merge rule judges: a merge target or one a block guards.
func Candidate(n Node, byID map[string]Node, project string) bool {
	return MergeTarget(n) || (inPlay(n.Bead) && len(pluggedBlocks(n, byID, project)) > 0)
}

var prInDescription = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[1-9][0-9]*`)

// LabelPRs resolves every pr: label commander writes: pr:N through the project's
// repo, pr:owner/name#N, and a full URL. --review adds a label; only --review none removes them.
func LabelPRs(b beads.Bead, repo string) []string {
	var out []string
	for _, l := range b.Labels {
		ref, ok := strings.CutPrefix(l, "pr:")
		if !ok {
			continue
		}
		if pr, err := CanonicalPR(ref); err == nil {
			out = append(out, pr)
			continue
		}
		r, num := repo, ref
		if owner, n, ok := strings.Cut(ref, "#"); ok && strings.Contains(owner, "/") {
			r, num = owner, n
		}
		if r == "" {
			continue
		}
		if pr, err := CanonicalPR("https://github.com/" + r + "/pull/" + num); err == nil {
			out = append(out, pr)
		}
	}
	return out
}

// BlockPR is the PR a plugged block is about: plug records it in the description,
// and the instance key binds it. No other bead's description names a PR.
func BlockPR(block beads.Bead) string {
	pr, err := CanonicalPR(prInDescription.FindString(block.Description))
	if err != nil {
		return ""
	}
	return pr
}

// stageOf is the stage a gate bead guards ("" for anything else).
func stageOf(b beads.Bead) string { return beads.UnescapeLabel(Label(b, "gate-of:")) }

// TargetPRs is every PR a merge target is judged on, the one reading door and graph
// check share: the block plugged before it, then each pr: label of the target and
// of the stage a gate guards. Text in a description or a gate check never counts.
func TargetPRs(n Node, byID map[string]Node, project, repo string) []string {
	var prs []string
	for _, b := range pluggedBlocks(n, byID, project) {
		prs = append(prs, BlockPR(b.Bead))
	}
	prs = append(prs, LabelPRs(n.Bead, repo)...)
	prs = append(prs, LabelPRs(byID[stageOf(n.Bead)].Bead, repo)...)
	seen := map[string]bool{"": true}
	return slices.DeleteFunc(prs, func(pr string) bool {
		dup := seen[pr]
		seen[pr] = true
		return dup
	})
}

// TargetPR is the PR a target's head and printed commands are about: the first of
// TargetPRs, else a review block it waits on that is not plugged for it.
func TargetPR(n Node, byID map[string]Node, project, repo string) string {
	if prs := TargetPRs(n, byID, project, repo); len(prs) > 0 {
		return prs[0]
	}
	for _, id := range n.Waits {
		if b, ok := byID[id]; ok && IsReviewBlock(b.Bead) {
			return BlockPR(b.Bead)
		}
	}
	return ""
}

// prLabel is the target's or its guarded stage's first pr: label, resolvable or not.
func prLabel(n Node, byID map[string]Node) string {
	for _, b := range []beads.Bead{n.Bead, byID[stageOf(n.Bead)].Bead} {
		for _, l := range b.Labels {
			if strings.HasPrefix(l, "pr:") {
				return l
			}
		}
	}
	return ""
}

// Settled is whether nothing is left to review: at least one PR merged and none
// live. An abandoned (closed, unmerged) PR needs no block, but alone it is no merge.
func Settled(prs []string, states map[string]string) bool {
	merged := false
	for _, pr := range prs {
		switch states[pr] {
		case "MERGED":
			merged = true
		case "CLOSED":
		default:
			return false
		}
	}
	return merged
}

func Definition(b beads.Bead) (*Template, error) {
	_, raw, ok := strings.Cut(b.Description, "\n\n")
	if !ok {
		return nil, fmt.Errorf("%s has no template snapshot", b.ID)
	}
	t, err := Parse([]byte(raw))
	if err != nil {
		return nil, err
	}
	if Label(b, LabelTemplate) != t.ID || Label(b, LabelHash) != t.Hash() || Label(b, LabelVersion) != fmt.Sprint(t.Version) {
		return nil, fmt.Errorf("%s template identity differs from snapshot", b.ID)
	}
	return t, nil
}
func EvidenceFor(n Node, instance, step, verifier, pr, head string) bool {
	for i := len(n.Comments) - 1; i >= 0; i-- {
		e, ok := ParseEvidence(n.Comments[i].Text)
		if ok && e.Instance == instance && e.Step == step && e.Verifier == verifier && e.PR == pr && e.Head == head && head != "" {
			return true
		}
	}
	return false
}

type Finding struct {
	Check, Target, Block, Text, Fix string
	Stale                           []string
	Advisory                        bool
	// NoPR: the target has no pr: label (nor its stage) and no review block, the
	// one finding an approved no-PR ship may waive (internal-gate --no-pr).
	NoPR bool
}

// MergeRule checks topology, exact snapshot steps and current-head evidence.
// Generated steps are never merge targets themselves (no recursion).
func MergeRule(project, repo string, nodes []Node, scope map[string]bool) []Finding {
	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	var out []Finding
	for _, m := range nodes {
		if scope != nil && !scope[m.Bead.ID] {
			continue
		}
		if !Candidate(m, byID, project) {
			continue
		}
		prs := TargetPRs(m, byID, project, repo)
		pr := TargetPR(m, byID, project, repo)
		label := prLabel(m, byID)
		if pr == "" && label == "" {
			out = append(out, Finding{Check: "MERGE_REVIEW_REQUIRED", Target: m.Bead.ID, Advisory: true, NoPR: true,
				Text: fmt.Sprintf("MERGE_REVIEW_REQUIRED: %s has no PR yet; add its pr: label and plug the pr-review block when the PR exists", m.Bead.ID),
				Fix:  "Once the PR exists, add its pr: label and plug the pr-review block before merging"})
			continue
		}
		if Settled(prs, m.PRStates) {
			continue
		}
		var block *Node
		blocks := pluggedBlocks(m, byID, project)
		if len(blocks) == 1 {
			block = &blocks[0]
		}
		covered := ""
		if block != nil {
			covered = BlockPR(block.Bead)
			pr = covered
		}
		// One block reviews one PR: any other live PR needs its own.
		uncovered := ""
		for _, p := range prs {
			if s := m.PRStates[p]; s != "MERGED" && s != "CLOSED" && p != covered && uncovered == "" {
				uncovered = p
			}
		}
		parent := m.Bead.ParentID
		if parent == "" {
			parent = stageOf(m.Bead)
		}
		if block != nil {
			parent = block.Bead.ParentID // Its key has this parent: plug finishes it, any other is a second block.
		}
		if parent == "" {
			parent = "<B>"
		}
		plugPR := pr
		if uncovered != "" {
			plugPR = uncovered
		}
		if plugPR == "" && label != "" {
			plugPR = "<URL of " + label + ">" // A label no repo resolves: known, but no URL to print.
		}
		if plugPR == "" {
			plugPR = "<URL>"
		}
		fix := fmt.Sprintf("cmdr workflow plug pr-review --project %s --parent %s --before %s --pr %s", project, parent, m.Bead.ID, plugPR)
		text := fmt.Sprintf("MERGE_REVIEW_REQUIRED: %s needs the pr-review template before it. Fix: %s", m.Bead.ID, fix)
		if other := otherBlock(m, byID, Key(Params{Project: project, Parent: parent, Before: m.Bead.ID, PR: plugPR})); other != "" {
			fix = refusedPlugFix(m, byID, project, repo, other, plugPR)
			text = fmt.Sprintf("MERGE_REVIEW_REQUIRED: %s has no pr-review block for %s, and plug refuses a second block while it waits on %s, a block for another PR or bead. Fix: %s", m.Bead.ID, plugPR, other, fix)
		}
		if !MergeGate(m) && len(blocks) == 0 {
			// Matched by its title alone: say how to undo a wrong match.
			text += fmt.Sprintf(". If %s is not the merge of its PR, retitle it without the word merge: cmdr internal-update --project %s --bead %s --title '<title>'", m.Bead.ID, project, m.Bead.ID)
		}
		required := func() {
			out = append(out, Finding{Check: "MERGE_REVIEW_REQUIRED", Target: m.Bead.ID, Text: text, Fix: fix})
		}
		if block == nil || uncovered != "" {
			required()
			continue
		}
		t, err := Definition(block.Bead)
		if err != nil {
			required()
			continue
		}
		key := Label(block.Bead, LabelInstance)
		var stale []string
		plugNeeded := false
		steps := map[string]Node{}
		for _, n := range nodes {
			if Label(n.Bead, LabelInstance) == key && Label(n.Bead, LabelStep) != "" {
				id := Label(n.Bead, LabelStep)
				if _, ok := steps[id]; ok {
					stale = append(stale, n.Bead.ID)
				}
				steps[id] = n
			}
		}
		head := m.Head
		if head == "" {
			head = block.Head
		}
		for _, s := range t.Steps {
			n, ok := steps[s.ID]
			if !ok {
				stale = append(stale, s.ID)
				plugNeeded = true
				continue
			}
			bad := !CheckCurrent(n, *block, t, s.ID, s.Verifier, pr, head)
			if bad {
				stale = append(stale, n.Bead.ID)
			}
			delete(steps, s.ID) // edges below use all nodes, so resolve separately
		}
		// Resolve ordering independently of the map consumed above.
		ids := map[string]string{}
		for _, n := range nodes {
			if Label(n.Bead, LabelInstance) == key {
				ids[Label(n.Bead, LabelStep)] = n.Bead.ID
			}
		}
		for _, s := range t.Steps {
			n, ok := byID[ids[s.ID]]
			if !ok {
				continue // already listed by its step id
			}
			for _, dep := range t.Waits()[s.ID] {
				if !slices.Contains(n.Waits, ids[dep]) {
					plugNeeded = true
					if !slices.Contains(stale, n.Bead.ID) {
						stale = append(stale, n.Bead.ID)
					}
				}
			}
		}
		for _, n := range steps {
			stale = append(stale, n.Bead.ID)
		}
		if block.Bead.Status != "closed" {
			stale = append(stale, block.Bead.ID)
		}
		slices.Sort(stale)
		stale = slices.Compact(stale)
		if len(stale) > 0 {
			renew := renewCommands(project, t, ids, stale, block.Bead)
			binding, bindingErr := Binding(block.Comments)
			if bindingErr != nil || binding.PR != pr || binding.Head != head {
				var old *WriterBinding
				if bindingErr == nil {
					old = &binding
				}
				renew = append([]string{RebindCommand(project, block.Bead.ID, old, "")}, renew...)
			}
			if plugNeeded {
				renew = append([]string{fix}, renew...)
			}
			fix = strings.Join(renew, " ; ")
			current := head
			if current == "" {
				current = "the current PR head"
			}
			out = append(out, Finding{Check: "MERGE_REVIEW_INCOMPLETE", Target: m.Bead.ID, Block: block.Bead.ID, Stale: stale, Fix: fix, Text: fmt.Sprintf("MERGE_REVIEW_INCOMPLETE: %s is blocked by %s; unfinished or stale: %s. Fix, in this order (closing a closed step again reruns its fresh check; a proof names a closed separate review of %s and the commits that fixed its findings; only if that review reports no findings, use %s instead of fix_commits): %s", m.Bead.ID, block.Bead.ID, strings.Join(stale, ", "), current, noFindingsProof, fix)})
		}
	}
	return out
}

// otherBlock is what makes plug refuse n for the instance key: a wait on another
// pr-review instance (cmd/cmdr/workflow.go, "refusing a second block").
func otherBlock(n Node, byID map[string]Node, key string) string {
	for _, id := range n.Waits {
		if b := byID[id].Bead; Label(b, LabelTemplate) == "pr-review" && Label(b, LabelInstance) != key {
			return id
		}
	}
	return ""
}

// refusedPlugFix is the way out when plug would refuse: pr is either a mistaken
// label, cleared where it sits, or a second live PR, which waits its turn.
func refusedPlugFix(n Node, byID map[string]Node, project, repo, other, pr string) string {
	for _, b := range []beads.Bead{n.Bead, byID[stageOf(n.Bead)].Bead} {
		if slices.Contains(LabelPRs(b, repo), pr) {
			return fmt.Sprintf("if the pr: label on %s is a mistake, clear it: cmdr internal-set-objective --project %s --bead %s --review none --reason '<why>'. If it is right, this reads clear once %s merges or closes", b.ID, project, b.ID, pr)
		}
	}
	return fmt.Sprintf("work out why %s waits on %s; no plug can be added while it does", n.Bead.ID, other)
}

// noFindingsProof is the alternative to fix_commits, for a review that says it found nothing.
const noFindingsProof = `"no_findings":true,"fix_commits":[]`

// renewCommands are the commands that finish or renew the stale steps, in template
// order; the block, when still open, closes last. A proof's values are placeholders:
// only the review can say which head it read and whether it found anything.
func renewCommands(project string, t *Template, ids map[string]string, stale []string, block beads.Bead) []string {
	var cmds []string
	closeBead := func(id string) {
		cmds = append(cmds, fmt.Sprintf("cmdr internal-close-objective --project %s --bead %s", project, id))
	}
	for _, s := range t.Steps {
		id := ids[s.ID]
		if id == "" {
			id = "<" + s.ID + "-bead-from-plug>" // plug makes it and prints its id
		} else if !slices.Contains(stale, id) {
			continue
		}
		if needsProof(s.Verifier) {
			cmds = append(cmds, fmt.Sprintf(`cmdr internal-comment --project %s --bead %s --text '%s{"review_bead":"<separate-review-task>","head":"<full-sha>","fix_commits":["<full-fix-sha>"]}'`, project, id, ProofPrefix))
		}
		closeBead(id)
	}
	if slices.Contains(stale, block.ID) {
		closeBead(block.ID)
	}
	return cmds
}
