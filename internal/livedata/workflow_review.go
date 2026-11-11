package livedata

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/workflowtemplate"
)

func workflowNode(b beads.Bead, d beads.Detail, reviewBlocks map[string]bool) *workflowtemplate.Node {
	n := workflowtemplate.Node{Bead: b}
	n.Bead.ParentID = d.Parent
	for _, dep := range d.Deps {
		if dep.Type == "blocks" {
			n.Waits = append(n.Waits, dep.DependsOnID)
		}
	}
	guarded := workflowtemplate.Guardable(b) && slices.ContainsFunc(n.Waits, func(id string) bool { return reviewBlocks[id] })
	if workflowtemplate.Label(b, workflowtemplate.LabelTemplate) == "" && workflowtemplate.Label(b, workflowtemplate.LabelStep) == "" && !workflowtemplate.MergeTarget(n) && !guarded {
		return nil
	}
	for _, c := range d.Comments {
		if strings.HasPrefix(c.Text, workflowtemplate.EvidencePrefix) || strings.HasPrefix(c.Text, workflowtemplate.BindingPrefix) {
			n.Comments = append(n.Comments, c)
		}
	}
	return &n
}

// attachFoldedSteps keeps a closed block's steps on the block's node: the board
// folds them away (dropOrphans), and its target is still judged by their evidence.
func attachFoldedSteps(rows []ObjectiveRow, all []beads.Bead, details map[string]beads.Detail, reviewBlocks map[string]bool) {
	for i := range rows {
		block := rows[i].WorkflowCheck
		if block == nil || !reviewBlocks[rows[i].BeadID] || rows[i].Status != "closed" {
			continue
		}
		block.Steps = blockSteps(rows[i].BeadID, all, details)
	}
}

// blockSteps is the step nodes of the block id, with their evidence and edges.
func blockSteps(id string, all []beads.Bead, details map[string]beads.Detail) []workflowtemplate.Node {
	var steps []workflowtemplate.Node
	for _, b := range all {
		if workflowtemplate.Label(b, workflowtemplate.LabelStep) != "" && parentOf(details, b.ID) == id {
			if n := workflowNode(b, details[b.ID], nil); n != nil {
				steps = append(steps, *n)
			}
		}
	}
	return steps
}

// attachFoldedBlocks keeps a review block its parent's closing folded off the board
// (foldedUnderClosed) on each bead that waits on it. It draws nothing: the merge rule
// reads it, as a door reads it fresh. Run on the final rows and their gate checks.
func attachFoldedBlocks(rows []ObjectiveRow, all []beads.Bead, details map[string]beads.Detail, reviewBlocks map[string]bool) {
	drawn := make(map[string]bool, len(rows))
	for _, r := range rows {
		drawn[r.BeadID] = true
	}
	attach := func(n *workflowtemplate.Node) {
		for _, id := range n.Waits {
			if !reviewBlocks[id] || drawn[id] {
				continue
			}
			for _, b := range all {
				if b.ID != id {
					continue
				}
				if block := workflowNode(b, details[id], nil); block != nil {
					block.Steps = blockSteps(id, all, details)
					n.Blocks = append(n.Blocks, *block)
				}
			}
		}
	}
	for i := range rows {
		if n := rows[i].WorkflowCheck; n != nil && workflowtemplate.Guardable(n.Bead) {
			attach(n)
		}
		for g := range rows[i].GateChecks {
			attach(&rows[i].GateChecks[g])
		}
	}
}

// attachMergeGates puts each stage's open merge gates on its row: gate beads are
// not steps. A failed read shows none, as attachGates does; doors read fresh.
func attachMergeGates(ctx context.Context, bc beads.Client, dir string, rows []ObjectiveRow, reviewBlocks map[string]bool) {
	bs, err := bc.List(ctx, dir, beads.ListOptions{Labels: []string{gates.KindLabel}, Status: "all"})
	if err != nil {
		return
	}
	var open []beads.Bead
	for _, b := range bs {
		if workflowtemplate.MergeGate(workflowtemplate.Node{Bead: b}) {
			open = append(open, b)
		}
	}
	if len(open) == 0 {
		return
	}
	details, err := beadDetails(ctx, bc, dir, open)
	var partial *beads.PartialDetailsError
	if err != nil && !errors.As(err, &partial) {
		return
	}
	for _, b := range open {
		stage := beads.UnescapeLabel(workflowtemplate.Label(b, "gate-of:"))
		for i := range rows {
			if rows[i].BeadID == stage {
				rows[i].GateChecks = append(rows[i].GateChecks, *workflowNode(b, details[b.ID], reviewBlocks))
			}
		}
	}
}

// workflowInput is every node the merge rule reads for a workstream, drawn or folded.
func workflowInput(ws SnapWorkstream) []workflowtemplate.Node {
	var nodes []workflowtemplate.Node
	// Several beads can wait on one folded block; the rule reads a repeat as a duplicate step.
	folded := map[string]bool{}
	addFolded := func(n workflowtemplate.Node) {
		for _, b := range n.Blocks {
			if !folded[b.Bead.ID] {
				folded[b.Bead.ID] = true
				nodes = append(append(nodes, b), b.Steps...)
			}
		}
	}
	for _, s := range flattenSteps(ws.Steps) {
		nodes = append(nodes, s.GateChecks...)
		for _, g := range s.GateChecks {
			addFolded(g)
		}
		if s.WorkflowCheck != nil {
			n := *s.WorkflowCheck
			n.Bead.Status = s.Status
			n.Waits = s.BlockedBy
			nodes = append(append(nodes, n), n.Steps...)
			addFolded(n)
			continue
		}
		// A step with no managed metadata still names its PR and whether it is a gate.
		labels := []string{}
		if s.Gate {
			labels = append(labels, "gate")
		}
		for _, pr := range s.PR {
			if s.PRRepo != "" {
				pr = s.PRRepo + "#" + pr
			}
			labels = append(labels, "pr:"+pr)
		}
		nodes = append(nodes, workflowtemplate.Node{Bead: beads.Bead{ID: s.BeadID, Title: s.Title, Status: s.Status, Labels: labels}, Waits: s.BlockedBy})
	}
	return nodes
}

