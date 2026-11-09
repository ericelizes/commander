package tui

import (
	"context"
	"testing"

	"github.com/example-user/commander/internal/store"
)

func TestClearFinishedOperatorOwners(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	pid, err := st.InsertProject(ctx, store.Project{Name: "manifest", Type: "code", Path: t.TempDir(), ActivatedAt: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.AgentRun{
		{ProjectID: pid, TaskBeadID: "t1", AgentName: "operator-live", SpawnTime: 1, Status: "running"},
		{ProjectID: pid, TaskBeadID: "t2", AgentName: "operator-gone", SpawnTime: 1, Status: "closed"},
	} {
		if _, err := st.InsertAgentRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows := []ObjectiveRow{
		{BeadID: "a", ProjectID: pid, Owner: "operator-live"},
		{BeadID: "b", ProjectID: pid, Owner: "operator-gone"},
		{BeadID: "c", ProjectID: pid, Owner: "operator-external"}, // never spawned by commander
		{BeadID: "d", ProjectID: pid, Owner: "user"},
	}
	got := clearFinishedOperatorOwners(ctx, st, []store.Project{{ID: pid, Name: "manifest"}}, rows)
	want := []string{"operator-live", "", "operator-external", "user"}
	for i, r := range got {
		if r.Owner != want[i] {
			t.Errorf("row %s owner = %q, want %q", r.BeadID, r.Owner, want[i])
		}
	}
}
