package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/livedata"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/wake"
)

type memIssues struct{ rows []store.WatchIssue }

func (m *memIssues) WatchIssues(context.Context) ([]store.WatchIssue, error) { return m.rows, nil }
func (m *memIssues) SetWatchIssues(_ context.Context, rows []store.WatchIssue) error {
	m.rows = rows
	return nil
}

// stalledSnap: one workstream with open work, nobody on it, its Manager idle.
func stalledSnap() livedata.Snapshot {
	return livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{BeadID: "c-1", Title: "Commander", ManagerRole: "manager-c-1", Steps: []livedata.SnapStep{
			{BeadID: "c-1.1", Title: "Desktop app fixes", State: "in-progress", Status: "open", Children: []livedata.SnapStep{
				{BeadID: "c-1.1.5", Title: "Chats survive restarts", State: "review", Status: "open", Owner: "manager-c-1"},
			}},
		}}},
		Agents: map[string]livedata.SnapAgent{"manager-c-1": {State: "idle"}},
	}
}

func commanderWatch(snap livedata.Snapshot, start time.Time) (*opWatch, *time.Time, *[]said, *memIssues, *fakeWatchStore) {
	now := start
	st := &fakeWatchStore{watches: map[int64]store.RunWatch{}, checkins: map[string]store.Checkin{}}
	issues := &memIssues{}
	var delivered []said
	w := &opWatch{
		store: st, issues: issues, now: func() time.Time { return now },
		activeAt: func(string) (time.Time, bool) { return time.Time{}, false },
		latest:   func() (livedata.Snapshot, bool) { return snap, true },
		deliver: func(_ context.Context, role, prompt string) error {
			delivered = append(delivered, said{role, prompt})
			return nil
		},
	}
	return w, &now, &delivered, issues, st
}

func TestCommanderHearsOfAStalledWorkstreamOnceItHasLasted(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	w, now, delivered, issues, _ := commanderWatch(stalledSnap(), start)
	w.pass(context.Background())
	if len(*delivered) != 0 {
		t.Fatalf("woke Commander for a fresh stall: %v", *delivered)
	}
	if len(issues.rows) != 1 || issues.rows[0].Active || issues.rows[0].Since != start.Unix() {
		t.Fatalf("issues = %+v", issues.rows)
	}
	*now = start.Add(31 * time.Minute)
	w.pass(context.Background())
	if len(*delivered) != 1 || (*delivered)[0].role != "commander" {
		t.Fatalf("delivered = %v", *delivered)
	}
	got := (*delivered)[0].text
	want := "- Commander: stalled: nobody on its open work; finished work to review: Chats survive restarts, 31m\n"
	if !strings.HasPrefix(got, "[mail] cmdrd: Check-in: 1 workstream, 1 needs attention\n") || !strings.Contains(got, want) {
		t.Fatalf("prompt = %q", got)
	}
	// Told once: the next pass is quiet.
	*now = start.Add(32 * time.Minute)
	w.pass(context.Background())
	if len(*delivered) != 1 {
		t.Fatalf("told twice: %v", *delivered)
	}
}

func TestCommanderCheckInWaitsWhileUserTalks(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	w, now, _, issues, _ := commanderWatch(stalledSnap(), start)
	w.pass(context.Background())
	w.deliver = func(context.Context, string, string) error { return wake.ErrDeferred }
	*now = start.Add(31 * time.Minute)
	w.pass(context.Background())
	if !issues.rows[0].Active || issues.rows[0].Told {
		t.Fatalf("deferred issue marked told: %+v", issues.rows)
	}
}

func TestCommanderTimedCheckInOnlyWhileWorkMoves(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	snap := livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{BeadID: "t-1", Title: "Import pipeline", ManagerRole: "manager-t-1", Steps: []livedata.SnapStep{
			{BeadID: "t-1.1", Title: "CI green", State: "in-progress", Status: "open", Waiting: "CI #4101", WaitSince: start.Unix()},
		}}},
		Agents: map[string]livedata.SnapAgent{"manager-t-1": {State: "waiting", Wait: "CI #4101"}},
	}
	w, now, delivered, _, st := commanderWatch(snap, start)
	w.pass(context.Background())
	if st.checkins["commander"].Due != start.Add(CommanderCheckinDefault).Unix() {
		t.Fatalf("checkin = %+v", st.checkins["commander"])
	}
	*now = start.Add(CommanderCheckinDefault)
	w.pass(context.Background())
	// CI has run 30m by now: that is the issue the check-in carries.
	if len(*delivered) != 1 || !strings.Contains((*delivered)[0].text, "- Import pipeline: CI still running (CI green), 30m") {
		t.Fatalf("delivered = %v", *delivered)
	}
}

