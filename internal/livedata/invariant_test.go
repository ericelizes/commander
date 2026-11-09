package livedata

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
)

func ids(vs []Violation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.BeadID)
	}
	return out
}

func TestOpenBeadInvariantBroken(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Title: "ws", Status: "open", ProjectName: "p"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Title: "orphan", Status: "open", ProjectName: "p"},
	}
	got := OpenBeadInvariant(rows, nil)
	if len(got) != 1 || got[0].BeadID != "p-1.1" {
		t.Fatalf("violations = %v, want [p-1.1] (the parent is covered by its open child)", ids(got))
	}
	if got[0].Workstream != "p-1" || got[0].ManagerRole != "manager-p-1" {
		t.Errorf("workstream/manager = %q/%q", got[0].Workstream, got[0].ManagerRole)
	}
}

func TestOpenBeadInvariantHealthy(t *testing.T) {
	wait := []beads.Wait{{Kind: beads.WaitCI, PR: 3, Since: time.Now()}}
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Owner: "operator-a", OwnerState: OwnerWorking},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", Waits: wait},
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", Gate: true},
		{BeadID: "p-1.4", ParentBeadID: "p-1", Status: "open", State: beads.StateNeedsUser},
		{BeadID: "p-1.5", ParentBeadID: "p-1", Status: "open", Owner: "manager-p-1"},
		{BeadID: "p-1.6", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.7", ParentBeadID: "p-1", Status: "closed"},
	}
	if got := OpenBeadInvariant(rows, func(string) string { return "working" }); len(got) != 0 {
		t.Fatalf("healthy fixture flagged %v", ids(got))
	}
}

func TestOpenBeadInvariantDeadOwnerAndBlockers(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Owner: "operator-a", OwnerState: OwnerDied},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", Owner: "manager-p-1"},
	}
	got := ids(OpenBeadInvariant(rows, func(string) string { return "" }))
	want := map[string]bool{"p-1.1": true, "p-1.3": true} // p-1.2 waits behind an open blocker
	if len(got) != 2 {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected %s", id)
		}
	}
}

func TestSnapshotViolationsAndBrokenFlag(t *testing.T) {
	s := Snapshot{
		Agents: map[string]SnapAgent{"manager-p-1": {State: "working"}},
		Workstreams: []SnapWorkstream{{
			BeadID: "p-1", Project: "p", Title: "ws", Status: "open",
			Steps: []SnapStep{
				{BeadID: "p-1.1", Status: "open", State: "in-progress", Owner: "manager-p-1"},
				{BeadID: "p-1.2", Status: "open", State: "not-started"},
				{BeadID: "p-1.3", Status: "open", State: "in-progress", Waiting: "CI #7"},
			},
		}},
	}
	got := SnapshotViolations(s)
	if len(got) != 1 || got[0].BeadID != "p-1.2" || got[0].ManagerRole != "manager-p-1" {
		t.Fatalf("violations = %+v", got)
	}
	markBroken(s.Workstreams, got)
	st := s.Workstreams[0].Steps
	if st[0].Broken || !st[1].Broken || st[2].Broken {
		t.Errorf("broken flags = %v %v %v", st[0].Broken, st[1].Broken, st[2].Broken)
	}
}

// An idle Manager's name on a step is not someone working on it: the step needs
// a recorded wait. (A step "waiting for the 12:00 reconcile" read
// Stalled all day, owned by an idle Manager, its wait only in the title.)
func TestIdleManagerOwnerDoesNotCoverAStep(t *testing.T) {
	step := ObjectiveRow{BeadID: "w.3", ParentBeadID: "w", Status: "in_progress", Owner: "manager-w", Title: "Waiting for scheduled reconcile"}
	root := ObjectiveRow{BeadID: "w", Status: "in_progress"}
	state := "idle"
	live := func(string) string { return state }
	if v := OpenBeadInvariant([]ObjectiveRow{root, step}, live); len(v) == 0 || v[0].BeadID != "w.3" {
		t.Fatalf("idle Manager owner: want w.3 flagged, got %+v", v)
	}
	state = "working"
	for _, v := range OpenBeadInvariant([]ObjectiveRow{root, step}, live) {
		if v.BeadID == "w.3" {
			t.Fatal("a Manager mid-turn covers its step")
		}
	}
	state = "idle"
	step.Waits = []beads.Wait{{Kind: beads.WaitUntil}}
	for _, v := range OpenBeadInvariant([]ObjectiveRow{root, step}, live) {
		if v.BeadID == "w.3" {
			t.Fatal("a recorded wait covers the step")
		}
	}
}

