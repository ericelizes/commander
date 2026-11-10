package livedata

import (
	"context"
	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
	"strings"
	"testing"
)

func TestOperationalDependencyStillHoldsTask(t *testing.T) {
	ctx := context.Background()
	m := &beads.MemClient{}
	m.Seed(beads.Bead{ID: "sample-flow", Status: "open", Labels: []string{"kind:objective"}})
	m.Seed(beads.Bead{ID: "sample-flow.1", ParentID: "sample-flow", Status: "open", Labels: []string{"kind:task"}})
	m.Seed(beads.Bead{ID: "sample-flow.2", ParentID: "sample-flow", Status: "open", Labels: []string{"kind:operational", "waiting:1:until:1999999999:external-input"}})
	if err := m.DependOn(ctx, "/sample", "sample-flow.1", "sample-flow.2"); err != nil {
		t.Fatal(err)
	}
	rows, err := projectObjectiveRows(ctx, m, store.Project{ID: 1, Name: "sample", Path: "/sample"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range OpenBeadInvariant(rows, nil) {
		if v.BeadID == "sample-flow.1" {
			t.Fatalf("open operational dependency lost: %+v rows=%+v", v, rows)
		}
	}
	found := false
	for _, r := range rows {
		found = found || r.BeadID == "sample-flow.2"
	}
	if !found {
		t.Fatal("operational bead filtered out")
	}
}

func TestClosedReportRetainedLabelsCannotBecomeOverdue(t *testing.T) {
	s := SnapStep{BeadID: "sample-flow.1", Status: "closed", State: "review", Waiting: beads.ManagerCloseWait, WaitSince: 1, WaitUntil: 2, WaitUntilAs: beads.ManagerCloseWait}
	ws := SnapWorkstream{BeadID: "sample-flow", Project: "sample", ManagerRole: "manager-sample-flow", Steps: []SnapStep{s}}
	snap := Snapshot{Workstreams: []SnapWorkstream{ws}, Agents: map[string]SnapAgent{ws.ManagerRole: {State: "idle"}}}
	r := Board(snap, 100).Workstreams[0]
	if r.Kind == "stalled" || strings.Contains(r.Text, "overdue") || len(StalledWork(snap, 100)) != 0 {
		t.Fatalf("closed report revived: %+v", r)
	}
}

func TestOverdueHandoffStaysRedDuringManagerReaction(t *testing.T) {
	for _, state := range []string{"idle", "working"} {
		ws := SnapWorkstream{BeadID: "sample-flow", Project: "sample", ManagerRole: "manager-sample-flow", Steps: []SnapStep{{BeadID: "sample-flow.1", Status: "in_progress", State: "review", Waiting: beads.ManagerCloseWait, WaitSince: 1, WaitUntil: 2, WaitUntilAs: beads.ManagerCloseWait}}}
		snap := Snapshot{Workstreams: []SnapWorkstream{ws}, Agents: map[string]SnapAgent{ws.ManagerRole: {State: state}}, Unread: map[string]int{ws.ManagerRole: 1}}
		r := Board(snap, 100).Workstreams[0]
		if r.Kind != "stalled" || kindColour(r.Kind) != "red" || !strings.Contains(r.Why, "Owner: "+ws.ManagerRole) || !strings.Contains(r.Why, "existing report") || !strings.Contains(r.Why, "sample-flow.1") || r.Flag != 0 {
			t.Fatalf("failure hidden or sent to user (%s): %+v", state, r)
		}
	}
}

func TestStoppedWorkNeverTurnsGreenWhileManagerReacts(t *testing.T) {
	for _, receipt := range []string{"", beads.ManagerRecoverWait} {
		ws := SnapWorkstream{BeadID: "sample-flow", Project: "sample", ManagerRole: "manager-sample-flow", Steps: []SnapStep{{BeadID: "sample-flow.1", Status: "open", State: "not-started", OwnerState: OwnerDied, Waiting: receipt, WaitUntil: 200, WaitUntilAs: receipt}}}
		snap := Snapshot{Workstreams: []SnapWorkstream{ws}, Agents: map[string]SnapAgent{ws.ManagerRole: {State: "working"}}}
		r := Board(snap, 100).Workstreams[0]
		if r.Kind != "stalled" || !strings.Contains(r.Why, "stopped run and current holder") {
			t.Fatalf("stopped run disguised as recovery progress: %+v", r)
		}
	}
}

func TestPlanningAndRecoveryReceiptsExposeFailures(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		now     int64
		failure bool
	}{
		{beads.ManagerPlanWait, 10, false}, {beads.ManagerPlanWait, 30, true},
		{beads.ManagerRecoverWait, 10, true}, {beads.ManagerRecoverWait, 30, true},
	} {
		s := SnapStep{BeadID: "sample-flow.1", Status: "open", Waiting: tc.ref, WaitSince: 1, WaitUntil: 20, WaitUntilAs: tc.ref}
		l, ok := resultHandoff([]SnapStep{s}, tc.now)
		if !ok || (l.kind == "stalled") != tc.failure {
			t.Fatalf("receipt %s at %d: %+v", tc.ref, tc.now, l)
		}
		s.Status = "closed"
		if _, ok := resultHandoff([]SnapStep{s}, tc.now); ok {
			t.Fatal("closed custody resurrected")
		}
	}
}