func TestCommanderAllQuietIsNotWoken(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	snap := livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{BeadID: "q-1", Title: "Quiet", ManagerRole: "manager-q-1"}}}
	w, now, delivered, _, _ := commanderWatch(snap, start)
	w.pass(context.Background())
	*now = start.Add(2 * CommanderCheckinDefault)
	w.pass(context.Background())
	if len(*delivered) != 0 {
		t.Fatalf("woke Commander with nothing moving: %v", *delivered)
	}
}

// Sample text trials shape: open work sits behind an open gate the user approves
// (Workflow approved). NEEDS YOU shows the gate, so it is not a stall.
func TestCommanderDoesNotCallAnUserGateAStall(t *testing.T) {
	snap := stalledSnap()
	snap.Workstreams[0].Steps = append([]livedata.SnapStep{
		{BeadID: "c-1.0", Title: "Workflow approved", State: "not-started", Status: "open", Gate: true, Approvers: []string{"user"}},
	}, snap.Workstreams[0].Steps...)
	cands, _ := commanderCandidates(snap)
	for _, c := range cands {
		if strings.Contains(c.key, ":stalled:") {
			t.Fatalf("stalled candidate behind an the user gate: %+v", c)
		}
	}
	// A gate someone else approves is not the user's to answer: still a stall.
	snap.Workstreams[0].Steps[0].Approvers = []string{"reviewer-a"}
	cands, _ = commanderCandidates(snap)
	found := false
	for _, c := range cands {
		found = found || strings.Contains(c.key, ":stalled:")
	}
	if !found {
		t.Fatal("real stall behind another approver's gate was dropped")
	}
}