func TestOpenBeadInvariantDeferred(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Title: "ws", Status: "open", ProjectName: "p"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Title: "parked", Status: "deferred", ProjectName: "p"},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Title: "waits on parked", Status: "open", ProjectName: "p", BlockedBy: []string{"p-1.1"}},
	}
	got := OpenBeadInvariant(rows, nil)
	if len(got) != 1 || got[0].BeadID != "p-1.2" {
		t.Fatalf("violations = %v, want the dependant of a parked blocker (it is ready, so it needs an owner)", ids(got))
	}
	rows[2].BlockedBy = nil
	if got := OpenBeadInvariant(rows, nil); len(got) != 1 || got[0].BeadID != "p-1.2" {
		t.Fatalf("violations = %v, want only the unblocked open child", ids(got))
	}
	rows[2].BlockedBy = []string{"p-1.1"}
	rows[0].Status = "deferred"
	rows[2].Status = "deferred"
	if got := OpenBeadInvariant(rows, nil); len(got) != 0 {
		t.Fatalf("violations = %v, want none when everything is parked", ids(got))
	}
}

func TestSnapshotViolationsDeferred(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Status: "open", Steps: []SnapStep{
		{BeadID: "w.1", Status: "deferred", State: "not-started"},
	}}
	for _, v := range SnapshotViolations(snapOf(ws, idle)) {
		if v.BeadID == "w.1" {
			t.Fatalf("deferred bead flagged: %v", v)
		}
	}
}

func TestRowsDeferredNeverNeedsYou(t *testing.T) {
	ws := SnapWorkstream{BeadID: "w", Status: "open", Steps: []SnapStep{
		{BeadID: "w.1", Status: "deferred", State: "needs-user", Waiting: "you"},
		{BeadID: "w.2", Status: "deferred", State: "needs-user", Waiting: "you"},
	}}
	r := Rows(snapOf(ws, idle), nowSec)[0]
	if r.Line.Kind == "needs" || r.Colour == "red" {
		t.Fatalf("row %+v, want no needs/red for parked beads", r.Line)
	}
}

func TestInvariantBlockedByOpenBlocker(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open"},                               // itself ownerless
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}}, // covered
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.2"}}, // chain, covered
		{BeadID: "p-2", Status: "open", Gate: true},                                          // other track
		{BeadID: "p-2.1", ParentBeadID: "p-2", Status: "open", BlockedBy: []string{"p-1.1"}}, // cross-parent, covered
	}
	got := ids(OpenBeadInvariant(rows, nil))
	if len(got) != 1 || got[0] != "p-1.1" {
		t.Fatalf("violations = %v, want only the blocker p-1.1", got)
	}
}

func TestInvariantBlockedReadyAndInProgressStillFlagged(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Owner: "operator-a", OwnerState: OwnerWorking},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open"}, // ready, unblocked
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "in_progress", BlockedBy: []string{"p-1.1"}},
	}
	got := ids(OpenBeadInvariant(rows, nil))
	want := map[string]bool{"p-1.2": true, "p-1.3": true}
	if len(got) != len(want) {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected %s", id)
		}
	}
}

func TestInvariantBlockedClosedBlockerMakesItReady(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "closed"},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}},
	}
	if got := ids(OpenBeadInvariant(rows, nil)); len(got) != 1 || got[0] != "p-1.2" {
		t.Fatalf("violations = %v, want [p-1.2]", got)
	}
}

func TestInvariantBlockedAuditAgrees(t *testing.T) {
	ws := SnapWorkstream{BeadID: "p-1", Steps: []SnapStep{
		{BeadID: "p-1.1", Status: "open"},
		{BeadID: "p-1.2", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.3", Status: "in_progress", BlockedBy: []string{"p-1.1"}},
	}}
	got := openWithoutOwner(ws)
	if len(got) != 2 || got[0].BeadID != "p-1.1" || got[1].BeadID != "p-1.3" {
		t.Fatalf("openWithoutOwner = %v, want p-1.1 and p-1.3", got)
	}
}

func TestInvariantDeferredOrSupersededBlockerDoesNotBlock(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: beads.StatusDeferred},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", Superseded: true, Owner: "manager-p-1"},
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.4", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.2"}},
	}
	got := map[string]bool{}
	for _, id := range ids(OpenBeadInvariant(rows, func(string) string { return "" })) {
		got[id] = true
	}
	if !got["p-1.3"] || !got["p-1.4"] {
		t.Fatalf("violations = %v, want p-1.3 and p-1.4 (ready behind a parked/superseded blocker)", got)
	}
}

