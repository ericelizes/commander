package livedata

import (
	"testing"

	"github.com/example-user/commander/internal/beads"
)

func TestResultHandoffUsesManagerLaneAndNeverInventsLiveOperator(t *testing.T) {
	task := SnapStep{BeadID: "lab-x.1.1", Title: "CPU measurement report", Task: true, Status: "in_progress", State: "review", Owner: "manager-lab-x", Waiting: "Manager to close", WaitSince: 1792999900, WaitUntil: 1793003600, WaitUntilAs: "Manager to close"}
	step := SnapStep{Status: "in_progress", State: "in-progress", Owner: "manager-lab-x", Children: []SnapStep{task}}
	row, d := taskBoard(t, step)
	if row.Working != 0 {
		t.Fatalf("result receipt fabricated a live Operator: %+v", row)
	}
	if row.Text != resultReviewText || d.Text != resultReviewText || d.Actor != ActorManager || d.Kind != "waiting" {
		t.Fatalf("handoff disagrees: row=%q diagram=%+v", row.Text, d)
	}
	if row.Workflow.Steps[0].Text != d.Text {
		t.Fatal("workflow and diagram disagree")
	}
	if task.Waiting != "Manager to close" {
		t.Fatal("rendering mutated historical receipt")
	}
}
func TestResultHandoffDoesNotOutrankResumedOperator(t *testing.T) {
	receipt := SnapStep{BeadID: "old", Task: true, Status: "in_progress", State: "review", Owner: "manager-lab-x", Waiting: "Manager to close", WaitUntil: 1793003600, WaitUntilAs: "Manager to close"}
	live := opTask("in_progress", "working")
	live.BeadID = "new"
	_, d := taskBoard(t, SnapStep{Status: "in_progress", State: "in-progress", Owner: "manager-lab-x", Children: []SnapStep{receipt, live}})
	if d.Actor != ActorOperators || d.Text != "Operator working" {
		t.Fatalf("old result masks resumed work: %+v", d)
	}
}
func TestResultHandoffBecomesOverdueWithoutBecomingUserDecision(t *testing.T) {
	s := SnapStep{Status: "in_progress", State: "review", Waiting: "Manager to close", WaitSince: 10, WaitUntil: 20, WaitUntilAs: "Manager to close"}
	l, ok := resultHandoff([]SnapStep{s}, 21)
	if !ok || l.kind != "stalled" || l.text != "Manager review overdue" || l.since != 10 {
		t.Fatal(l, ok)
	}
	s.Status = "closed"
	if _, ok := resultHandoff([]SnapStep{s}, 21); ok {
		t.Fatal("closed handoff remains outstanding")
	}
}

// Work a close released (or a create made) carries the Manager's "Manager to
// start" receipt: the row reads the Manager's next move, never State missing,
// and past its time it reads stalled with the recovery the Manager owes.
func TestStartReceiptReadsTheManagersMoveNeverStateMissing(t *testing.T) {
	released := func(until int64) []SnapStep {
		return []SnapStep{
			st("a", closedStep, title("Draft the notice")),
			st("b", title("Send the notice"), blockedBy("a"), func(s *SnapStep) {
				s.Waiting, s.WaitSince, s.WaitUntil, s.WaitUntilAs = "Manager to start", caseNow-60, until, "Manager to start"
			}),
		}
	}
	for _, tc := range []struct {
		name       string
		until      int64
		kind, text string
	}{
		{"in time", caseNow + 3000, "waiting", startPendingText},
		{"overdue", caseNow - 1, "stalled", startOverdueText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "W", Status: "open", ManagerRole: "manager-w", Steps: released(tc.until)}}, Agents: map[string]SnapAgent{"manager-w": caseIdle}}
			for _, v := range SnapshotViolations(s) {
				if v.BeadID == "b" {
					t.Fatalf("receipt holds nothing: %+v", v)
				}
			}
			r := Board(s, caseNow).Workstreams[0]
			expectLine(t, r, tc.kind, tc.text)
			for _, d := range r.Diagram.Steps {
				if d.ID == "b" && d.Actor != ActorManager {
					t.Fatalf("diagram hand %+v", d)
				}
			}
		})
	}
}

// startReceipt is a "Manager to start" receipt on a step, due at until.
func startReceipt(until int64) func(*SnapStep) {
	return func(s *SnapStep) {
		s.Waiting, s.WaitSince, s.WaitUntil, s.WaitUntilAs = beads.ManagerStartWait, caseNow-60, until, beads.ManagerStartWait
	}
}

// A receipt written at create survives a gate linked in front of the step
// later: the step is not ready, so the row never reads it as the Manager's
// move, and never as overdue recovery while the user's approval is pending.
func TestStartReceiptBehindAnOpenGateIsNotTheManagersMove(t *testing.T) {
	for _, until := range []int64{caseNow + 3000, caseNow - 1} {
		steps := []SnapStep{
			st("g", gateOf("Workflow approved", "user")),
			st("b", title("Build it"), blockedBy("g"), startReceipt(until)),
		}
		if l, ok := resultHandoff(steps, caseNow); ok {
			t.Fatalf("until %d: receipt behind an open gate read as %+v", until, l)
		}
		steps[0] = st("g", closedStep, gateOf("Workflow approved", "user"))
		if l, ok := resultHandoff(steps, caseNow); !ok || (l.text != startPendingText && l.text != startOverdueText) {
			t.Fatalf("until %d: receipt on a ready step read as %+v, %v", until, l, ok)
		}
	}
}

// A receipt on one step never hides a sibling nothing holds: the row keeps
// State missing. Only when every broken step carries a receipt does the
// receipt's own line stand.
func TestStartReceiptNeverMasksStateMissingBesideIt(t *testing.T) {
	unheld := func(s *SnapStep) { s.Title, s.Broken = "Nobody on this", true }
	r := boardOne([]SnapStep{
		st("b", title("Send the notice"), startReceipt(caseNow+3000)),
		st("c", unheld),
	}, &caseIdle)
	expectLine(t, r, "stalled", "State missing")

	overdue := func(s *SnapStep) { startReceipt(caseNow - 1)(s); s.Broken = true }
	r = boardOne([]SnapStep{st("b", title("Send the notice"), overdue)}, &caseIdle)
	expectLine(t, r, "stalled", startOverdueText)
}
