package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

// gateMem is a workstream whose next step waits only behind the user's send
// gate: closing the gate releases it to whatever holds it next. The close door
// reads it fresh (never cmdrd's snapshot); labels on next say what holds it.
func gateMem(t *testing.T, role string, next ...string) *loggedMem {
	t.Helper()
	m := &loggedMem{MemClient: &beads.MemClient{EdgeParentsOnly: true}}
	m.Seed(beads.Bead{ID: "w", Title: "Notify", Status: "open", Labels: []string{"kind:objective", "kind:workstream"}})
	m.Seed(beads.Bead{ID: "w.9", Title: "Send gate", Status: "open", Labels: []string{"gate", "approver:user"}})
	m.Seed(beads.Bead{ID: "w.2", Title: "Send it", Status: "open", Labels: next})
	m.Edges = []beads.Dep{
		{IssueID: "w.9", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "w.2", DependsOnID: "w", Type: "parent-child"},
		{IssueID: "w.2", DependsOnID: "w.9", Type: "blocks"},
	}
	setupObjectiveProject(t, m)
	t.Setenv("COMMANDER_ROLE", role)
	return m
}

// loggedMem is a MemClient that records its writes in order.
type loggedMem struct {
	*beads.MemClient
	Calls    []string
	SetErr   error // every SetLabel fails with it (after logging)
	CloseErr error // every Close fails with it (after logging)
	Reads    int   // List, Show, Details and Dependents calls
}

func (m *loggedMem) List(ctx context.Context, dir string, o beads.ListOptions) ([]beads.Bead, error) {
	m.Reads++
	return m.MemClient.List(ctx, dir, o)
}

func (m *loggedMem) Show(ctx context.Context, dir, id string) (*beads.Bead, error) {
	m.Reads++
	return m.MemClient.Show(ctx, dir, id)
}

func (m *loggedMem) Details(ctx context.Context, dir string, ids []string) (map[string]beads.Detail, error) {
	m.Reads++
	return m.MemClient.Details(ctx, dir, ids)
}

func (m *loggedMem) Dependents(ctx context.Context, dir, id string) ([]beads.Dep, error) {
	m.Reads++
	return m.MemClient.Dependents(ctx, dir, id)
}

func (m *loggedMem) SetLabel(ctx context.Context, dir, id, label string, on bool) error {
	m.Calls = append(m.Calls, fmt.Sprintf("SetLabel:%s|%s|on=%v", id, label, on))
	if m.SetErr != nil {
		return m.SetErr
	}
	return m.MemClient.SetLabel(ctx, dir, id, label, on)
}

func (m *loggedMem) Close(ctx context.Context, dir, id string) error {
	m.Calls = append(m.Calls, "Close:"+id)
	if m.CloseErr != nil {
		return m.CloseErr
	}
	return m.MemClient.Close(ctx, dir, id)
}