func TestInvariantDeferredBlockerAuditAndSnapshotAgree(t *testing.T) {
	steps := []SnapStep{
		{BeadID: "p-1.1", Status: beads.StatusDeferred},
		{BeadID: "p-1.2", Status: "open", Superseded: true, Waiting: "CI #7"},
		{BeadID: "p-1.3", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.4", Status: "open", BlockedBy: []string{"p-1.2"}},
	}
	ws := SnapWorkstream{BeadID: "p-1", Project: "p", Status: "open", Steps: steps}
	audit := openWithoutOwner(ws)
	snap := SnapshotViolations(Snapshot{Workstreams: []SnapWorkstream{ws}})
	if len(audit) != 2 || audit[0].BeadID != "p-1.3" || audit[1].BeadID != "p-1.4" {
		t.Fatalf("openWithoutOwner = %v, want p-1.3 and p-1.4", audit)
	}
	got := ids(snap)
	if len(got) != 2 || got[0] != "p-1.3" || got[1] != "p-1.4" {
		t.Fatalf("SnapshotViolations = %v, want p-1.3 and p-1.4", got)
	}
}

func TestInvariantGateNodeAndUnknownBlockers(t *testing.T) {
	ws := SnapWorkstream{BeadID: "p-1", Project: "p", Status: "open",
		GateNodes: []SnapGateNode{{BeadID: "g-open", Status: "open"}, {BeadID: "g-done", Status: "passed"}},
		Steps: []SnapStep{
			{BeadID: "p-1.1", Status: "open", BlockedBy: []string{"g-open"}},          // behind an open gate: covered
			{BeadID: "p-1.2", Status: "open", BlockedBy: []string{"g-done"}},          // gate passed: ready, flagged
			{BeadID: "p-1.3", Status: "open", BlockedBy: []string{"other-project-1"}}, // unknown: fail safe, flagged
		}}
	want := []string{"p-1.2", "p-1.3"}
	audit := openWithoutOwner(ws)
	got := ids(SnapshotViolations(Snapshot{Workstreams: []SnapWorkstream{ws}}))
	if len(audit) != 2 || audit[0].BeadID != want[0] || audit[1].BeadID != want[1] {
		t.Fatalf("openWithoutOwner = %v, want %v", audit, want)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SnapshotViolations = %v, want %v", got, want)
	}
}

// A slug-labelled timer stands in for a wait nothing watches: it covers
// nothing, not even a gate, so the row reads broken until it is rewritten.
func TestInvariantFlagsAnUntypedWait(t *testing.T) {
	slug := []beads.Wait{{Kind: beads.WaitUntil, Ref: "user-spec-review-first", Until: time.Now().Add(time.Hour)}}
	words := []beads.Wait{{Kind: beads.WaitUntil, Ref: "the 6 AM quiet window opens", Until: time.Now().Add(time.Hour)}}
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open", Owner: "manager-p-1"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Waits: slug},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", Gate: true, Waits: slug},
		{BeadID: "p-1.3", ParentBeadID: "p-1", Status: "open", Waits: words},
		{BeadID: "p-1.4", ParentBeadID: "p-1", Status: "open", Waits: []beads.Wait{{Kind: beads.WaitUntil, Until: time.Now()}}},
	}
	got := OpenBeadInvariant(rows, nil)
	if len(got) != 2 || got[0].BeadID != "p-1.1" || got[1].BeadID != "p-1.2" {
		t.Fatalf("violations = %v, want [p-1.1 p-1.2]", ids(got))
	}
	if got[0].Why == "" {
		t.Error("an untyped wait's violation should say why")
	}
}

// Every reader of "held" goes through holding.Held (invariant I5); a new
// predicate written beside it fails here.
func TestEveryHeldReaderCallsTheOnePredicate(t *testing.T) {
	for _, f := range []string{"invariant.go", "audit.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "holding.Held(") {
			t.Errorf("%s does not call holding.Held", f)
		}
	}
}

func TestInvariantBlockedThroughParentIsHeld(t *testing.T) {
	rows := []ObjectiveRow{
		{BeadID: "p-1", Status: "open"},
		{BeadID: "p-1.1", ParentBeadID: "p-1", Status: "open", Waits: []beads.Wait{{}}},
		{BeadID: "p-1.2", ParentBeadID: "p-1", Status: "open", BlockedBy: []string{"p-1.1"}},
		{BeadID: "p-1.2.1", ParentBeadID: "p-1.2", Status: "open"},
	}
	if got := ids(OpenBeadInvariant(rows, nil)); len(got) != 0 {
		t.Fatalf("violations = %v, want none", got)
	}
}
