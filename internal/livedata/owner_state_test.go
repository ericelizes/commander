package livedata

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

// A claim stays on its step after the process exits: marked finished when the
// Operator handed the step on, died when it left the step open.
func TestMarkOperatorOwners(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.AgentRun{
		{ProjectID: pid, TaskBeadID: "t1", AgentName: "operator-live", SpawnTime: 1, Status: "running"},
		{ProjectID: pid, TaskBeadID: "t2", AgentName: "operator-done", SpawnTime: 1, Status: "closed"},
		{ProjectID: pid, TaskBeadID: "t3", AgentName: "operator-dead", SpawnTime: 1, Status: "closed"},
	} {
		if _, err := st.InsertAgentRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows := []ObjectiveRow{
		{BeadID: "a", ProjectID: pid, Owner: "operator-live", State: beads.StateInProgress},
		{BeadID: "b", ProjectID: pid, Owner: "operator-done", State: beads.StateReview},
		{BeadID: "c", ProjectID: pid, Owner: "operator-dead", State: beads.StateInProgress},
		{BeadID: "d", ProjectID: pid, Owner: "user", State: beads.StateInProgress},
	}
	got := MarkOperatorOwners(ctx, st, []store.Project{{ID: pid, Name: "p"}}, rows)
	want := []string{OwnerWorking, OwnerFinished, OwnerDied, ""}
	for i, r := range got {
		if r.Owner == "" {
			t.Errorf("row %s lost its owner", r.BeadID)
		}
		if r.OwnerState != want[i] {
			t.Errorf("row %s owner state = %q, want %q", r.BeadID, r.OwnerState, want[i])
		}
	}
}

// A bead read that predates the owner label (the spawn writes the run row
// first) still gives the step to the Operator running on it, so a fresh
// Operator never reads as ownerless and open-bead broken.
func TestMarkOperatorOwners_RunningRunClaimsOwnerlessTask(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "p", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertAgentRun(ctx, store.AgentRun{ProjectID: pid, TaskBeadID: "p-1.2", AgentName: "operator-op-x-p-1.2", SpawnTime: 1, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	rows := []ObjectiveRow{{BeadID: "p-1.2", ProjectID: pid, Status: "in_progress", State: beads.StateInProgress}}
	got := MarkOperatorOwners(ctx, st, []store.Project{{ID: pid, Name: "p"}}, rows)
	if got[0].Owner != "operator-op-x-p-1-2" || got[0].OwnerState != OwnerWorking {
		t.Fatalf("owner %q state %q, want the running Operator working", got[0].Owner, got[0].OwnerState)
	}
	if v := OpenBeadInvariant(MarkLiveOwners(got, LiveOperators{"operator-op-x-p-1-2": true}), nil); len(v) != 0 {
		t.Fatalf("fresh Operator reads broken: %+v", v)
	}
}
