package livedata

import (
	"encoding/json"
	"os"
	"testing"
)

func reportedGap(t *testing.T) (Snapshot, int64) {
	t.Helper()
	var fx struct {
		Now      int64    `json:"now"`
		Snapshot Snapshot `json:"snapshot"`
	}
	raw, err := os.ReadFile("testdata/board/reported-gap.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx.Snapshot, fx.Now
}

// From the moment an Operator ends with a report the row is the Manager's move
// to close, before cmdrd's settle has written a thing; a death with no report,
// a run-out timer and a step truly nothing holds read as they always did.
func TestBoardReportedOperatorGap(t *testing.T) {
	snap, now := reportedGap(t)
	d := Board(snap, now)
	want := map[string]string{
		"app-w1":  resultReviewText,
		"app-w2":  "Working on build the next wave",
		"app-w3":  "Agent stopped on trace the slow query: the Manager's move",
		"app-w4":  "Manager review overdue",
		"app-w5":  resultReviewText,
		"app-w6":  resultReviewText,
		"app-w7":  "Agent stopped on split the worker: the Manager's move",
		"app-w.8": resultReviewText,
		"app-w9":  "State missing",
	}
	for _, r := range d.Workstreams {
		if r.Text != want[r.BeadID] {
			t.Errorf("%s (%s) reads %q, want %q (why %q)", r.BeadID, r.Title, r.Text, want[r.BeadID], r.Why)
		}
	}
}

// The row beside a report still answers for a step nothing holds: a handoff
// leads the line as it does once settled, and the plan keeps flagging the step.
func TestBoardReportedHidesNoUnheldStep(t *testing.T) {
	snap, now := reportedGap(t)
	d := Board(snap, now)
	unheld := map[string]bool{}
	for _, r := range d.Workstreams {
		for _, f := range r.Diagram.Findings {
			if f.Check == "unheld" {
				unheld[f.Bead] = true
			}
		}
	}
	for _, bead := range []string{"app-w1.1", "app-w2.1", "app-w4.1", "app-w5.1", "app-w6.1"} {
		if unheld[bead] {
			t.Errorf("%s reported, yet its plan flags it unheld", bead)
		}
	}
	if !unheld["app-w5.2"] {
		t.Errorf("app-w5.2 is held by nothing, yet not flagged: %v", unheld)
	}
	// What health reads: a reported step's workstream has no blocking finding.
	for _, r := range d.Workstreams {
		if r.BeadID != "app-w1" && r.BeadID != "app-w2" && r.BeadID != "app-w6" {
			continue
		}
		for _, f := range r.Diagram.Findings {
			if !f.Advisory {
				t.Errorf("%s: health would flag %+v", r.BeadID, f)
			}
		}
	}
}

// reportedRows is one workstream through cmdrd's own order: the Operator's claim
// checked against what runs, then the report. The live Operators list is empty.
func reportedRows(reported ...SnapReported) []ObjectiveRow {
	rows := []ObjectiveRow{
		{ProjectID: 1, ProjectName: "app", BeadID: "app-w", Title: "Queue work", Status: "open", State: "in-progress", Owner: "manager-app-w"},
		{ProjectID: 1, ProjectName: "app", BeadID: "app-w.1", ParentBeadID: "app-w", Title: "Measure", Status: "in_progress", State: "in-progress", Owner: "operator-op-a", OwnerState: OwnerWorking},
	}
	live := LiveOperators{}
	return MarkReported(MarkLiveOwners(rows, live), reported, live)
}

// The invariant and graph check read the step as held for the whole window, from
// the rows and from the snapshot both; with no report the claim is still a death.
func TestReportedRowsKeepInvariantQuiet(t *testing.T) {
	rep := SnapReported{Operator: "operator-op-a", ProjectID: 1, Bead: "app-w.1", Since: 1793214000}
	idle := func(string) string { return "idle" }
	if v := OpenBeadInvariant(reportedRows(), idle); len(v) != 1 || v[0].BeadID != "app-w.1" {
		t.Fatalf("no report: violations %+v, want the step", v)
	}
	rows := reportedRows(rep)
	if v := OpenBeadInvariant(rows, idle); len(v) != 0 {
		t.Fatalf("reported: violations %+v", v)
	}
	r := rows[1]
	if r.Owner != "manager-app-w" || r.OwnerState != "" || r.OwnerWhy != "" || r.State != "review" || len(r.Waits) != 1 ||
		r.Waits[0].Ref != "Manager to close" || r.Waits[0].Until.Unix() != rep.Since+3600 {
		t.Fatalf("row does not read as its settle will leave it: %+v", r)
	}
	s := BuildSnapshot(Data{Objectives: rows}, nil)
	if v := SnapshotViolations(s); len(v) != 0 {
		t.Fatalf("snapshot form: %+v", v)
	}
}

// A run's end speaks only for the step it left.
func TestReportedHoldsOnlyItsOwnStep(t *testing.T) {
	rep := SnapReported{Operator: "operator-op-a", ProjectID: 1, Bead: "b", Since: 100}
	const mgr = "manager-w"
	cases := []struct {
		name                          string
		status, state, owner, waiting string
		live                          LiveOperators
		want                          bool
	}{
		{"claim still names it", "in_progress", "in-progress", "operator-op-a", "", LiveOperators{}, true},
		{"dotted spelling of it", "in_progress", "in-progress", "operator-op.a", "", LiveOperators{}, true},
		{"settle cleared the claim", "in_progress", "in-progress", "", "", LiveOperators{}, true},
		{"settle moved the claim", "in_progress", "review", mgr, "", LiveOperators{}, true},
		{"reopened, nobody on it", "open", "", "", "", LiveOperators{}, false},
		{"another Operator's", "in_progress", "in-progress", "operator-op-b", "", LiveOperators{}, false},
		{"same name respawned", "in_progress", "in-progress", "operator-op-a", "", LiveOperators{"operator-op-a": true}, false},
		{"wait recorded", "in_progress", "review", mgr, "Manager to close", LiveOperators{}, false},
		{"closed", "closed", "done", mgr, "", LiveOperators{}, false},
		{"parked", "deferred", "in-progress", "", "", LiveOperators{}, false},
		{"user card", "open", "needs-user", "operator-op-a", "", LiveOperators{}, false},
	}
	for _, c := range cases {
		if got := rep.holds(c.status, c.state, c.owner, mgr, c.waiting, c.live); got != c.want {
			t.Errorf("%s: holds = %v, want %v", c.name, got, c.want)
		}
	}
}

// moveBead writes the owner label dashed while the Manager's role keeps the dots
// of a workstream promoted from a step: the two spellings are one Manager.
func TestReportedHoldsADottedManager(t *testing.T) {
	rep := SnapReported{Operator: "operator-op-a", ProjectID: 1, Bead: "b", Since: 100}
	if !rep.holds("in_progress", "review", "manager-app-w-8", "manager-app-w.8", "", LiveOperators{}) {
		t.Fatal("the settle's dashed owner label reads as somebody else")
	}
	if rep.holds("in_progress", "review", "manager-app-w-9", "manager-app-w.8", "", LiveOperators{}) {
		t.Fatal("another Manager's step reads as held")
	}
}
