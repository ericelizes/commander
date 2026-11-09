package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// gateSnapshot is a workstream whose next step waits only behind the user's
// send gate: closing the gate releases it to whatever holds it next.
func gateSnapshot(next livedata.SnapStep) *livedata.Snapshot {
	next.BeadID, next.Title, next.BlockedBy = "w.2", "Send it", []string{"w.9"}
	if next.Status == "" {
		next.Status, next.State = "open", beads.StateNotStarted
	}
	return &livedata.Snapshot{
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}, "operator-w-2": {State: "working"}},
		Workstreams: []livedata.SnapWorkstream{{
			BeadID: "w", Project: "manifest", Title: "Notify", Status: "open", ManagerRole: "manager-w",
			Steps: []livedata.SnapStep{
				{BeadID: "w.9", Title: "Send gate", Status: "open", State: beads.StateInProgress, Gate: true, Approvers: []string{"user"}},
				next,
			},
		}},
	}
}

func withCloseSnapshot(t *testing.T, s *livedata.Snapshot, role string) {
	t.Helper()
	orig := closeSnapshotFn
	closeSnapshotFn = func() (*livedata.Snapshot, error) { return s, nil }
	t.Cleanup(func() { closeSnapshotFn = orig })
	t.Setenv("COMMANDER_ROLE", role)
}

func closeIndex(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestCloseDoor_RefusesLeavingWorkUnheld(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "commander")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("a close that leaves w.2 unheld passed the door")
	}
	_, errOut := read()
	for _, want := range []string{"w.2", "State missing", "--then-waiting until:", "--then-owner", "--then-decision"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q, want %q", errOut, want)
		}
	}
	if closeIndex(mock.Calls, "Close:") >= 0 {
		t.Fatalf("a refused close closed:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

func TestCloseDoor_ThenWaitingHoldsBeforeTheClose(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "commander")
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	stdout, stderr, read := withTempOutputFiles(t)
	code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9", "--then-waiting", "until:" + at + ":the Manager sends the final text"}, stdout, stderr)
	out, errOut := read()
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	wait, closed := -1, closeIndex(mock.Calls, "Close:w.9")
	for i, c := range mock.Calls {
		if strings.HasPrefix(c, "SetLabel:w.2|") && strings.Contains(c, "Manager_20sends") && strings.Contains(c, "on=true") {
			wait = i
			break
		}
	}
	if wait < 0 || closed < 0 || wait > closed {
		t.Fatalf("want the wait on w.2 written before Close:w.9, calls:\n%s", strings.Join(mock.Calls, "\n"))
	}
	if !strings.Contains(out, `"then":["w.2"]`) || !strings.Contains(out, `"closed":true`) {
		t.Fatalf("stdout %s", out)
	}
}

func TestCloseDoor_RefusedHoldClosesNothing(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "commander")
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9", "--then-waiting", "until:" + at + ":send-wait"}, stdout, stderr); code == 0 {
		t.Fatal("a slug label passed through --then-waiting")
	}
	if _, errOut := read(); !strings.Contains(errOut, "plain words") || !strings.Contains(errOut, "not closed") {
		t.Fatalf("stderr %q", errOut)
	}
	if closeIndex(mock.Calls, "Close:") >= 0 {
		t.Fatalf("closed after a refused hold:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

func TestCloseDoor_HeldWorkIsUnaffected(t *testing.T) {
	for name, next := range map[string]livedata.SnapStep{
		"running operator": {Status: "open", State: beads.StateInProgress, Owner: "operator-w-2", OwnerState: "working"},
		"recorded wait":    {Status: "open", State: beads.StateNotStarted, Waiting: "CI #7"},
		"user decision":    {Status: "open", State: beads.StateNeedsUser, Waiting: "you"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.9", Title: "Send gate", Status: "open"}}
			setupObjectiveProject(t, mock)
			withCloseSnapshot(t, gateSnapshot(next), "commander")
			stdout, stderr, read := withTempOutputFiles(t)
			if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
				_, e := read()
				t.Fatal(e)
			}
			if closeIndex(mock.Calls, "Close:w.9") < 0 {
				t.Fatalf("calls:\n%s", strings.Join(mock.Calls, "\n"))
			}
		})
	}
}

// The workstream's own Manager is mid-turn (it can spawn the next Operator
// before the turn ends) and the user's own close has no role: neither is refused.
func TestCloseDoor_ManagerAndUserAreNotRefused(t *testing.T) {
	for _, role := range []string{"manager-w", ""} {
		mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.9", Title: "Send gate", Status: "open"}}
		setupObjectiveProject(t, mock)
		withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), role)
		stdout, stderr, read := withTempOutputFiles(t)
		if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
			_, e := read()
			t.Fatalf("role %q: %s", role, e)
		}
	}
}

