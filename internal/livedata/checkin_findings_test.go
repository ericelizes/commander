package livedata

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestCheckinFindingCountsIncludeTracksOnce(t *testing.T) {
	fs := []DiagramFinding{{Check: CheckNeedsYouLane}, {Check: "MERGE_REVIEW_INCOMPLETE"}, {Check: "MERGE_REVIEW_REQUIRED"}, {Check: "MERGE_REVIEW_REQUIRED", Advisory: true}}
	s := Snapshot{Display: &Display{Workstreams: []BoardRow{{Diagram: &Diagram{Findings: fs, Tracks: []Diagram{{Findings: fs}}}}}}}
	errors, blockers, advisories := FindingCounts(s, 1000)
	if errors != 1 || blockers != 2 || advisories != 1 {
		t.Fatalf("counts = %d/%d/%d", errors, blockers, advisories)
	}
	report := CheckinReport(s, 1000, AuditOptions{})
	if !strings.Contains(report, "Workflow errors: 1\nBlockers: 2\nAdvisories: 1") {
		t.Fatal(report)
	}
}

// The count reads claims after liveness: an Operator cmdrd did not find has stopped, beside
// a card, while the same claim on a live Operator holds its step.
func TestStalledWorkReadsLiveness(t *testing.T) {
	ws := SnapWorkstream{N: 1, Project: "p", BeadID: "w", Title: "Alpha", ManagerRole: "manager-w", Steps: []SnapStep{
		{BeadID: "w.1", Title: "Build", Status: "open", State: "in-progress", Owner: "operator-x"},
		{BeadID: "w.2", Title: "Plan approved", Status: "open", Gate: true, Approvers: []string{"user"}},
	}}
	s := snapOf(ws, SnapAgent{State: "idle", Since: 1}, NeedsYou{Project: "p", BeadID: "w.2", Workstream: "Alpha", Title: "Plan approved"})
	s.Live = &SnapLive{At: 900, Operators: []string{"operator-x"}}
	if got := StalledWork(s, 1000); len(got) != 0 {
		t.Fatalf("live Operator: %+v", got)
	}
	s.Live = &SnapLive{At: 900, Operators: []string{}}
	got := StalledWork(s, 1000)
	if len(got) != 1 || got[0].Bead != "w.1" || !strings.Contains(got[0].Reason, "Agent stopped on build") {
		t.Fatalf("stopped Operator: %+v", got)
	}
	if report := CheckinReport(s, 1000, AuditOptions{}); !strings.Contains(report, "\n- w.1 | Alpha (w) | Build | Agent stopped on build") {
		t.Fatal(report)
	}
}

// A card outranks the Operator its bead still names, even one not on any gate.
func TestStalledWorkLeavesACardOnAnEndedOperator(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Title: "Alpha", Steps: []SnapStep{
		{BeadID: "w.1", Title: "Sign in", Status: "open", State: "needs-user", Owner: "operator-x", OwnerState: "finished"},
	}}
	s := snapOf(ws, SnapAgent{State: "idle", Since: 1}, NeedsYou{Project: "p", BeadID: "w.1", Workstream: "Alpha", Title: "Sign in"})
	if got := StalledWork(s, 1000); len(got) != 0 {
		t.Fatalf("card on an ended Operator: %+v", got)
	}
	// The work under the card is not the user's move.
	ws.Steps[0].Children = []SnapStep{{BeadID: "w.1.1", Title: "Fix it", Status: "open", Owner: "operator-y", OwnerState: "died"}}
	s = snapOf(ws, SnapAgent{State: "idle", Since: 1}, NeedsYou{Project: "p", BeadID: "w.1", Workstream: "Alpha", Title: "Sign in"})
	if got := StalledWork(s, 1000); len(got) != 1 || got[0].Bead != "w.1.1" {
		t.Fatalf("stopped work under a card: %+v", got)
	}
}

// A gate whose card an ancestor holds is the user's move like any card, so its input is held.
func TestStalledWorkHoldsTheInputOfAGateAnAncestorsCardHolds(t *testing.T) {
	gate := SnapStep{BeadID: "w.1.2", Title: "Plan approved", Status: "open", Gate: true, Approvers: []string{"user"},
		Children: []SnapStep{{BeadID: "w.1.2.1", Title: "Gather it", Status: "open", Owner: "manager-w"}}}
	track := SnapStep{BeadID: "w.1", Title: "Track", Status: "open", State: "needs-user", Track: true, Children: []SnapStep{gate,
		{BeadID: "w.1.3", Title: "Soak", Status: "open", Waiting: "soak-period", WaitSince: 1}}}
	card := &store.Decision{Question: "Approve w.1.2?", Options: []store.DecisionOption{{Label: "Yes"}}}
	s := snapOf(SnapWorkstream{BeadID: "w", Title: "Alpha", Steps: []SnapStep{track}}, SnapAgent{State: "idle", Since: 1},
		NeedsYou{Project: "p", BeadID: "w.1", Workstream: "Alpha", Decision: card})
	s.NoPitch = []NoPitch{{Project: "p", BeadID: "w.1.2", Title: "Plan approved", Gate: true, AskedAt: 1}}
	if got := StalledWork(s, 1000); len(got) != 0 {
		t.Fatalf("input of a gate under its ancestor's card: %+v", got)
	}
}

