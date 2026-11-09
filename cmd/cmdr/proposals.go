package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/example-user/commander/internal/agentmail"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/decisions"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wsmgr"
)

// A Manager does not add gates, asks, reviews or long waits on its own say:
// each such write lands as a proposal (beads.ProposedLabel) and Commander is
// mailed to accept or overrule it. A proposed ask or user gate stays out of
// NEEDS YOU until accepted. Writes by Commander or the user stand as written.

// proposalWaitOver is how far out a Manager's until: wait or park may reach
// before it is a proposal ("come back later" is hours, not days).
const proposalWaitOver = 4 * time.Hour

// reviewTitle is a step that reviews (or re-reviews) work.
var reviewTitle = regexp.MustCompile(`(?i)\b(re-?)?review\b`)

// proposingManager is the Manager whose write becomes a proposal, "" for anyone else.
func proposingManager() string {
	if role := os.Getenv("COMMANDER_ROLE"); strings.HasPrefix(role, "manager-") {
		return role
	}
	return ""
}

// proposal is a write held for Commander's review.
type proposal struct {
	kind, prev string
	detail     string // what the Manager proposed, in a line
}

func (p proposal) label(now time.Time) string { return beads.ProposedLabel(p.kind, now.Unix(), p.prev) }

func proposedKind(p *proposal) string {
	if p == nil {
		return ""
	}
	return p.kind
}

// longWait is the first until: wait further out than proposalWaitOver, or nil.
func longWait(waits []beads.Wait, now time.Time) *beads.Wait {
	for i, w := range waits {
		if w.Kind == beads.WaitUntil && w.Until.Sub(now) > proposalWaitOver {
			return &waits[i]
		}
	}
	return nil
}

// workflowApproved reports whether the scope holding id (id itself when it is
// a workstream or track) has a closed first gate: past it, a new gate or
// review is beyond the workflow the user approved. A failed read counts as
// approved, so the write is reviewed rather than let through.
func workflowApproved(ctx context.Context, bc beads.Client, dir, id string) bool {
	all, err := readOnceMore(ctx, func() ([]beads.Bead, error) {
		return bc.List(ctx, dir, beads.ListOptions{Status: "all"})
	})
	if err != nil {
		return true
	}
	byID := make(map[string]beads.Bead, len(all))
	var gateIDs []string
	for _, b := range all {
		byID[b.ID] = b
		if isGateBead(b) && !isRetiredBead(b) && isFirstGateCandidate(b) {
			gateIDs = append(gateIDs, b.ID)
		}
	}
	scope := id
	if chain := beads.Lineage(ctx, bc, dir, nil, id); len(chain) >= 2 && !beads.IsWorkstream(byID[id].Labels) {
		scope = workflowGateScope(chain, byID)
	}
	parents, err := parentsOf(ctx, bc, dir, gateIDs)
	if err != nil {
		return true
	}
	closed, _ := firstGateOf(scope, all, parents)
	return closed
}

// standingFor is the user's standing decisions that hold for id: the global
// ones and those scoped to id or any bead above it. Unreadable = none.
func standingFor(ctx context.Context, bc beads.Client, dir, id string) []decisions.Decision {
	all, err := decisions.Load(appCommanderHome())
	if err != nil || len(all) == 0 {
		return nil
	}
	return decisions.ActiveIn(all, beads.Lineage(ctx, bc, dir, nil, id)...)
}

// checkStandingDecisions refuses an agent's write whose texts name what a
// standing decision's rule rules out. The user's own writes are never refused.
func checkStandingDecisions(ctx context.Context, bc beads.Client, dir, id string, texts ...string) error {
	if os.Getenv("COMMANDER_ROLE") == "" {
		return nil
	}
	d := decisions.Contradicts(standingFor(ctx, bc, dir, id), texts...)
	if d == nil {
		return nil
	}
	return fmt.Errorf("refused: this contradicts the user's standing decision %s (%s, %s): %q [rule %s]. Nothing was written. Work without it; if the user has changed that call, Commander retires it first (cmdr decisions retire %s)", d.ID, d.Scope, d.Date, d.Words, d.Rule, d.ID)
}

