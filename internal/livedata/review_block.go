package livedata

import (
	"slices"
	"strconv"

	"github.com/example-user/commander/internal/workflowtemplate"
)

// ReviewBlock is a plugged review block (`cmdr workflow plug`) as the diagram
// and workflow draw it: one step on its parent's level, its checks held under
// it, each with what its check found. Read from the snapshot only: the
// evidence comments the rows carry and the PR head the display already has
// (PRHeadCache), never a GitHub call of its own.
type ReviewBlock struct {
	Template string `json:"template"`
	Invalid  bool   `json:"invalid,omitempty"`
	PR       string `json:"pr,omitempty"`
	// Head is the PR head the display knows ("" until the first read): a check
	// recorded on another head is stale.
	Head   string        `json:"head,omitempty"`
	Done   int           `json:"done"`  // checks closed with evidence on Head
	Total  int           `json:"total"` // the template's steps
	Stale  int           `json:"stale,omitempty"`
	Checks []ReviewCheck `json:"checks"`
}

// ReviewCheck is one step of a block: its state in the board's words and
// what its last check found.
type ReviewCheck struct {
	ID       string `json:"id"` // the step's bead, "" when plug has not made it
	Step     string `json:"step"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Verifier string `json:"verifier,omitempty"`
	Head     string `json:"head,omitempty"`    // the head its evidence was recorded on
	Finding  string `json:"finding,omitempty"` // what the check found
	Stale    bool   `json:"stale,omitempty"`   // closed, but current bound evidence is missing
}

// isReviewBlock: s is a plugged review container (not one of its steps).
func isReviewBlock(s SnapStep) bool {
	return s.WorkflowCheck != nil && workflowtemplate.IsReviewBlock(s.WorkflowCheck.Bead)
}

// reviewBlock is s's block data, nil when s is no review block.
func (f flow) reviewBlock(s SnapStep) *ReviewBlock {
	if !isReviewBlock(s) {
		return nil
	}
	block := s.WorkflowCheck.Bead
	key := workflowtemplate.Label(block, workflowtemplate.LabelInstance)
	rb := &ReviewBlock{Template: workflowtemplate.Label(block, workflowtemplate.LabelTemplate), PR: workflowtemplate.BlockPR(block), Head: f.reviewHead(s), Checks: []ReviewCheck{}}
	// The block's steps: rows while it is open, folded on its node once closed.
	nodes := map[string]workflowtemplate.Node{}
	rows := map[string]SnapStep{}
	var order []string
	add := func(n workflowtemplate.Node) {
		id := workflowtemplate.Label(n.Bead, workflowtemplate.LabelStep)
		if id == "" {
			return
		}
		if _, dup := nodes[id]; !dup {
			order = append(order, id)
		}
		nodes[id] = n
	}
	for _, c := range s.Children {
		if c.WorkflowCheck != nil {
			n := *c.WorkflowCheck
			n.Bead.Status = c.Status
			add(n)
			rows[workflowtemplate.Label(n.Bead, workflowtemplate.LabelStep)] = c
		}
	}
	for _, n := range s.WorkflowCheck.Steps {
		if _, ok := nodes[workflowtemplate.Label(n.Bead, workflowtemplate.LabelStep)]; !ok {
			add(n)
		}
	}
	type tstep struct{ id, title, verifier string }
	var steps []tstep
	t, definitionErr := workflowtemplate.Definition(block)
	settled := f.reviewSettled(s)
	rb.Invalid = definitionErr != nil && !settled
	if definitionErr == nil {
		for _, ts := range t.Steps {
			steps = append(steps, tstep{ts.ID, ts.Title, ts.Verifier})
		}
	} else {
		for _, id := range order {
			steps = append(steps, tstep{id: id, title: nodes[id].Bead.Title})
		}
	}
	for _, ts := range steps {
		c := ReviewCheck{Step: ts.id, Title: ts.title, Verifier: ts.verifier}
		n, made := nodes[ts.id]
		if !made {
			c.Kind, c.Text = "idle", "Not made yet: plug the block again"
			rb.Checks = append(rb.Checks, c)
			continue
		}
		c.ID = n.Bead.ID
		if n.Bead.Title != "" {
			c.Title = plainTitle(n.Bead.Title)
		}
		if e, ok := lastEvidence(n, key, ts.id); ok {
			c.Head, c.Finding = e.Head, e.Finding
			if c.Verifier == "" {
				c.Verifier = e.Verifier
			}
		}
		closed := n.Bead.Status == "closed"
		current := settled || (definitionErr == nil && workflowtemplate.CheckCurrent(n, *s.WorkflowCheck, t, ts.id, ts.verifier, rb.PR, rb.Head))
		switch {
		case rb.Invalid:
			c.Stale = true
			rb.Stale++
			c.Kind, c.Text = "stalled", "Invalid template snapshot: ask the Manager to plug the block again"
		case closed && !current:
			c.Stale = true
			rb.Stale++
			c.Kind, c.Text = "stalled", "Stale: checked "+shortSHA(c.Head)+", the PR is now at "+shortSHA(rb.Head)
			if c.Head == "" {
				c.Text = "Stale: no check recorded on the PR head " + shortSHA(rb.Head)
			}
			binding, err := workflowtemplate.Binding(s.WorkflowCheck.Comments)
			switch {
			case err != nil:
				c.Text = "Stale: writer binding required"
			case binding.PR != rb.PR || (binding.Head != rb.Head && c.Head == rb.Head):
				c.Text = "Stale: writer binding differs from the current PR head"
			case rb.Head == "":
				c.Text = "Waiting for the current PR head"
			case c.Head == rb.Head:
				c.Text = "Stale: no check recorded for the current writer binding"
			}
		case closed:
			rb.Done++
			c.Kind, c.Text = "done", "Done"
		default:
			if row, ok := rows[ts.id]; ok {
				l := f.leafLine(row)
				c.Kind, c.Text = l.kind, l.text
			} else {
				c.Kind, c.Text = "idle", "Not started"
			}
		}
		rb.Checks = append(rb.Checks, c)
	}
	rb.Total = len(rb.Checks)
	return rb
}

// reviewSettled uses the same target PR set and cached states as MergeRule.
// A merged block PR alone cannot exempt a target with another live PR.
// A closed target is exempt as the merge rule exempts it, merged or not; the
// display gets no PR facts for it, so nothing else could say so.
func (f flow) reviewSettled(s SnapStep) bool {
	nodes := workflowInput(f.ws)
	byID := map[string]workflowtemplate.Node{}
	for _, n := range nodes {
		byID[n.Bead.ID] = n
	}
	for _, n := range nodes {
		if slices.Contains(n.Waits, s.BeadID) && workflowtemplate.PluggedBefore(s.WorkflowCheck.Bead, n.Bead.ID, f.ws.Project) {
			if n.Bead.Status == "closed" {
				return true
			}
			return workflowtemplate.Settled(workflowtemplate.TargetPRs(n, byID, f.ws.Project, f.ws.Repo), n.PRStates)
		}
	}
	return false
}

// reviewHead is the PR head the display knows for s's block: its target's
// (RefreshWorkflowHeads fills merge targets), else the block's own.
func (f flow) reviewHead(s SnapStep) string {
	for _, n := range workflowInput(f.ws) {
		if n.Head != "" && slices.Contains(n.Waits, s.BeadID) && workflowtemplate.PluggedBefore(s.WorkflowCheck.Bead, n.Bead.ID, f.ws.Project) {
			return n.Head
		}
	}
	return s.WorkflowCheck.Head
}

// lastEvidence is the newest well-formed check of step in instance on n.
func lastEvidence(n workflowtemplate.Node, instance, step string) (workflowtemplate.Evidence, bool) {
	for i := len(n.Comments) - 1; i >= 0; i-- {
		if e, ok := workflowtemplate.ParseEvidence(n.Comments[i].Text); ok && e.Instance == instance && e.Step == step {
			return e, true
		}
	}
	return workflowtemplate.Evidence{}, false
}

func shortSHA(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// reviewLine is a block's own line: the same rules as any row, with two of its
// own once closed: done only while every check stands on the PR head, and
// stale after a new push.
func reviewLine(rb *ReviewBlock, closed bool, l line) line {
	if !closed {
		return l
	}
	if rb.Invalid {
		return line{kind: "stalled", text: "Invalid template snapshot: ask the Manager to plug the block again"}
	}
	if rb.Stale > 0 {
		return line{kind: "stalled", text: "Checks need renewal: " + strconv.Itoa(rb.Stale) + " to rerun, the Manager's move"}
	}
	if rb.Done < rb.Total {
		return line{kind: "stalled", text: "Closed with " + strconv.Itoa(rb.Total-rb.Done) + " checks missing, the Manager's move"}
	}
	return l
}

// progress is the block's count, as a step with work under it reads.
func (rb *ReviewBlock) progress() string {
	return strconv.Itoa(rb.Done) + " of " + strconv.Itoa(rb.Total) + " checks done"
}

// ReviewCheckLines are a block's checks as text, one line each, for agents:
// the same words the app shows when the block is opened. current marks the
// check the work is on (▶).
func ReviewCheckLines(rb *ReviewBlock, pad, current string) []string {
	var out []string
	for _, c := range rb.Checks {
		mark := "  "
		if c.ID != "" && c.ID == current {
			mark = "▶ "
		}
		l := pad + mark + c.Title + ": " + c.Text
		switch {
		case c.Finding != "":
			l += " · found: " + c.Finding
		case c.Verifier != "":
			l += " · check: " + c.Verifier
		}
		if c.Head != "" {
			l += " @" + shortSHA(c.Head)
		}
		if c.ID != "" {
			l += " (" + c.ID + ")"
		}
		out = append(out, l)
	}
	return out
}