func (m *loggedMem) SetStatus(ctx context.Context, dir, id, status string) error {
	m.Calls = append(m.Calls, "SetStatus:"+id+"|"+status)
	return m.MemClient.SetStatus(ctx, dir, id, status)
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
	mock := gateMem(t, "commander")
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
	mock := gateMem(t, "commander")
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
	mock := gateMem(t, "commander")
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
	for name, next := range map[string][]string{
		"running operator": {"owner:operator-w-2"},
		"recorded wait":    {"waiting:1:ci:7"},
		"user decision":    {"needs-user"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := gateMem(t, "commander", next...)
			if name == "running operator" {
				registerRunningOperator(t, "operator-w-2")
			}
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

// Only the user, an Operator closing its own work and the workstream's own Manager may close
// leaving work unheld, and then the close itself hands that work to the Manager (custody). Any
// other agent is refused with the flags that name the next holder. The user's recorded
// --leave-unheld skips the receipt.
func TestCloseDoor_OtherAgentIsRefusedOwnManagerAndUserGetCustody(t *testing.T) {
	mock := gateMem(t, "manager-x")
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("another workstream's Manager closed w.9 leaving w.2 unheld")
	}
	if _, e := read(); !strings.Contains(e, "w.2") || !strings.Contains(e, "refused:") || !strings.Contains(e, "--then-waiting") {
		t.Fatalf("stderr %q", e)
	}
	if closeIndex(mock.Calls, "Close:") >= 0 || closeIndex(mock.Calls, "SetLabel:") >= 0 {
		t.Fatalf("a refused close wrote:\n%s", strings.Join(mock.Calls, "\n"))
	}
	mock = gateMem(t, "")
	orig := leaveUnheldReason
	leaveUnheldReason = "the user takes it from here by hand"
	t.Cleanup(func() { leaveUnheldReason, leaveUnheldRecorded = orig, false })
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatalf("the user's own close with --leave-unheld: %s", e)
	}
	if closeIndex(mock.Calls, "Close:w.9") < 0 || closeIndex(mock.Calls, "SetLabel:w.2|") >= 0 {
		t.Fatalf("calls %v", mock.Calls)
	}
	cs, _ := mock.Comments(context.Background(), "", "w.9")
	if len(cs) != 1 || !strings.Contains(cs[0].Text, "left unheld by the user") {
		t.Fatalf("recorded once: %+v", cs)
	}
}

// The door reads the beads, not cmdrd's board: a board that still shows w.2 held
// (a lagging snapshot) does not let the close through.
func TestCloseDoor_JudgesFreshReadsNotTheBoard(t *testing.T) {
	gateMem(t, "commander")
	orig := closeSnapshotFn
	closeSnapshotFn = func() (*livedata.Snapshot, error) {
		return &livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{BeadID: "w", Steps: []livedata.SnapStep{
			{BeadID: "w.2", Status: "open", State: beads.StateNotStarted, Waiting: "CI #7"},
		}}}}, nil
	}
	t.Cleanup(func() { closeSnapshotFn = orig })
	stdout, stderr, _ := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code == 0 {
		t.Fatal("a stale board's wait let the close through")
	}
}