func TestCommanderPromptCarriesRowsAndAudit(t *testing.T) {
	snap := livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{
			N: 1, Project: "p", BeadID: "w", Title: "Alpha", ManagerRole: "manager-w",
			Steps: []livedata.SnapStep{{BeadID: "w.1", Title: "Orphan", Status: "open", State: "not-started"}},
		}},
		Agents: map[string]livedata.SnapAgent{"manager-w": {State: "idle", Since: 1}},
	}
	got := commanderPrompt(nil, nil, snap, time.Unix(1000, 0))
	for _, want := range []string{"action needed", "1 | Alpha |", "NEEDS YOU (0)", "Sanity audit: 1 suspect\n", "w.1 | manager-w |"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// A step a Manager left waiting on Commander is Commander's own queue: it wakes
// Commander (it is not "work moving"), named as Commander's to do.
func TestCommanderIsWokenForStepsWaitingOnIt(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	snap := livedata.Snapshot{
		Workstreams: []livedata.SnapWorkstream{{BeadID: "r-1", Title: "Example app ops", ManagerRole: "manager-r-1", Steps: []livedata.SnapStep{
			{BeadID: "r-1.1", Title: "Stuck import job", State: "in-progress", Status: "open", Waiting: "commander", WaitSince: start.Add(-10 * time.Hour).Unix()},
		}}},
		Agents: map[string]livedata.SnapAgent{"manager-r-1": {State: "idle"}},
	}
	w, _, delivered, _, _ := commanderWatch(snap, start)
	w.pass(context.Background())
	if len(*delivered) != 1 {
		t.Fatalf("delivered = %v", *delivered)
	}
	got := (*delivered)[0].text
	if !strings.Contains(got, "- Example app ops: waiting on you (Commander): Stuck import job, 600m") || !strings.Contains(got, "is yours: do it now") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestCommanderBusyManagerCannotResetEvidenceClockWithRecoveryTasks(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	snap := stalledSnap()
	snap.Agents["manager-c-1"] = livedata.SnapAgent{State: store.AgentWorking}
	w, now, delivered, issues, _ := commanderWatch(snap, start)
	w.latest = func() (livedata.Snapshot, bool) { return snap, true }
	w.pass(context.Background())
	if len(issues.rows) != 1 || !strings.Contains(issues.rows[0].Key, ":stalled:") || !strings.Contains(issues.rows[0].Text, "without new recorded stage/gate evidence") {
		t.Fatalf("issues %+v", issues.rows)
	}
	for i := 1; i <= 31; i++ {
		*now = start.Add(time.Duration(i) * time.Minute)
		state := store.AgentWorking
		if i%2 == 0 {
			state = store.AgentIdle
		}
		snap.Agents["manager-c-1"] = livedata.SnapAgent{State: state, Since: now.Unix()}
		snap.Workstreams[0].Steps[0].Children = append(snap.Workstreams[0].Steps[0].Children, livedata.SnapStep{BeadID: "recovery", Task: true, Status: "closed"})
		w.pass(context.Background())
	}
	if len(*delivered) != 1 || !strings.Contains((*delivered)[0].text, "30m") {
		t.Fatalf("delivered %v", *delivered)
	}
	if issues.rows[0].Since != start.Unix() {
		t.Fatal("administrative churn reset evidence clock")
	}
	// A new stage gate receipt is evidence and starts a fresh window.
	snap.Workstreams[0].Steps[0].Gates = []livedata.SnapGate{{Name: "review", Status: "passed", Evidence: "review receipt", At: now.Unix()}}
	*now = now.Add(time.Minute)
	w.pass(context.Background())
	if len(issues.rows) != 1 || issues.rows[0].Active || issues.rows[0].Since != now.Unix() {
		t.Fatalf("new evidence not recognized: %+v", issues.rows)
	}
}

func TestCommanderEvidenceWatchLeavesActualOperatorAndExternalWaitsToTheirWatches(t *testing.T) {
	for _, mode := range []string{"operator", "CI", "you"} {
		t.Run(mode, func(t *testing.T) {
			snap := stalledSnap()
			snap.Agents["manager-c-1"] = livedata.SnapAgent{State: store.AgentWorking}
			leaf := &snap.Workstreams[0].Steps[0].Children[0]
			if mode == "operator" {
				leaf.Owner = "operator-task"
			} else {
				leaf.Waiting = mode
			}
			cands, _ := commanderCandidates(snap)
			for _, c := range cands {
				if strings.Contains(c.key, ":stalled:") {
					t.Fatalf("unnecessary evidence wake for %s", mode)
				}
			}
		})
	}
}

// Each case is one snapshot through commanderPrompt; the two lines are compared whole,
// so an instruction naming an action nothing needs fails.
func TestCheckinHeadlineScenario(t *testing.T) {
	raw, err := os.ReadFile("../../tests/evals/scenarios/checkin-headline.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Head, Instruction string
			Now                     int64
			Snapshot                livedata.Snapshot
			SuspectLine             bool     `json:"suspect_line"`
			StalledLines            []string `json:"stalled_lines"`
			MismatchLines           []string `json:"mismatch_lines"`
			HeldLines               []string `json:"held_lines"`
		}
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if len(scenario.Cases) == 0 {
		t.Fatal("scenario has no cases")
	}
	const suspectLine = "For each sanity-audit suspect:"
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			now := c.Now
			if now == 0 {
				now = 1000
			}
			got := commanderPrompt(nil, nil, c.Snapshot, time.Unix(now, 0))
			lines := strings.SplitN(got, "\n", 3)
			workstreams := len(c.Snapshot.Workstreams)
			head := fmt.Sprintf("[mail] cmdrd: Check-in: %d workstream%s, %s", workstreams, plural(workstreams), c.Head)
			if lines[0] != head {
				t.Errorf("head\n got %s\nwant %s", lines[0], head)
			}
			if strings.TrimSpace(lines[1]) != c.Instruction {
				t.Errorf("instruction\n got %s\nwant %s", lines[1], c.Instruction)
			}
			if strings.Contains(got, suspectLine) != c.SuspectLine {
				t.Errorf("suspect line present = %v, want %v:\n%s", !c.SuspectLine, c.SuspectLine, got)
			}
			if lines := reportList(got, livedata.StalledHeader); !slices.Equal(lines, c.StalledLines) {
				t.Errorf("stalled lines\n got %q\nwant %q\n%s", lines, c.StalledLines, got)
			}
			if lines := reportList(got, livedata.MismatchHeader); !slices.Equal(lines, c.MismatchLines) {
				t.Errorf("mismatch lines\n got %q\nwant %q\n%s", lines, c.MismatchLines, got)
			}
			if lines := reportList(got, livedata.HeldHeader); !slices.Equal(lines, c.HeldLines) {
				t.Errorf("held lines\n got %q\nwant %q\n%s", lines, c.HeldLines, got)
			}
		})
	}
}