func TestCloseDoor_ThenFlagsNeedAStep(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.9", Title: "Send gate", Status: "open"}}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{Waiting: "CI #7"}), "commander")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9", "--then-waiting", "ci:#7"}, stdout, stderr); code == 0 {
		t.Fatal("--then-waiting with nothing left unheld and no --then-step passed")
	}
	if _, errOut := read(); !strings.Contains(errOut, "--then-step") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestCloseDoor_ThenOwnerAndThenDecisionHold(t *testing.T) {
	for name, then := range map[string][]string{
		"owner":    {"--then-owner", "operator-w-2"},
		"decision": {"--then-decision", `{"question": "Send the final text?", "context": "The text is final.", "options": [{"label": "Send", "approves": true}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			// w.2 waits on the gate being closed: the hold reads it as closed.
			mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"},
				DepsResult: map[string][]beads.Dep{"w.2": {{IssueID: "w.2", DependsOnID: "w.9", Type: "blocks"}}}}
			setupObjectiveProject(t, mock)
			registerRunningOperator(t, "operator-w-2")
			withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "commander")
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalCloseObjective(append([]string{"--project", "manifest", "--bead", "w.9"}, then...), stdout, stderr)
			if _, e := read(); code != 0 {
				t.Fatal(e)
			}
			hold, closed := closeIndex(mock.Calls, "SetLabel:w.2|"), closeIndex(mock.Calls, "Close:w.9")
			if hold < 0 || closed < 0 || hold > closed {
				t.Fatalf("want the hold on w.2 before Close:w.9, calls:\n%s", strings.Join(mock.Calls, "\n"))
			}
		})
	}
}

// A close the door lets through (the workstream's Manager mid-turn, the user,
// an Operator finishing its own task) still never leaves work reading State
// missing once the turn ends: the released step gets the Manager's
// "Manager to start" receipt before the close lands.
func TestCloseDoor_PermittedCloseHoldsReleasedWorkForTheManager(t *testing.T) {
	for _, role := range []string{"manager-w", ""} {
		t.Run("role "+role, func(t *testing.T) {
			mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}}
			setupObjectiveProject(t, mock)
			withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), role)
			stdout, stderr, read := withTempOutputFiles(t)
			if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
				_, e := read()
				t.Fatal(e)
			}
			out, _ := read()
			hold, closed := -1, closeIndex(mock.Calls, "Close:w.9")
			for i, c := range mock.Calls {
				if strings.HasPrefix(c, "SetLabel:w.2|") && strings.Contains(c, "Manager_20to_20start") && strings.Contains(c, "on=true") {
					hold = i
				}
			}
			if hold < 0 || closed < 0 || hold > closed {
				t.Fatalf("want the Manager's receipt on w.2 before Close:w.9, calls:\n%s", strings.Join(mock.Calls, "\n"))
			}
			if !strings.Contains(out, `"custody":["w.2"]`) {
				t.Fatalf("stdout %s", out)
			}
		})
	}
}

// The board the door judged may lag: a step a fresh read shows waiting,
// claimed by an Operator or the user's gets no receipt.
func TestCloseDoor_CustodySkipsWorkAFreshReadShowsHeld(t *testing.T) {
	for name, labels := range map[string][]string{
		"waiting":  {beads.Wait{Kind: beads.WaitCI, PR: 7, Since: time.Now()}.Label()},
		"operator": {"owner:operator-x"},
		"user":     {"needs-user"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open", Labels: labels}}
			setupObjectiveProject(t, mock)
			withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "manager-w")
			stdout, stderr, read := withTempOutputFiles(t)
			if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
				_, e := read()
				t.Fatal(e)
			}
			for _, c := range mock.Calls {
				if strings.Contains(c, "Manager_20to_20start") {
					t.Fatalf("receipt written on held work: %s", c)
				}
			}
		})
	}
}

// A failed receipt write closes nothing: the close would leave the step unheld.
func TestCloseDoor_FailedCustodyClosesNothing(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}, SetErr: errors.New("busy")}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "manager-w")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("close passed with its custody write failing")
	}
	if _, e := read(); !strings.Contains(e, "was not closed") {
		t.Fatalf("stderr %q", e)
	}
	if closeIndex(mock.Calls, "Close:") >= 0 {
		t.Fatalf("closed:\n%s", strings.Join(mock.Calls, "\n"))
	}
}

// The Manager closes the task its step waited on (the step recorded a wait on
// that task's Operator, now finished): the step keeps no other holder, so it
// gets the Manager's receipt instead of reading State missing or Agent stopped.
func TestCloseDoor_ClosingTheTaskAStepWaitedOnHoldsTheStep(t *testing.T) {
	opWait := beads.Wait{Kind: beads.WaitOperator, Ref: "operator-op-x-w-2-1", Since: time.Now().Add(-time.Hour)}.Label()
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w", Title: "Notify", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.2", Title: "Send it", Status: "in_progress", Labels: []string{"kind:objective", opWait}, ParentID: "w"})
	m.Seed(beads.Bead{ID: "w.2.1", Title: "Send it (task)", Status: "in_progress", Labels: []string{"kind:task"}, ParentID: "w.2"})
	setupObjectiveProject(t, m)
	// cmdrd's board: the Operator is gone, so its wait holds nothing; the open task held the step until now.
	snap := &livedata.Snapshot{
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []livedata.SnapWorkstream{{BeadID: "w", Project: "manifest", Title: "Notify", Status: "open", ManagerRole: "manager-w",
			Steps: []livedata.SnapStep{{BeadID: "w.2", Title: "Send it", Status: "in_progress", State: beads.StateInProgress, OwnerState: livedata.OwnerDied,
				Children: []livedata.SnapStep{{BeadID: "w.2.1", Title: "Send it (task)", Status: "in_progress", State: beads.StateReview, Task: true, Waiting: beads.ManagerCloseWait}}}},
		}},
	}
	withCloseSnapshot(t, snap, "manager-w")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.2.1"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	b, _ := m.Show(t.Context(), "", "w.2")
	refs := map[string]bool{}
	for _, w := range beads.WaitsOf(b.Labels) {
		refs[w.Ref] = true
	}
	if !refs[beads.ManagerStartWait] {
		t.Fatalf("w.2 left without the Manager's receipt: %v", b.Labels)
	}
	if out, _ := read(); !strings.Contains(out, `"custody":["w.2"]`) {
		t.Fatalf("stdout %s", out)
	}
	// What the board reads once the close lands: the Manager's move, never State missing.
	after := livedata.WithEdits(*snap, map[string]func(*livedata.SnapStep){
		"w.2.1": func(s *livedata.SnapStep) { s.Status, s.State = "closed", beads.StateDone },
		"w.2": func(s *livedata.SnapStep) {
			s.OwnerState, s.Waiting, s.WaitUntil, s.WaitUntilAs = "", beads.ManagerStartWait, time.Now().Add(time.Hour).Unix(), beads.ManagerStartWait
		},
	})
	row := livedata.Board(after, time.Now().Unix()).Workstreams[0]
	if row.Text == "State missing" || strings.Contains(row.Text, "Agent stopped") || len(after.Broken) > 0 {
		t.Fatalf("row %q broken %+v", row.Text, after.Broken)
	}
}

// A close bd refuses after the receipt landed (open children, busy) takes the
// receipt back: the step is still behind the unclosed bead, and a receipt left
// there would go overdue and mail the Manager about work nobody released.
func TestCloseDoor_RefusedCloseTakesTheReceiptBack(t *testing.T) {
	mock := &beads.MockClient{ShowResult: &beads.Bead{ID: "w.2", Status: "open"}, CloseErr: errors.New("has open children")}
	setupObjectiveProject(t, mock)
	withCloseSnapshot(t, gateSnapshot(livedata.SnapStep{}), "manager-w")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("close reported success with bd refusing it")
	}
	on, off, closed := -1, -1, closeIndex(mock.Calls, "Close:w.9")
	var onLabel string
	for i, c := range mock.Calls {
		if !strings.HasPrefix(c, "SetLabel:w.2|") || !strings.Contains(c, "Manager_20to_20start") {
			continue
		}
		label := strings.SplitN(strings.SplitN(c, "|label=", 2)[1], "|", 2)[0]
		if strings.Contains(c, "on=true") {
			on, onLabel = i, label
		} else if label == onLabel {
			off = i
		}
	}
	if on < 0 || closed < 0 || off < closed {
		t.Fatalf("want the receipt written, the close tried, then the same receipt removed; calls:\n%s", strings.Join(mock.Calls, "\n"))
	}
	if out, _ := read(); strings.Contains(out, `"custody"`) {
		t.Fatalf("custody reported for a close that did not happen: %s", out)
	}
}

// holdForManager reads each step fresh and leaves alone what the board's view
// may have missed: a parked step, one not started behind another open blocker
// (one in progress is not), and it fails whole on a read it cannot make.
func TestHoldForManagerSkipsParkedAndBlockedStepsAndFailsOnARead(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "w.parked", Status: beads.StatusDeferred, Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.blocker", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.closing", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.behind", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.started", Status: "in_progress", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.next", Status: "open", Labels: []string{"kind:objective"}})
	for _, e := range [][2]string{{"w.behind", "w.blocker"}, {"w.started", "w.blocker"}, {"w.next", "w.closing"}} {
		if err := m.DependOn(ctx, "", e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	held, err := holdForManager(ctx, m, "", []string{"w.parked", "w.behind", "w.started", "w.next", "w.gone"}, "w.closing", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(held, []string{"w.started", "w.next"}) {
		t.Fatalf("held %v, want the started step and the one only the closing bead blocked", held)
	}

	failing := &beads.MockClient{ShowErr: errors.New("busy")}
	if _, err := holdForManager(ctx, failing, "", []string{"w.next"}, "", time.Now()); err == nil {
		t.Fatal("an unreadable step was held or skipped silently")
	}
	for _, c := range failing.Calls {
		if strings.HasPrefix(c, "SetLabel:") {
			t.Fatalf("wrote with a failed read: %s", c)
		}
	}
}