// proposeStateWrite checks a set-objective write against the standing
// decisions and, from a Manager, marks a new ask or a long wait as a proposal
// (the label rides in the same bead write). nil when the write stands as is.
func proposeStateWrite(ctx context.Context, bc beads.Client, dir, beadID string, entering bool, ask, question string, waits []beads.Wait) (*proposal, error) {
	var texts []string
	for _, w := range waits {
		if w.Kind == beads.WaitUntil {
			texts = append(texts, w.Ref)
		}
	}
	if entering {
		texts = append(texts, ask, question)
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if err := checkStandingDecisions(ctx, bc, dir, beadID, texts...); err != nil {
		return nil, err
	}
	if proposingManager() == "" {
		return nil, nil
	}
	now := time.Now()
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil || b == nil {
		b = &beads.Bead{ID: beadID}
	}
	var p *proposal
	userGate := hasLabel(b.Labels, beads.GateLabel) || (gates.IsGate(*b) && hasLabel(b.Labels, beads.ApproverLabelPrefix+"user"))
	if entering && !userGate {
		prev, _ := beads.StepState(b.Status, b.Labels)
		what := question
		if what == "" {
			what = ask
		}
		p = &proposal{kind: beads.ProposedAsk, prev: prev, detail: "ask the user: " + what}
	} else if w := longWait(waits, now); w != nil {
		p = &proposal{kind: beads.ProposedWait, detail: fmt.Sprintf("wait until %s (%s from now) for %q", w.Until.Format(time.RFC3339), w.Until.Sub(now).Round(time.Minute), w.Ref)}
	} else {
		return nil, nil
	}
	if open, ok := beads.ProposalOf(b.Labels); ok {
		if open.Kind == p.kind {
			return nil, nil // the open proposal still covers it
		}
		return nil, fmt.Errorf("%s already has a proposed %s Commander has not reviewed: nothing was written; propose the %s once Commander has answered", beadID, open.Kind, p.kind)
	}
	return p, bc.SetLabel(ctx, dir, beadID, p.label(now), true)
}

// proposedPark is the proposal a Manager's park is: one with no time, or
// further out than proposalWaitOver. nil for a short park.
func proposedPark(until time.Time, reason string, now time.Time) *proposal {
	why := ""
	if r := strings.TrimSpace(reason); r != "" {
		why = ": " + r
	}
	if until.IsZero() {
		return &proposal{kind: beads.ProposedPark, detail: "park it with no time to come back" + why}
	}
	if until.Sub(now) > proposalWaitOver {
		return &proposal{kind: beads.ProposedPark, detail: fmt.Sprintf("park it until %s (%s from now)%s", until.Format(time.RFC3339), until.Sub(now).Round(time.Minute), why)}
	}
	return nil
}

// announceProposal records who proposed what on the bead and mails Commander
// to review it. Failures are reported, never undo the write (the label holds it);
// a failed mail also comes back for callers with a result to put it in.
func announceProposal(ctx context.Context, bc beads.Client, dir, project, beadID, manager string, p proposal, stderr *os.File) error {
	_ = bc.AddComment(ctx, dir, beadID, fmt.Sprintf("proposed %s by %s at %d: %s", p.kind, manager, time.Now().Unix(), p.detail))
	title := beadID
	if b, err := bc.Show(ctx, dir, beadID); err == nil && b != nil && b.Title != "" {
		title = b.Title
	}
	body := fmt.Sprintf("%s proposes a new %s on %s %q (project %s): %s\n\n", manager, p.kind, beadID, title, project, p.detail)
	if p.kind == beads.ProposedAsk || p.kind == beads.ProposedGate {
		body += "It does not reach the user until you accept it. "
	}
	body += "Review it against the user's standing decisions and what the user asked for. Overrule what they did not ask for yourself; never relay it to the user as a question.\n\n" +
		fmt.Sprintf("Accept:   cmdr proposals accept --project %s --bead %s\n", project, beadID) +
		fmt.Sprintf("Overrule: cmdr proposals overrule --project %s --bead %s --reason '<why, quoting the user's call>'\n", project, beadID)
	if ds := standingFor(ctx, bc, dir, beadID); len(ds) > 0 {
		body += "\n" + decisions.Text(ds)
	}
	subject := fmt.Sprintf("[proposed] %s on %s (%s)", p.kind, title, project)
	st, client, key, cleanup, err := mailDeps()
	if err == nil {
		defer cleanup()
		err = sendRoleMail(ctx, st, client, key, manager, []string{"commander"}, subject, body, agentmail.SendOptions{Importance: "high"})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: %s is proposed but mailing Commander failed: %v\n", beadID, err)
	}
	return err
}

// runProposals is `cmdr proposals list|accept|overrule`: Commander's review of
// the Managers' proposed gates, asks, reviews, waits and parks.
func runProposals(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		emitErr(stderr, "proposals: want list, accept or overrule")
		return 1
	}
	verb, args := args[0], args[1:]
	fs := flag.NewFlagSet("proposals "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectName := fs.String("project", "", "project slug")
	beadID := fs.String("bead", "", "the proposed bead")
	reason := fs.String("reason", "", "overrule: why, for the Manager (quote the user's call)")
	asJSON := fs.Bool("json", false, "list: JSON output")
	if err := fs.Parse(args); err != nil {
		emitErr(stderr, fmt.Sprintf("parse flags: %v", err))
		return 1
	}
	if verb == "list" {
		return listProposals(stdout, stderr, *asJSON)
	}
	if verb != "accept" && verb != "overrule" {
		emitErr(stderr, fmt.Sprintf("proposals: unknown verb %q (want list, accept or overrule)", verb))
		return 1
	}
	if role := os.Getenv("COMMANDER_ROLE"); role != "" && role != "commander" {
		emitErr(stderr, "only Commander (or the user) accepts or overrules a proposal")
		return 1
	}
	if *projectName == "" || *beadID == "" {
		emitErr(stderr, "missing required flag(s): --project, --bead")
		return 1
	}
	if verb == "overrule" && strings.TrimSpace(*reason) == "" {
		emitErr(stderr, "overrule needs --reason: the Manager is told why (quote the user's call)")
		return 1
	}
	project, err := resolveProject(*projectName)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	who := os.Getenv("COMMANDER_ROLE")
	if who == "" {
		who = "user"
	}
	var out map[string]any
	if verb == "accept" {
		out, err = acceptProposal(ctx, newBeadsClientFn(), *project, *beadID, who)
	} else {
		out, err = overruleProposal(ctx, newBeadsClientFn(), *project, *beadID, who, strings.TrimSpace(*reason), stderr)
	}
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(out)
	return 0
}

func openProposal(ctx context.Context, bc beads.Client, dir, beadID string) (*beads.Bead, beads.Proposal, error) {
	b, err := bc.Show(ctx, dir, beadID)
	if err != nil {
		return nil, beads.Proposal{}, err
	}
	if b == nil {
		return nil, beads.Proposal{}, fmt.Errorf("bead %s not found", beadID)
	}
	p, ok := beads.ProposalOf(b.Labels)
	if !ok {
		return nil, beads.Proposal{}, fmt.Errorf("%s has no open proposal (cmdr proposals list)", beadID)
	}
	return b, p, nil
}

// acceptProposal lets the write stand: the label goes, so a proposed ask or
// user gate now shows in NEEDS YOU.
func acceptProposal(ctx context.Context, bc beads.Client, project store.Project, beadID, who string) (map[string]any, error) {
	_, p, err := openProposal(ctx, bc, project.Path, beadID)
	if err != nil {
		return nil, err
	}
	if err := bc.SetLabel(ctx, project.Path, beadID, p.Label, false); err != nil {
		return nil, err
	}
	_ = bc.AddComment(ctx, project.Path, beadID, fmt.Sprintf("proposed %s accepted by %s at %d", p.Kind, who, time.Now().Unix()))
	out := map[string]any{"project": project.Name, "bead_id": beadID, "kind": p.Kind, "accepted": true}
	if p.Kind == beads.ProposedAsk || p.Kind == beads.ProposedGate {
		out["note"] = "it now shows in NEEDS YOU if it asks the user anything: raise it with the user"
	}
	return out, nil
}

// overruleProposal undoes the proposed write and tells the Manager why.
func overruleProposal(ctx context.Context, bc beads.Client, project store.Project, beadID, who, reason string, stderr *os.File) (map[string]any, error) {
	dir := project.Path
	b, p, err := openProposal(ctx, bc, dir, beadID)
	if err != nil {
		return nil, err
	}
	var undone string
	switch p.Kind {
	case beads.ProposedAsk:
		prev := p.Prev
		if prev == "" || prev == beads.StateNeedsUser || prev == beads.StateDone {
			prev = beads.StateNotStarted
		}
		if err := applyStepState(ctx, bc, dir, beadID, prev); err != nil {
			return nil, err
		}
		if err := syncNeedsYou(ctx, project.Name, beadID, prev, "", "", "", nil); err != nil {
			return nil, err
		}
		undone = "the ask and its card are gone; the step is " + prev + " again"
	case beads.ProposedGate, beads.ProposedReview:
		stage, name := gateStageAndName(b.Labels)
		if b.Status == "closed" && slices.Contains(b.Labels, gates.RetiredLabel) {
			// A retry after a failed hold: the gate is already retired.
		} else if stage != "" && name != "" {
			err = gates.Remove(ctx, bc, dir, project.ID, stage, name)
		} else if err = bc.SetLabel(ctx, dir, beadID, gates.RetiredLabel, true); err == nil {
			err = bc.Close(ctx, dir, beadID)
		}
		if err != nil {
			return nil, err
		}
		undone = "it is retired: nothing waits on it"
	case beads.ProposedWait:
		for _, l := range b.Labels {
			if w, ok := beads.ParseWaitLabel(l); ok && w.Kind == beads.WaitUntil {
				if err := bc.SetLabel(ctx, dir, beadID, l, false); err != nil {
					return nil, err
				}
			}
		}
		undone = "the timed wait is gone"
	case beads.ProposedPark:
		if _, err := beads.Unpark(ctx, bc, dir, beadID, who); err != nil {
			return nil, err
		}
		undone = "it is unparked"
	default:
		return nil, fmt.Errorf("%s: unknown proposal kind %q", beadID, p.Kind)
	}
	// The undo can leave steps with no owner, wait or card (State missing):
	// give their Manager custody in the same step, unless something else holds them.
	held, err := overruleCustody(ctx, bc, dir, b, p.Kind)
	if err != nil {
		return nil, fmt.Errorf("%s: undone, but the Manager's hold was not written (run the overrule again): %w", beadID, err)
	}
	if err := bc.SetLabel(ctx, dir, beadID, p.Label, false); err != nil {
		return nil, err
	}
	_ = bc.AddComment(ctx, dir, beadID, fmt.Sprintf("proposed %s overruled by %s at %d: %s", p.Kind, who, time.Now().Unix(), reason))
	manager := wsmgr.Role(wsmgr.Owner(ctx, bc, dir, beadID))
	body := fmt.Sprintf("Commander overruled your proposed %s on %s %q: %s\n\nUndone: %s. Do not add it again; carry on with the approved workflow. If the user wants it, they will say so.", p.Kind, beadID, b.Title, reason, undone)
	if len(held) > 0 {
		body += fmt.Sprintf("\n\nYou now hold %s (a \"Manager to start\" receipt, due within %s): start each, give it an owner or a wait, or close it.", strings.Join(held, ", "), custodyWithin)
	} else if p.Kind == beads.ProposedAsk || p.Kind == beads.ProposedWait {
		body += fmt.Sprintf("\n\n%s needs a holder now: an owner working it, a wait cmdrd can watch, or close it.", beadID)
	}
	st, client, key, cleanup, mailErr := mailDeps()
	if mailErr == nil {
		defer cleanup()
		mailErr = sendRoleMail(ctx, st, client, key, "commander", []string{manager}, fmt.Sprintf("[overruled] %s on %s", p.Kind, b.Title), body, agentmail.SendOptions{Importance: "high"})
	}
	if mailErr != nil {
		fmt.Fprintf(stderr, "warning: overruled, but mailing %s failed: %v\n", manager, mailErr)
	}
	return map[string]any{"project": project.Name, "bead_id": beadID, "kind": p.Kind, "overruled": true, "undone": undone, "manager": manager, "held": held}, nil
}

// overruleCustody holds, for the Manager, every step the overrule of b leaves
// unheld, and returns the steps that carry the Manager's "Manager to start"
// receipt afterwards (written now, or by an earlier run of the same overrule).
// An ask, wait or park overrule concerns b itself; a gate or review is retired,
// which releases the stage it guarded, every bead waiting on it by a blocks
// edge, and its parent when it was the last open child. Which of those are
// unheld is the board's own rule (livedata.WithEdits over cmdrd's snapshot with
// the fresh state of each), so a step with an open child, a live Manager's turn,
// a running Operator, a wait or a card gets nothing; a step the board does not
// show (or no board) is judged on a fresh read: no open child, then
// holdForManager's own checks.
func overruleCustody(ctx context.Context, bc beads.Client, dir string, b *beads.Bead, kind string) ([]string, error) {
	candidates := []string{b.ID}
	closing := ""
	if kind == beads.ProposedGate || kind == beads.ProposedReview {
		closing = b.ID
		candidates = nil
		if stage, _ := gateStageAndName(b.Labels); stage != "" {
			candidates = append(candidates, stage)
		}
		deps, err := bc.Dependents(ctx, dir, b.ID)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			if d.Type == "blocks" && d.IssueID != "" {
				candidates = append(candidates, d.IssueID)
			}
		}
		if d, err := beads.FreshDetails(ctx, bc, dir, []string{b.ID}); err != nil {
			return nil, err
		} else if parent := d[b.ID].Parent; parent != "" {
			candidates = append(candidates, parent)
		}
	}
	if len(candidates) == 0 {
		return []string{}, nil
	}
	fresh := map[string]*beads.Bead{}
	var ids []string
	parents, err := beads.FreshDetails(ctx, bc, dir, candidates)
	if err != nil {
		return nil, err
	}
	for _, id := range candidates {
		if id == closing || fresh[id] != nil {
			continue
		}
		if d, ok := parents[id]; ok && d.Parent == "" {
			continue // a workstream bead: not a step, its Manager holds it
		}
		c, err := beads.FreshShow(ctx, bc, dir, id)
		if errors.Is(err, beads.ErrNotFound) || (err == nil && c == nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.Status == "closed" || c.Status == "tombstone" || c.Status == beads.StatusDeferred || beads.IsWorkstream(c.Labels) {
			continue // done, parked, or a nested workstream bead (its own Manager holds it)
		}
		fresh[id] = c
		ids = append(ids, id)
	}
	held := []string{}
	var check []string
	for _, id := range ids {
		if hasManagerStart(fresh[id].Labels) {
			held = append(held, id) // an earlier run of this overrule held it
		} else {
			check = append(check, id)
		}
	}
	unheld, onBoard := overruleUnheld(boardSnapshot(), fresh, closing)
	var toHold []string
	for _, id := range check {
		if onBoard[id] {
			if unheld[id] {
				toHold = append(toHold, id)
			}
			continue
		}
		open, err := beads.OpenChildren(ctx, bc, dir, id)
		if err != nil {
			return nil, err
		}
		if len(open) == 0 {
			toHold = append(toHold, id)
		}
	}
	if len(toHold) == 0 {
		return held, nil
	}
	wrote, err := holdForManager(ctx, bc, dir, toHold, closing, time.Now())
	if err != nil {
		return nil, err
	}
	return append(held, wrote...), nil
}

// overruleUnheld reads the board as it is once the overrule lands: each step in
// fresh takes its fresh status, state, owner and waits, and closing (the retired
// gate, "" for none) reads closed. unheld are the steps the board then marks
// Broken; onBoard the steps it shows at all (nil board: none).
func overruleUnheld(snap *livedata.Snapshot, fresh map[string]*beads.Bead, closing string) (unheld, onBoard map[string]bool) {
	unheld, onBoard = map[string]bool{}, map[string]bool{}
	if snap == nil {
		return unheld, onBoard
	}
	edits := map[string]func(*livedata.SnapStep){}
	for id, c := range fresh {
		c := c
		edits[id] = func(st *livedata.SnapStep) {
			onBoard[st.BeadID] = true
			state, owner := beads.StepState(c.Status, c.Labels)
			if owner != st.Owner {
				st.OwnerState = "" // the board's liveness was about another owner
			}
			st.Status, st.State, st.Owner = c.Status, state, owner
			st.Waiting = ""
			if len(beads.WaitsOf(c.Labels)) > 0 {
				st.Waiting = "recorded wait"
			}
		}
	}
	s := *snap
	if closing != "" {
		edits[closing] = func(st *livedata.SnapStep) { st.Status, st.State = "closed", beads.StateDone }
		s.Workstreams = slices.Clone(s.Workstreams)
		for i, ws := range s.Workstreams {
			nodes := slices.Clone(ws.GateNodes)
			for j := range nodes {
				if nodes[j].BeadID == closing {
					nodes[j].Status = "passed"
				}
			}
			s.Workstreams[i].GateNodes = nodes
		}
	}
	for _, v := range livedata.WithEdits(s, edits).Broken {
		unheld[v.BeadID] = true
	}
	return unheld, onBoard
}

// hasManagerStart: the labels carry the Manager's "Manager to start" receipt.
func hasManagerStart(labels []string) bool {
	for _, w := range beads.WaitsOf(labels) {
		if w.Kind == beads.WaitUntil && w.Ref == beads.ManagerStartWait {
			return true
		}
	}
	return false
}

// gateStageAndName reads an internal-gate gate bead's stage and name ("" for a
// gate created as an objective).
func gateStageAndName(labels []string) (stage, name string) {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "gate-of:"); ok {
			stage = beads.UnescapeLabel(v)
		}
		if v, ok := strings.CutPrefix(l, "gate-name:"); ok {
			name = beads.UnescapeLabel(v)
		}
	}
	return stage, name
}

