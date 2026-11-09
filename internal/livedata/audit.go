package livedata

import (
	"fmt"
	"github.com/example-user/commander/internal/holding"
	"slices"
	"strings"
)

// Suspect is a row or bead that looks wrong: the bead to look at, the Manager
// who owns it (Commander nudges them), the rule that fired and why.
type Suspect struct {
	Bead        string `json:"bead"`
	ManagerRole string `json:"manager_role"`
	Rule        string `json:"rule"`
	Why         string `json:"why"`
}

// Audit rule names.
const (
	RuleStalledButLive  = "stalled-with-agent-or-wait"
	RuleNextGate        = "next-gate"
	RuleFlag            = "flag-iff-red"
	RuleNeedsYou        = "needs-you-not-user-gate"
	RuleStaleReviewer   = "stale-reviewer-gate"
	RuleOpenBead        = "open-bead-no-owner"
	RuleRollupAsState   = "rollup-as-state"
	RuleNeedsYouLane    = CheckNeedsYouLane
	statusDeferred      = "deferred"
	noteWaitPrefixLower = "wait"
)

// AuditOptions are the seams the snapshot alone cannot fill.
type AuditOptions struct {
	// Approved reports whether `login` already approved the PR a reviewer
	// gate waits on (gh PR reviews APPROVED, from the waits poller's data).
	// nil skips the live lookup; a gate whose evidence already says approved
	// is still caught.
	Approved func(gateBeadID, login string) bool
}

// AuditSnapshot runs the sanity rules over a snapshot and returns the suspect
// rows and beads, ordered by workstream. Pure: it reads only the snapshot.
func AuditSnapshot(s Snapshot, nowSec int64, opt AuditOptions) []Suspect {
	var out []Suspect
	rows := Rows(s, nowSec)
	byBead := map[string]SnapStep{}
	owner := map[string]string{} // bead -> Manager role
	for _, ws := range s.Workstreams {
		owner[ws.BeadID] = ws.ManagerRole
		for _, st := range flattenSteps(ws.Steps) {
			byBead[st.BeadID] = st
			owner[st.BeadID] = ws.ManagerRole
		}
	}
	add := func(bead, role, rule, why string) {
		out = append(out, Suspect{Bead: bead, ManagerRole: role, Rule: rule, Why: why})
	}
	for i, ws := range s.Workstreams {
		row := rows[i]
		steps := flattenSteps(ws.Steps)
		agent, hasAgent := s.Agents[ws.ManagerRole]

		// (1) Stalled although someone is on it or a wait is recorded.
		if row.Line.Kind == "stalled" && strings.HasPrefix(row.Line.Text, "Stalled") {
			var why []string
			if agentBusy(agent, hasAgent) {
				why = append(why, "its Manager is in a turn")
			}
			for _, st := range steps {
				if stepClosed(st) || st.Status == statusDeferred || st.Gate {
					continue
				}
				if strings.HasPrefix(st.Owner, "operator-") && stepAlive(st) {
					why = append(why, fmt.Sprintf("%s has a live Operator (%s)", st.BeadID, st.Owner))
				}
				if st.Waiting != "" {
					why = append(why, fmt.Sprintf("%s waits on %s", st.BeadID, st.Waiting))
				} else if noteWait(st.Note) {
					why = append(why, fmt.Sprintf("%s has a noted wait (%s)", st.BeadID, truncate(st.Note, 60)))
				}
			}
			if len(why) > 0 {
				add(ws.BeadID, ws.ManagerRole, RuleStalledButLive, fmt.Sprintf("row reads %q but %s", row.Line.Text, strings.Join(why, "; ")))
			}
		}

		// (2) The next gate the row names must be open, in play and the earliest.
		if row.NextGateID != "" {
			g := byBead[row.NextGateID]
			switch {
			case g.Status == statusDeferred:
				add(g.BeadID, ws.ManagerRole, RuleNextGate, fmt.Sprintf("next gate %q is parked (deferred)", row.NextGate))
			case stepClosed(g):
				add(g.BeadID, ws.ManagerRole, RuleNextGate, fmt.Sprintf("next gate %q is already passed/closed", row.NextGate))
			default:
				if e := earlierOpenGate(ws, g.BeadID, byBead); e != "" {
					add(g.BeadID, ws.ManagerRole, RuleNextGate, fmt.Sprintf("next gate %q is not the earliest open gate by blocks order: %s comes first", row.NextGate, e))
				}
			}
		}

		// (3) The flag shows iff the row is red.
		red := row.Colour == "red"
		if row.Flag > 0 && !red {
			add(ws.BeadID, ws.ManagerRole, RuleFlag, fmt.Sprintf("flag %d but the row is %s (%q)", row.Flag, row.Colour, row.Line.Text))
		}
		if row.Flag == 0 && red {
			add(ws.BeadID, ws.ManagerRole, RuleFlag, fmt.Sprintf("row is red (%q) but has no flag", row.Line.Text))
		}

		// (5) A reviewer gate whose approval is already in.
		for _, gn := range ws.GateNodes {
			login, ok := strings.CutPrefix(gn.Verifier, "reviewer:")
			if !ok || gn.Status == "passed" || byBead[gn.BeadID].Status == "closed" || byBead[gn.BeadID].Status == statusDeferred {
				continue
			}
			switch {
			case gn.Status != "failed" && strings.Contains(strings.ToLower(gn.Evidence), "approved"):
				add(gn.BeadID, ws.ManagerRole, RuleStaleReviewer, fmt.Sprintf("reviewer gate %q is open but its evidence says approved (%s)", gn.Name, truncate(gn.Evidence, 60)))
			case opt.Approved != nil && opt.Approved(gn.BeadID, login):
				add(gn.BeadID, ws.ManagerRole, RuleStaleReviewer, fmt.Sprintf("reviewer gate %q is open but %s already approved the PR", gn.Name, login))
			}
		}

		// (6) Open beads nobody answers for.
		for _, b := range openWithoutOwner(ws) {
			add(b.BeadID, ws.ManagerRole, RuleOpenBead, fmt.Sprintf("%q is open with no live agent, recorded wait or the user on it", truncate(b.Title, 50)))
		}

		// (7) A row's state that is only its children's rollup.
		if ws.State == "done" && ws.Status != "closed" {
			add(ws.BeadID, ws.ManagerRole, RuleRollupAsState, "workstream state is done but its own bead is still open")
		}
		for _, st := range steps {
			if st.State == "done" && st.Status != "closed" {
				add(st.BeadID, ws.ManagerRole, RuleRollupAsState, fmt.Sprintf("%q reads done but its own bead is %s (state came from its children)", truncate(st.Title, 50), st.Status))
			}
		}
	}

	// (8) An open card the board draws as anything but the user's move.
	for _, w := range displayOf(s, nowSec).Workstreams {
		if w.Diagram == nil {
			continue
		}
		for _, f := range w.Diagram.Findings {
			if f.Check == CheckNeedsYouLane {
				add(f.Bead, w.ManagerRole, RuleNeedsYouLane, f.Why)
			}
		}
	}

	// (4) NEEDS YOU lists only open gates the user approves.
	for _, a := range s.NeedsYou {
		st, ok := byBead[a.BeadID]
		role := owner[a.BeadID]
		if role == "" {
			role = roleOfWorkstream(s, a.Project, a.Workstream)
		}
		switch {
		case !ok:
			add(a.BeadID, role, RuleNeedsYou, fmt.Sprintf("NEEDS YOU %q is a workstream bead, not a gate", truncate(a.Title, 50)))
		case !st.Gate:
			add(a.BeadID, role, RuleNeedsYou, fmt.Sprintf("NEEDS YOU %q is a plain step, not an open gate bead", truncate(a.Title, 50)))
		case !slices.Contains(st.Approvers, "user"):
			add(a.BeadID, role, RuleNeedsYou, fmt.Sprintf("NEEDS YOU %q is a gate approved by %s, not user", truncate(a.Title, 50), strings.Join(st.Approvers, ", ")))
		case stepClosed(st) || st.Status == statusDeferred:
			add(a.BeadID, role, RuleNeedsYou, fmt.Sprintf("NEEDS YOU %q is a %s gate", truncate(a.Title, 50), st.Status))
		}
	}
	return out
}

