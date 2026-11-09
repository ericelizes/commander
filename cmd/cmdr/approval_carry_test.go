package main

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/gates"
	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
)

// carrySnapshot is the board of workstream ms-1: the user approved gate ms-1.7,
// which starts ms-1.2 (and its child ms-1.2.5) and the sibling ms-1.4; ms-1.3
// waits behind the next gate ms-1.8, still open.
func carrySnapshot() *livedata.Snapshot {
	return &livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{
		Project: "manifest", BeadID: "ms-1", ManagerRole: "manager-ms-1",
		Steps: []livedata.SnapStep{
			{BeadID: "ms-1.7", Title: "Greenlight the migration", Status: "closed", Gate: true, Approvers: []string{"user"}},
			{BeadID: "ms-1.2", Status: "open", BlockedBy: []string{"ms-1.7"}, Children: []livedata.SnapStep{{BeadID: "ms-1.2.5", Status: "open"}}},
			{BeadID: "ms-1.4", Status: "open", BlockedBy: []string{"ms-1.7"}},
			{BeadID: "ms-1.8", Title: "Greenlight the switch", Status: "open", Gate: true, Approvers: []string{"user"}, BlockedBy: []string{"ms-1.2"}},
			{BeadID: "ms-1.3", Status: "open", BlockedBy: []string{"ms-1.8"}},
		},
	}}}
}

func TestSpawnOperator_ApprovalCarriesToTheStepsItCovers(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	for step, wantErr := range map[string]string{
		"ms-1.2":   "",
		"ms-1.2.5": "", // a child of a covered step
		"ms-1.4":   "", // a sibling the same gate starts
		"ms-1.3":   "ms-1 has no Workflow approved gate",
	} {
		t.Run(step, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			useMockBeadsRaw(t, &beads.MockClient{CreateID: step + ".1"})
			stubMail(t)
			withCloseSnapshot(t, carrySnapshot(), "manager-ms-1")
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", step, "--title", "t"}, stdout, stderr)
			_, errOut := read()
			if wantErr == "" && code != 0 {
				t.Fatalf("covered step refused: %s", errOut)
			}
			if wantErr != "" && (code == 0 || !strings.Contains(errOut, wantErr)) {
				t.Fatalf("step past the next gate: exit=%d stderr=%s, want %q", code, errOut, wantErr)
			}
		})
	}
}

func TestSetObjective_InProgressOnACoveredStepNeedsNoNewApproval(t *testing.T) {
	t.Setenv("COMMANDER_WORKFLOW_GATE", "enforce")
	withCloseSnapshot(t, carrySnapshot(), "manager-ms-1")
	m := &beads.MockClient{}
	if err := requireWorkflowGate(context.Background(), m, "", "ms-1.2", io.Discard); err != nil {
		t.Fatalf("covered: %v", err)
	}
	if err := requireWorkflowGate(context.Background(), m, "", "ms-1.3", io.Discard); err == nil {
		t.Fatal("a step behind the next gate passed")
	}
	// No readable board: no carry-over, the old rule stands.
	withCloseSnapshot(t, nil, "manager-ms-1")
	if err := requireWorkflowGate(context.Background(), m, "", "ms-1.2", io.Discard); err == nil {
		t.Fatal("no board, yet the step passed")
	}
}

func TestSpawnOperator_ProdGateCoveringTheStepIsLinked(t *testing.T) {
	labels := []string{gates.KindLabel, "approver:user"} // no gate-of, no edge
	for step, wantErr := range map[string]string{"ms-1.4": "", "ms-1.3": "not linked"} {
		t.Run(step, func(t *testing.T) {
			sp := &fakeOperatorSpawner{}
			stubSpawnDeps(t, sp, 0, 3)
			useMockBeads(t, prodGateMock("● P2 · CLOSED", labels, "run migration 42"))
			stubMail(t)
			withCloseSnapshot(t, carrySnapshot(), "manager-ms-1")
			stdout, stderr, read := withTempOutputFiles(t)
			code := runInternalSpawnOperator([]string{"--project", "manifest", "--step", step, "--title", "t", "--prod-gate", "ms-1.7"}, stdout, stderr)
			_, errOut := read()
			if wantErr == "" && code != 0 {
				t.Fatalf("covered step refused: %s", errOut)
			}
			if wantErr != "" && (code == 0 || !strings.Contains(errOut, wantErr)) {
				t.Fatalf("exit=%d stderr=%s, want %q", code, errOut, wantErr)
			}
		})
	}
}