// A blocker holds a step unless it cannot finish before that step: the walk follows blockers,
// children and a gate's stage, stops at what is no longer open and ends on a loop.
func TestHoldsFromFollowsWhatTheBlockerAwaits(t *testing.T) {
	steps := []SnapStep{
		{BeadID: "x", Status: "open"},
		{BeadID: "edge", Status: "open", BlockedBy: []string{"x"}},
		{BeadID: "far", Status: "open", BlockedBy: []string{"edge"}},
		{BeadID: "parent", Status: "open", Children: []SnapStep{{BeadID: "kid", Status: "open", BlockedBy: []string{"x"}}}},
		{BeadID: "closed", Status: "closed", BlockedBy: []string{"x"}},
		{BeadID: "behind-closed", Status: "open", BlockedBy: []string{"closed"}},
		{BeadID: "loop-a", Status: "open", BlockedBy: []string{"loop-b"}},
		{BeadID: "loop-b", Status: "open", BlockedBy: []string{"loop-a"}},
	}
	b := newBoard(snapOf(SnapWorkstream{BeadID: "w", Steps: steps}, SnapAgent{State: "idle", Since: 1}), 1000)
	b.openGates = map[string]SnapGateNode{"own": {Stage: "x"}, "approval": {Stage: "x", Verifier: store.VerifierUser, BlockedBy: []string{"x"}}, "other": {Stage: "loop-a"}, "inputs": {BlockedBy: []string{"far"}}}
	for id, want := range map[string]bool{
		"edge": false, "far": false, "parent": false, "own": false, "inputs": false, "x": false, "closed": false, "unknown": false,
		"behind-closed": true, "loop-a": true, "other": true, "approval": true,
	} {
		if got := b.holdsFrom(id, map[string]bool{"x": true}); got != want {
			t.Errorf("holdsFrom(%s) = %v, want %v", id, got, want)
		}
	}
}

// The report gives Commander its own line for a mismatch, the badge's text only without one.
func TestCheckinReportMismatchReadsTheAgentLine(t *testing.T) {
	report := CheckinReport(Snapshot{Wrong: []SnapWrong{
		{Key: "a", Text: "badge a", Agent: "commander a"},
		{Key: "b", Text: "badge b"},
	}}, 1000, AuditOptions{})
	if !strings.Contains(report, "\n- commander a\n- badge b\n") || strings.Contains(report, "badge a") {
		t.Fatal(report)
	}
}

// Only the root's own label reaches the snapshot, with the reason its latest comment records.
func TestHoldOnTheRootReachesTheSnapshot(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{EdgeParentsOnly: true}
	m.Seed(beads.Bead{ID: "w", Title: "Alpha", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "w.a", ParentID: "w", Title: "Build track", Labels: []string{"kind:objective", beads.TrackLabel}})
	m.Seed(beads.Bead{ID: "w.1", ParentID: "w", Title: "Sign off", Labels: []string{"kind:objective"}})
	m.SetParent(ctx, "/p", "w.a", "w")
	m.SetParent(ctx, "/p", "w.1", "w")
	snapOf := func() SnapWorkstream {
		t.Helper()
		rows, err := projectObjectiveRows(ctx, m, store.Project{ID: 1, Path: "/p"})
		if err != nil {
			t.Fatal(err)
		}
		nested := NestObjectives(rows)
		if len(nested) != 3 || nested[0].Row.BeadID != "w" {
			t.Fatalf("rows %+v", nested)
		}
		return snapWorkstream(1, Workstream{Project: store.Project{Name: "p"}, Root: nested[0].Row, Steps: nested[1:]}, nil)
	}
	m.SetLabel(ctx, "/p", "w.a", beads.HoldLabel, true)
	m.SetLabel(ctx, "/p", "w.1", beads.HoldLabel, true)
	if got := snapOf(); got.Hold || got.HoldReason != "" {
		t.Fatalf("a hold on a track or step reached the root: %+v", got)
	}
	m.SetLabel(ctx, "/p", "w", beads.HoldLabel, true)
	m.AddComment(ctx, "/p", "w", beads.HoldComment("commander", "first"))
	m.AddComment(ctx, "/p", "w", beads.HoldComment("commander", "TEAM-12 is paused"))
	if got := snapOf(); !got.Hold || got.HoldReason != "TEAM-12 is paused" {
		t.Fatalf("root hold %v reason %q", got.Hold, got.HoldReason)
	}
}

// No hold: Checkin says exactly what the three unsplit reads say.
func TestCheckinWithNoHoldIsTheUnsplitReads(t *testing.T) {
	s := Snapshot{
		Workstreams: []SnapWorkstream{{N: 1, Project: "p", BeadID: "w", Title: "Alpha", ManagerRole: "manager-w", Steps: []SnapStep{{BeadID: "w.1", Title: "Build", Status: "open"}}}},
		Agents:      map[string]SnapAgent{"manager-w": {State: store.AgentIdle, Since: 1}},
		Display: &Display{Workstreams: []BoardRow{{N: 1, Project: "p", BeadID: "w", Title: "Alpha", ManagerRole: "manager-w", Kind: "stalled", Text: "Stuck", Diagram: &Diagram{Findings: []DiagramFinding{
			{Check: "unheld", Bead: "w.1"}, {Check: "MERGE_REVIEW_REQUIRED", Bead: "w.2"}, {Check: "gate-title", Bead: "w.3", Advisory: true},
		}}}}},
	}
	got := Checkin(s, 1000, AuditOptions{})
	errors, blockers, advisories := FindingCounts(s, 1000)
	if got.Errors != errors || got.Blockers != blockers || got.Advisories != advisories || got.Held != nil {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(got.Findings, WorkflowFindings(s, 1000)) || !reflect.DeepEqual(got.Stalled, StalledWork(s, 1000)) || !reflect.DeepEqual(got.Suspects, AuditSnapshot(s, 1000, AuditOptions{})) {
		t.Fatalf("%+v", got)
	}
	if errors != 1 || len(got.Stalled) == 0 || len(got.Suspects) == 0 {
		t.Fatalf("the fixture must exercise each list: %+v", got)
	}
}