func roleOfWorkstream(s Snapshot, project, title string) string {
	for _, ws := range s.Workstreams {
		if ws.Project == project && ws.Title == title {
			return ws.ManagerRole
		}
	}
	return ""
}

func hasOpenGate(ws SnapWorkstream) bool {
	for _, st := range flattenSteps(ws.Steps) {
		if st.Gate && !stepClosed(st) && st.Status != statusDeferred {
			return true
		}
	}
	return false
}

func noteWait(note string) bool {
	n := strings.ToLower(strings.TrimSpace(note))
	return strings.HasPrefix(n, noteWaitPrefixLower) || strings.Contains(n, "waiting on") || strings.Contains(n, "waiting for")
}

func truncate(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// earlierOpenGate returns an open, in-play gate that the gate `id` is
// (transitively) blocked by, "" when `id` is the earliest.
func earlierOpenGate(ws SnapWorkstream, id string, byBead map[string]SnapStep) string {
	deps := map[string][]string{}
	for bead, st := range byBead {
		deps[bead] = append(deps[bead], st.BlockedBy...)
	}
	for _, gn := range ws.GateNodes {
		deps[gn.BeadID] = append(deps[gn.BeadID], gn.BlockedBy...)
	}
	seen := map[string]bool{id: true}
	queue := slices.Clone(deps[id])
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if st, ok := byBead[cur]; ok && st.Gate && !stepClosed(st) && st.Status != statusDeferred {
			return cur
		}
		queue = append(queue, deps[cur]...)
	}
	return ""
}

// openWithoutOwner is the open-bead invariant over a workstream's steps (the
// snapshot form of the il9.27.1.1 rule): an open step is fine when it is a
// gate, the user's, has a recorded wait or `until`, a live owner, an open child,
// is parked, or is not in progress and waits behind an open step (not parked or superseded).
func openWithoutOwner(ws SnapWorkstream) []SnapStep {
	w := newRowWorld(workstreamRows(ws), nil)
	var out []SnapStep
	for _, st := range flattenSteps(ws.Steps) {
		if st.Status == "closed" || st.Status == statusDeferred {
			continue
		}
		if _, held, _ := holding.Held(rowBead(w.rows[st.BeadID], w.now), w); !held {
			out = append(out, st)
		}
	}
	return out
}