func mergeReviewRules(ws SnapWorkstream, scope []SnapStep) []DiagramFinding {
	// Parked and superseded work is out of play here, as in the rest of the
	// diagram check; a door still judges a parked merge gate that someone closes.
	ids := map[string]bool{}
	var walk func([]SnapStep)
	walk = func(level []SnapStep) {
		for _, s := range level {
			if s.Superseded || parked(s) {
				continue
			}
			ids[s.BeadID] = true
			for _, g := range s.GateChecks {
				if g.Bead.Status != statusDeferred {
					ids[g.Bead.ID] = true
				}
			}
			walk(s.Children)
		}
	}
	walk(scope)
	var out []DiagramFinding
	for _, f := range workflowtemplate.MergeRule(ws.Project, ws.Repo, workflowInput(ws), ids) {
		out = append(out, DiagramFinding{Check: f.Check, Bead: f.Target, Why: f.Text, Fix: f.Fix, Advisory: f.Advisory})
	}
	return out
}

// RefreshWorkflowHeads fills the display's PR facts from heads, which must not
// block or spawn per call (PRHeadCache). Completion doors always reread GitHub.
func RefreshWorkflowHeads(snap *Snapshot, heads func(pr string) (workflowtemplate.Head, bool)) {
	if heads == nil {
		return
	}
	for wi := range snap.Workstreams {
		ws := &snap.Workstreams[wi]
		byID := map[string]workflowtemplate.Node{}
		for _, n := range workflowInput(*ws) {
			byID[n.Bead.ID] = n
		}
		fill := func(n *workflowtemplate.Node) {
			v := byID[n.Bead.ID] // with the row's status and edges
			if v.Bead.Status == statusDeferred || !workflowtemplate.Candidate(v, byID, ws.Project) {
				return
			}
			primary := workflowtemplate.TargetPR(v, byID, ws.Project, ws.Repo)
			for _, pr := range append([]string{primary}, workflowtemplate.TargetPRs(v, byID, ws.Project, ws.Repo)...) {
				if pr == "" {
					continue
				}
				h, ok := heads(pr)
				if !ok {
					continue
				}
				if n.PRStates == nil {
					n.PRStates = map[string]string{}
				}
				n.PRStates[pr] = h.State
				if pr == primary {
					n.Head = h.SHA
				}
			}
		}
		var walk func([]SnapStep)
		walk = func(level []SnapStep) {
			for i := range level {
				s := &level[i]
				walk(s.Children)
				for g := range s.GateChecks {
					fill(&s.GateChecks[g])
				}
				if s.WorkflowCheck != nil {
					fill(s.WorkflowCheck)
				}
			}
		}
		walk(ws.Steps)
	}
}

// PRHeadTTL is how long a display PR fact stands before cmdrd reads it again.
const PRHeadTTL = 5 * time.Minute

const prHeadReadTimeout = 15 * time.Second

// PRHeadCache gives the snapshot GitHub facts without a gh process per poll: a
// lookup never blocks, stale PRs are read one at a time in the background, and a
// failed read keeps the last good value (the next try is one TTL later), so the
// display does not flip. A merged PR is final and is not read again.
type PRHeadCache struct {
	run workflowtemplate.GHRunner
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*prHeadEntry
	queue   []string
	reading bool
}

type prHeadEntry struct {
	head   workflowtemplate.Head
	known  bool
	tried  time.Time
	queued bool
}

func NewPRHeadCache(run workflowtemplate.GHRunner, ttl time.Duration) *PRHeadCache {
	return &PRHeadCache{run: run, ttl: ttl, now: time.Now, entries: map[string]*prHeadEntry{}}
}

// Lookup returns the last good facts for pr and queues a read when they are stale.
func (c *PRHeadCache) Lookup(pr string) (workflowtemplate.Head, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[pr]
	if e == nil {
		e = &prHeadEntry{}
		c.entries[pr] = e
	}
	final := e.known && e.head.State == "MERGED"
	if !final && !e.queued && (e.tried.IsZero() || c.now().Sub(e.tried) >= c.ttl) {
		e.queued = true
		c.queue = append(c.queue, pr)
		if !c.reading {
			c.reading = true
			go c.readQueue()
		}
	}
	return e.head, e.known
}

func (c *PRHeadCache) readQueue() {
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.reading = false
			c.mu.Unlock()
			return
		}
		pr := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), prHeadReadTimeout)
		h, err := workflowtemplate.ReadHead(ctx, c.run, pr)
		cancel()
		c.mu.Lock()
		e := c.entries[pr]
		e.queued, e.tried = false, c.now()
		if err == nil {
			e.head, e.known = h, true
		}
		c.mu.Unlock()
	}
}