// reportList is the "- " lines under the report's header, nil when it has no such list.
func reportList(report, header string) []string {
	_, rest, ok := strings.Cut(report, "\n"+header)
	if !ok {
		return nil
	}
	var out []string
	for _, l := range strings.Split(rest, "\n")[1:] {
		if !strings.HasPrefix(l, "- ") {
			break
		}
		out = append(out, l)
	}
	return out
}

// A held-mail or unrouted-mail issue reads as action needed, with or without findings beside it.
func TestCommanderPromptHealthIssueIsActionNeeded(t *testing.T) {
	for name, detail := range map[string]string{
		"mail-history": "3 mails held on 1 role with no read cursor\nMay hold a decision answer: manager-x: \"[decision] y\"",
		"no-manager":   "Mail can't reach manager-x: [no Manager] [wait] event",
	} {
		for _, findings := range [][]livedata.DiagramFinding{nil, {{Check: "unheld"}}} {
			t.Run(name, func(t *testing.T) {
				snap := livedata.Snapshot{
					Health:  []livedata.SnapHealth{{Key: name, Detail: detail, Since: 900}},
					Display: &livedata.Display{Workstreams: []livedata.BoardRow{{Diagram: &livedata.Diagram{Findings: findings}}}},
				}
				got := commanderPrompt(nil, nil, snap, time.Unix(1000, 0))
				head, _, _ := strings.Cut(got, "\n")
				if !strings.Contains(head, "NOT all on track, action needed: 1 health problem") || strings.Contains(got, "Nothing is required") {
					t.Fatal(got)
				}
				if !strings.Contains(got, "Fix each HEALTH problem") || !strings.Contains(got, strings.SplitN(detail, "\n", 2)[0]) {
					t.Fatal(got)
				}
			})
		}
	}
}

// A stopped agent whose row carries no finding and no suspect is still not on track.
func TestCommanderPromptStoppedAgentIsActionNeeded(t *testing.T) {
	raw, err := os.ReadFile("../livedata/testdata/board/liveness.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Now      int64             `json:"now"`
		Snapshot livedata.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, bead := range []string{"app-w1", "app-w6"} {
		t.Run(bead, func(t *testing.T) {
			snap := fx.Snapshot
			snap.Workstreams = slices.DeleteFunc(slices.Clone(fx.Snapshot.Workstreams), func(w livedata.SnapWorkstream) bool { return w.BeadID != bead })
			snap.NeedsYou = nil
			got := commanderPrompt(nil, nil, snap, time.Unix(fx.Now, 0))
			if !strings.Contains(got, "1 stopped or stalled") || !strings.Contains(got, "Agent stopped on") || strings.Contains(got, "all on track") || strings.Contains(got, "Nothing is required") {
				t.Fatal(got)
			}
		})
	}
}

// The check-in's first line follows the issues, so a hold must quiet them too: a stalled or
// stuck held workstream raises none. Waiting on Commander is Commander's own move and stays.
func TestCommanderCandidatesHoldQuietsOnlyWhatNobodyIsMoving(t *testing.T) {
	keys := func(snap livedata.Snapshot) []string {
		cands, _ := commanderCandidates(snap)
		var out []string
		for _, c := range cands {
			out = append(out, strings.SplitN(c.key, ":", 3)[1])
		}
		return out
	}
	snap := stalledSnap()
	snap.Agents["manager-c-1"] = livedata.SnapAgent{State: store.AgentStuck, Since: 1}
	if got := keys(snap); !slices.Equal(got, []string{"stuck", "stalled"}) {
		t.Fatalf("no hold: %v", got)
	}
	snap.Workstreams[0].Hold = true
	if got := keys(snap); len(got) != 0 {
		t.Fatalf("held: %v", got)
	}
	snap.Workstreams[0].Steps[0].Children[0].Waiting = "commander"
	snap.Workstreams[0].Steps[0].Children[0].WaitSince = 1
	if got := keys(snap); !slices.Equal(got, []string{"commander"}) {
		t.Fatalf("held, waiting on Commander: %v", got)
	}
}