type proposalRow struct {
	Project string `json:"project"`
	BeadID  string `json:"bead_id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	At      int64  `json:"at"`
}

func listProposals(stdout, stderr *os.File, asJSON bool) int {
	st, cleanup, err := needsYouStoreFn()
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	projects, err := st.ListProjects(ctx)
	if err != nil {
		emitErr(stderr, err.Error())
		return 1
	}
	bc := newBeadsClientFn()
	rows := []proposalRow{}
	for _, p := range projects {
		all, err := bc.List(ctx, p.Path, beads.ListOptions{Status: "all"})
		if err != nil {
			fmt.Fprintf(stderr, "warning: %s: %v\n", p.Name, err)
			continue
		}
		for _, b := range all {
			if pr, ok := beads.ProposalOf(b.Labels); ok && b.Status != "closed" && b.Status != "tombstone" {
				rows = append(rows, proposalRow{Project: p.Name, BeadID: b.ID, Title: b.Title, Kind: pr.Kind, At: pr.At})
			}
		}
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no open proposals")
	}
	for _, r := range rows {
		fmt.Fprintf(stdout, "%-12s %-14s %-7s %s  (proposed %s)\n", r.Project, r.BeadID, r.Kind, r.Title, time.Unix(r.At, 0).Format("Jan 2 15:04"))
	}
	return 0
}