// askBoard seeds ms-1's beads and board for the needs-user door.
func askBoard(t *testing.T, extra ...beads.Bead) *beads.MemClient {
	t.Helper()
	m := &beads.MemClient{}
	for _, b := range []beads.Bead{
		{ID: "ms-1", Title: "Area", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "ms-1.7", Title: "Greenlight the migration", Status: "closed", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", beads.DecisionAnsweredLabel}},
		{ID: "ms-1.2", Title: "Migrate", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "ms-1.4", Title: "Backfill", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "ms-1.8", Title: "Greenlight the switch", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user"}},
		{ID: "ms-1.3", Title: "Switch", Status: "open", Labels: []string{"kind:objective"}},
		{ID: "ms-1.9", Title: "Unrelated", Status: "open", Labels: []string{"kind:objective"}},
	} {
		m.Seed(b)
	}
	for _, b := range extra {
		m.Seed(b)
	}
	setupObjectiveProject(t, m)
	stubMail(t)
	snap := carrySnapshot()
	snap.Workstreams[0].Steps = append(snap.Workstreams[0].Steps, livedata.SnapStep{BeadID: "ms-1.9", Status: "open"})
	withCloseSnapshot(t, snap, "manager-ms-1")
	return m
}

const approveCard = `{"question": "Run the migration?", "context": "Ready.", "options": [{"label": "Run it", "approves": true}, {"label": "Not yet"}]}`

func ask(bead, card string, more ...string) []string {
	return append([]string{"--project", "manifest", "--bead", bead, "--state", beads.StateNeedsUser, "--decision", card}, more...)
}

func asked(t *testing.T, args []string) {
	t.Helper()
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalSetObjective(args, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("%v refused: %s", args, errOut)
	}
}

func reviewWait(login string) string {
	return beads.Wait{Kind: beads.WaitReview, Ref: login, Since: time.Unix(1_700_000_000, 0)}.Label()
}

func TestAskDoor_WaitOnAReviewerHoldsTheCard(t *testing.T) {
	askBoard(t, beads.Bead{ID: "ms-1.5", Title: "Merge", Status: "open", Labels: []string{"kind:objective", "pr:4021", reviewWait("rev-one")}})
	refused(t, ask("ms-1.5", `{"question": "Merge it?", "options": [{"label": "Merge"}]}`), "this is waiting on review by rev-one; the user already said wait")
	// The same PR raised from another bead is the same subject.
	refused(t, ask("ms-1.9", `{"question": "Merge PR #4021 now?", "options": [{"label": "Merge"}]}`), "PR #4021 is waiting on review by rev-one")
}

func TestAskDoor_PassedTimeAndCloseTimerHoldNothing(t *testing.T) {
	past := beads.Wait{Kind: beads.WaitUntil, Ref: "the window opens", Until: time.Now().Add(-time.Hour), Since: time.Now().Add(-2 * time.Hour)}.Label()
	closer := beads.Wait{Kind: beads.WaitUntil, Ref: beads.ManagerCloseWait, Until: time.Now().Add(time.Hour), Since: time.Now()}.Label()
	askBoard(t, beads.Bead{ID: "ms-1.6", Title: "Check", Status: "open", Labels: []string{"kind:objective", past, closer}})
	asked(t, ask("ms-1.6", `{"question": "Use the new window?", "options": [{"label": "Yes"}]}`))
	future := beads.Wait{Kind: beads.WaitUntil, Ref: "the window opens", Until: time.Now().Add(time.Hour), Since: time.Now()}.Label()
	askBoard(t, beads.Bead{ID: "ms-1.6", Title: "Check", Status: "open", Labels: []string{"kind:objective", future}})
	refused(t, ask("ms-1.6", `{"question": "Use the new window?", "options": [{"label": "Yes"}]}`), "this is waiting on the window opens")
}

// A step a close released or a create made carries the Manager's "Manager to
// start" receipt for an hour; a card is one of the successors that clears it,
// so the receipt never refuses one (nor does a dispatch receipt). The card
// also takes the receipt off: a card is a real successor.
func TestAskDoor_ManagerCustodyReceiptsHoldNothing(t *testing.T) {
	for _, ref := range []string{beads.ManagerStartWait, beads.ManagerDispatchWait} {
		t.Run(ref, func(t *testing.T) {
			receipt := beads.Wait{Kind: beads.WaitUntil, Ref: ref, Until: time.Now().Add(time.Hour), Since: time.Now()}.Label()
			m := askBoard(t, beads.Bead{ID: "ms-1.6", Title: "Send the notice", Status: "open", Labels: []string{"kind:objective", receipt}})
			asked(t, ask("ms-1.6", `{"question": "Send it today?", "options": [{"label": "Yes"}]}`))
			b, _ := m.Show(t.Context(), "", "ms-1.6")
			if slices.Contains(b.Labels, receipt) {
				t.Fatalf("the card left the receipt: %v", b.Labels)
			}
		})
	}
}

func TestAskDoor_AnsweredStepNeedsAnExplicitReopen(t *testing.T) {
	askBoard(t, beads.Bead{ID: "ms-1.6", Title: "Pick", Status: "open", Labels: []string{"kind:objective", beads.DecisionAnsweredLabel}})
	refused(t, ask("ms-1.6", `{"question": "Which one?", "options": [{"label": "A"}]}`), "the user already answered its card")
	asked(t, ask("ms-1.6", `{"question": "Which one?", "options": [{"label": "A"}]}`, "--status", "open"))
}

func TestAskDoor_ReworkedGateAfterChangeAsksAgain(t *testing.T) {
	askBoard(t, beads.Bead{ID: "ms-1.10", Title: "Greenlight the rework", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", beads.DecisionAnsweredLabel}})
	asked(t, ask("ms-1.10", approveCard))
}

func TestAskDoor_ApprovalCoveringTheStepIsNotAskedAgain(t *testing.T) {
	askBoard(t)
	refused(t, ask("ms-1.4", approveCard), "the user already approved ms-1.7, and that approval covers ms-1.4")
	// A question that approves nothing is still a fair ask on covered work.
	asked(t, ask("ms-1.2", `{"question": "Which table first?", "options": [{"label": "Users"}, {"label": "Orders"}]}`))
}

func TestAskDoor_StepBehindAnOpenGateUsesThatCard(t *testing.T) {
	askBoard(t)
	refused(t, ask("ms-1.3", `{"question": "Switch now?", "options": [{"label": "Yes"}]}`), "behind the user's gate ms-1.8, which is still open")
	// The gate's own card is the decision: writing it passes.
	asked(t, ask("ms-1.8", approveCard))
}

func TestAskDoor_SamePRAsAnOpenOrAnsweredCardIsADuplicate(t *testing.T) {
	askBoard(t, beads.Bead{ID: "ms-1.5", Title: "Merge", Status: "open", Labels: []string{"kind:objective", "pr:4021", beads.DecisionAnsweredLabel}})
	refused(t, ask("ms-1.9", `{"question": "Merge PR #4021?", "options": [{"label": "Merge"}]}`), "ms-1.5 already has a decision about PR #4021")
	asked(t, ask("ms-1.9", `{"question": "Merge pull request #4022?", "options": [{"label": "Merge"}]}`))
}

func TestAskDoor_SamePROnTheBoardsOpenCards(t *testing.T) {
	askBoard(t)
	snap := carrySnapshot()
	snap.Workstreams[0].Steps = append(snap.Workstreams[0].Steps, livedata.SnapStep{BeadID: "ms-1.9", Status: "open"})
	snap.NeedsYou = []livedata.NeedsYou{{Project: "manifest", BeadID: "ms-1.4", Decision: decisionOf(t, `{"question": "Merge PR 4021?", "options": [{"label": "Merge"}]}`)}}
	withCloseSnapshot(t, snap, "manager-ms-1")
	refused(t, ask("ms-1.9", `{"question": "Ship it?", "context": "github.com/o/r/pull/4021", "options": [{"label": "Ship"}]}`), "the open card on ms-1.4 already asks about PR #4021")
}

func TestAskDoor_TheUsersOwnMarkIsNotRefused(t *testing.T) {
	askBoard(t, beads.Bead{ID: "ms-1.6", Title: "Pick", Status: "open", Labels: []string{"kind:objective", beads.DecisionAnsweredLabel}})
	t.Setenv("COMMANDER_ROLE", "")
	asked(t, ask("ms-1.6", `{"question": "Which one?", "options": [{"label": "A"}]}`))
}

func decisionOf(t *testing.T, raw string) *store.Decision {
	t.Helper()
	d, err := store.ParseDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAskDoor_SidebarNumbersAreNotPRs(t *testing.T) {
	askBoard(t,
		beads.Bead{ID: "ms-1.5", Title: "Merge", Status: "open", Labels: []string{"kind:objective", "pr:2", reviewWait("rev-one")}},
		beads.Bead{ID: "ms-1.6", Title: "Pick", Status: "open", Labels: []string{"kind:objective", "pr:3", beads.DecisionAnsweredLabel}},
	)
	asked(t, ask("ms-1.9", `{"question": "Start #2 Sample text trials after #3A?", "context": "#8 Commander is idle.", "options": [{"label": "Start"}]}`))
}

func TestPRRefs_OnlyWordsThatNameAPR(t *testing.T) {
	d := decisionOf(t, `{"question": "Merge PR #11, pr 12 and Pull Request #13?", "context": "see https://github.com/o/r/pull/14; #2 Sample text trials, #3A, #8 Commander", "options": [{"label": "Merge"}]}`)
	got := prRefs(d, []string{"pr:15"})
	want := map[string]bool{"11": true, "12": true, "13": true, "14": true, "15": true}
	if len(got) != len(want) {
		t.Fatalf("prRefs = %v, want %v", got, want)
	}
	for n := range want {
		if !got[n] {
			t.Fatalf("prRefs = %v, want %v", got, want)
		}
	}
}

const prCard = `{"question": "Is PR #4116 confirmed done?", "options": [{"label": "Yes"}]}`

func trackAndGate(trackLabels ...string) []beads.Bead {
	return []beads.Bead{
		{ID: "ms-1.20", Title: "Track", Status: "open", Labels: append([]string{"kind:objective", beads.TrackLabel, "pr:4116"}, trackLabels...)},
		{ID: "ms-1.20.1", Title: "Confirmed done", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", "pr:4116"}},
	}
}

func TestAskDoor_GateTakesTheCardWhenTheTracksDecisionIsSettled(t *testing.T) {
	askBoard(t, trackAndGate(beads.DecisionAnsweredLabel)...)
	asked(t, ask("ms-1.20.1", prCard))
}

func TestAskDoor_OpenCardOnTheTrackMovesDownToTheGate(t *testing.T) {
	askBoard(t, trackAndGate("needs-user")...)
	refused(t, ask("ms-1.20.1", prCard), "the ancestor ms-1.20 already has an open card about PR #4116")
	refused(t, ask("ms-1.20.1", prCard), "cmdr internal-move-card --project manifest --from ms-1.20 --to ms-1.20.1")
	refused(t, ask("ms-1.20.1", prCard), "--not-the-same-ask followed by a quoted sentence")
}

func TestAskDoor_ADifferentQuestionOnTheSamePRMayBeAsked(t *testing.T) {
	askBoard(t, trackAndGate("needs-user")...)
	asked(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", "this asks about the deploy window"))
}

func TestAskDoor_StaleSnapshotCardOfAnAnsweredTrackIsNoRefusal(t *testing.T) {
	askBoard(t, trackAndGate(beads.DecisionAnsweredLabel)...)
	snap := carrySnapshot()
	snap.NeedsYou = []livedata.NeedsYou{{Project: "manifest", BeadID: "ms-1.20", Decision: decisionOf(t, prCard)}}
	withCloseSnapshot(t, snap, "manager-ms-1")
	asked(t, ask("ms-1.20.1", prCard))
}

func TestMoveCard_ClearsTheTrackUnderItsManagerThenTheGateTakesIt(t *testing.T) {
	m := askBoard(t, trackAndGate("needs-user")...)
	snap := carrySnapshot()
	snap.NeedsYou = []livedata.NeedsYou{{Project: "manifest", BeadID: "ms-1.20", Decision: decisionOf(t, prCard)}}
	withCloseSnapshot(t, snap, "manager-ms-1")
	var calls [][]string
	orig := setObjectiveFn
	setObjectiveFn = func(args []string, o, e *os.File) int {
		calls = append(calls, args)
		return orig(args, o, e)
	}
	t.Cleanup(func() { setObjectiveFn = orig })
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalMoveCard([]string{"--project", "manifest", "--from", "ms-1.20", "--to", "ms-1.20.1"}, stdout, stderr); code != 0 {
		_, errOut := read()
		t.Fatalf("move refused: %s (calls %v)", errOut, calls)
	}
	if len(calls) != 2 || strings.Join(calls[0], " ") != "--project manifest --bead ms-1.20 --state in-progress --owner manager-ms-1 --writes-workflow" {
		t.Fatalf("calls = %v", calls)
	}
	if calls[1][3] != "ms-1.20.1" || !strings.HasPrefix(calls[1][len(calls[1])-1], "@") {
		t.Fatalf("second call = %v", calls[1])
	}
	gate, _ := m.Show(context.Background(), "", "ms-1.20.1")
	if gate == nil || !slices.Contains(gate.Labels, "needs-user") {
		t.Fatalf("gate = %+v", gate)
	}
}

func TestMoveCard_RefusesWithNothingWritten(t *testing.T) {
	askBoard(t, trackAndGate("needs-user")...)
	withCloseSnapshot(t, carrySnapshot(), "manager-ms-1") // no card on the track
	stdout, stderr, read := withTempOutputFiles(t)
	if code := runInternalMoveCard([]string{"--project", "manifest", "--from", "ms-1.20", "--to", "ms-1.20.1"}, stdout, stderr); code == 0 {
		t.Fatal("moved a card that is not there")
	}
	if _, e := read(); !strings.Contains(e, "holds no open card") {
		t.Fatalf("stderr = %q", e)
	}
	// A gate that is not under the ancestor.
	stdout, stderr, read = withTempOutputFiles(t)
	if code := runInternalMoveCard([]string{"--project", "manifest", "--from", "ms-1.20", "--to", "ms-1.8"}, stdout, stderr); code == 0 {
		t.Fatal("moved to a gate outside the ancestor")
	}
	if _, e := read(); !strings.Contains(e, "is not under") {
		t.Fatalf("stderr = %q", e)
	}
}

func TestAskDoor_OpenCardOnTheTrackShowsOnTheBoardToo(t *testing.T) {
	askBoard(t, trackAndGate("needs-user")...)
	snap := carrySnapshot()
	snap.NeedsYou = []livedata.NeedsYou{{Project: "manifest", BeadID: "ms-1.20", Decision: decisionOf(t, prCard)}}
	withCloseSnapshot(t, snap, "manager-ms-1")
	refused(t, ask("ms-1.20.1", prCard), "the ancestor ms-1.20 already has an open card")
}

func TestAskDoor_SiblingWithTheSamePRIsStillADuplicate(t *testing.T) {
	askBoard(t,
		beads.Bead{ID: "ms-1.20", Title: "Track", Status: "open", Labels: []string{"kind:objective", beads.TrackLabel}},
		beads.Bead{ID: "ms-1.20.1", Title: "Confirmed done", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", "pr:4116"}},
		beads.Bead{ID: "ms-1.21.1", Title: "Other gate", Status: "open", Labels: []string{"kind:objective", beads.GateLabel, "approver:user", "pr:4116", "needs-user"}},
	)
	refused(t, ask("ms-1.20.1", prCard), "ms-1.21.1 already has a decision about PR #4116")
}

type failingComments struct{ *beads.MemClient }

func (failingComments) AddComment(context.Context, string, string, string) error {
	return errors.New("comment write failed")
}

func commentTexts(t *testing.T, m *beads.MemClient, id string) []string {
	t.Helper()
	cs, _ := m.Comments(context.Background(), "", id)
	var out []string
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestAskDoor_NotTheSameAskWritesANoteOnGateAndAncestor(t *testing.T) {
	m := askBoard(t, trackAndGate("needs-user")...)
	asked(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", "this asks about the deploy window"))
	for _, id := range []string{"ms-1.20.1", "ms-1.20"} {
		got := strings.Join(commentTexts(t, m, id), "\n")
		if !strings.Contains(got, "second card allowed: not the same ask as ms-1.20: this asks about the deploy window") || !strings.Contains(got, "manager-ms-1") {
			t.Fatalf("%s comments = %q", id, got)
		}
	}
}

func TestAskDoor_NoteFailureBlocksTheCard(t *testing.T) {
	m := askBoard(t, trackAndGate("needs-user")...)
	newBeadsClientFn = func() beads.Client { return failingComments{m} }
	refused(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", "different"), "The card was not written")
	gate, _ := m.Show(context.Background(), "", "ms-1.20.1")
	if slices.Contains(gate.Labels, "needs-user") {
		t.Fatalf("card written without its note: %+v", gate)
	}
}

func TestAskDoor_NotTheSameAskReasonMustBeRealAndShort(t *testing.T) {
	askBoard(t, trackAndGate("needs-user")...)
	refused(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", "   "), "it was empty")
	refused(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", strings.Repeat("x", 501)), "keep it to 500")
}

func TestAskDoor_NoNoteWhenTheRefusalDoesNotApply(t *testing.T) {
	m := askBoard(t, trackAndGate(beads.DecisionAnsweredLabel)...)
	asked(t, ask("ms-1.20.1", prCard, "--not-the-same-ask", "unneeded"))
	if n := len(commentTexts(t, m, "ms-1.20.1")) + len(commentTexts(t, m, "ms-1.20")); n != 0 {
		t.Fatalf("%d notes written", n)
	}
}