func TestCloseDoor_ThenFlagsNeedAStep(t *testing.T) {
	gateMem(t, "commander", "waiting:1:ci:7")
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
			mock := gateMem(t, "commander")
			registerRunningOperator(t, "operator-w-2")
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

// A close the door lets through (the workstream's Manager, the user, the app's Complete button)
// never leaves work reading State missing: the released step gets the Manager's "Manager to
// start" receipt before the close lands.
func TestCloseDoor_PermittedCloseHoldsReleasedWorkForTheManager(t *testing.T) {
	for _, role := range []string{"manager-w", ""} {
		t.Run("role "+role, func(t *testing.T) {
			mock := gateMem(t, role)
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

// Work a fresh read shows held (waiting, claimed by a running Operator, the user's) gets no receipt.
func TestCloseDoor_CustodySkipsWorkAFreshReadShowsHeld(t *testing.T) {
	for name, labels := range map[string][]string{
		"waiting":  {beads.Wait{Kind: beads.WaitCI, PR: 7, Since: time.Now()}.Label()},
		"operator": {"owner:operator-x"},
		"user":     {"needs-user"},
	} {
		t.Run(name, func(t *testing.T) {
			mock := gateMem(t, "manager-w", labels...)
			registerRunningOperator(t, "operator-x")
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
	mock := gateMem(t, "manager-w")
	mock.SetErr = errors.New("busy")
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
// that task's Operator, now gone): the step keeps no other holder, so it gets
// the Manager's receipt instead of reading State missing or Agent stopped.
func TestCloseDoor_ClosingTheTaskAStepWaitedOnHoldsTheStep(t *testing.T) {
	opWait := beads.Wait{Kind: beads.WaitOperator, Ref: "operator-op-x-w-2-1", Since: time.Now().Add(-time.Hour)}.Label()
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w", Title: "Notify", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.2", Title: "Send it", Status: "in_progress", Labels: []string{"kind:objective", opWait}})
	m.Seed(beads.Bead{ID: "w.2.1", Title: "Send it (task)", Status: "in_progress", Labels: []string{"kind:task"}})
	m.Edges = []beads.Dep{{IssueID: "w.2", DependsOnID: "w", Type: "parent-child"}, {IssueID: "w.2.1", DependsOnID: "w.2", Type: "parent-child"}}
	setupObjectiveProject(t, m)
	t.Setenv("COMMANDER_ROLE", "manager-w")
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
	snap := livedata.Snapshot{
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle"}},
		Workstreams: []livedata.SnapWorkstream{{BeadID: "w", Project: "manifest", Title: "Notify", Status: "open", ManagerRole: "manager-w",
			Steps: []livedata.SnapStep{{BeadID: "w.2", Title: "Send it", Status: "in_progress", State: beads.StateInProgress,
				Waiting: beads.ManagerStartWait, WaitUntil: time.Now().Add(time.Hour).Unix(), WaitUntilAs: beads.ManagerStartWait,
				Children: []livedata.SnapStep{{BeadID: "w.2.1", Title: "Send it (task)", Status: "closed", State: beads.StateDone, Task: true}}}},
		}},
	}
	row := livedata.Board(snap, time.Now().Unix()).Workstreams[0]
	if row.Text == "State missing" || strings.Contains(row.Text, "Agent stopped") {
		t.Fatalf("row %q", row.Text)
	}
}

// A close bd refuses after the receipt landed (open children, busy) takes the
// receipt back: the step is still behind the unclosed bead, and a receipt left
// there would go overdue and mail the Manager about work nobody released.
func TestCloseDoor_RefusedCloseTakesTheReceiptBack(t *testing.T) {
	mock := gateMem(t, "manager-w")
	mock.CloseErr = errors.New("has open children")
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
		label := strings.SplitN(c, "|", 3)[1]
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

// withSiblings adds n closed children of w beside the gate (the real Commander workstream has
// about 200) and, when waiting is set, n open steps that wait on the gate and carry a CI wait.
func withSiblings(m *loggedMem, n int, waiting bool) {
	for i := range n {
		id := fmt.Sprintf("w.s%03d", i)
		m.Seed(beads.Bead{ID: id, Title: "Done earlier", Status: "closed"})
		m.Edges = append(m.Edges, beads.Dep{IssueID: id, DependsOnID: "w", Type: "parent-child"})
		if waiting {
			d := fmt.Sprintf("w.d%03d", i)
			m.Seed(beads.Bead{ID: d, Title: "Also behind the gate", Status: "open", Labels: []string{beads.Wait{Kind: beads.WaitCI, PR: 7, Since: time.Now()}.Label()}})
			m.Edges = append(m.Edges, beads.Dep{IssueID: d, DependsOnID: "w", Type: "parent-child"}, beads.Dep{IssueID: d, DependsOnID: "w.9", Type: "blocks"})
		}
	}
}

// R1: a close under a parent with hundreds of children is still judged on fresh reads and its
// released step gets the Manager's receipt; the siblings cost a status list, not reads each.
func TestCloseDoor_LargeParentStillGetsCustodyInBoundedReads(t *testing.T) {
	mock := gateMem(t, "")
	withSiblings(mock, 250, false)
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	out, _ := read()
	if !strings.Contains(out, `"custody":["w.2"]`) {
		t.Fatalf("no custody under a large parent: stdout %s", out)
	}
	if mock.Reads > 60 {
		t.Fatalf("%d reads for one close under 252 siblings: want them bounded, not two per sibling", mock.Reads)
	}
	t.Logf("reads: %d", mock.Reads)
}

// R1: a closure past the fresh read's bound never fails open: the close falls back to cmdrd's
// board and still hands what it releases to the Manager.
func TestCloseDoor_ClosurePastTheBoundFallsBackToTheBoardsCustody(t *testing.T) {
	mock := gateMem(t, "")
	withSiblings(mock, maxClosure+5, true)
	orig := closeSnapshotFn
	closeSnapshotFn = func() (*livedata.Snapshot, error) {
		return &livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{BeadID: "w", Steps: []livedata.SnapStep{
			{BeadID: "w.9", Title: "Send gate", Status: "open", State: beads.StateNotStarted, Gate: true},
			{BeadID: "w.2", Title: "Send it", Status: "open", State: beads.StateNotStarted, BlockedBy: []string{"w.9"}},
		}}}}, nil
	}
	t.Cleanup(func() { closeSnapshotFn = orig })
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalCloseObjective([]string{"--project", "manifest", "--bead", "w.9"}, stdout, stderr); code != 0 {
		_, e := read()
		t.Fatal(e)
	}
	out, _ := read()
	if !strings.Contains(out, `"custody":["w.2"]`) {
		t.Fatalf("a closure past the bound failed open: stdout %s calls %v", out, mock.Calls)
	}
}
