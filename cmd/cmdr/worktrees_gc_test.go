package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/store"
)

func TestWorktreeGCLookupMatchesRunsAndHolds(t *testing.T) {
	runs := []store.AgentRun{
		{TaskBeadID: "p-1.2", Status: "closed", SpawnTime: 100},
		{TaskBeadID: "p-1.3", Status: "running", SpawnTime: 200},
	}
	byID := map[string]beads.Bead{
		"p-1":   {ID: "p-1", Labels: []string{beads.HoldLabel}},
		"p-1.2": {ID: "p-1.2", Status: "closed"},
	}
	look := gcLookup(context.Background(), nil, "", runs, byID)
	f := look("/x/.worktrees/op-foo-p-1.2", "op/foo-p-1.2")
	if f.Running || !f.TaskClosed || !f.Held || !f.LastActivity.Equal(time.Unix(100, 0)) {
		t.Errorf("closed run facts = %+v", f)
	}
	if f := look("/x/.worktrees/op-bar-p-1.3", "op/bar-p-1.3"); !f.Running {
		t.Errorf("running run facts = %+v", f)
	}
	if f := look("/x/.worktrees/op-none", "op/none"); f.Running || f.Held || f.TaskClosed {
		t.Errorf("unmatched facts = %+v", f)
	}
}

type failingList struct{ beads.Client }

func (failingList) List(context.Context, string, beads.ListOptions) ([]beads.Bead, error) {
	return nil, errors.New("br busy")
}

func TestWorktreeGCFailsClosedWhenBeadListFails(t *testing.T) {
	if _, err := gcBeadIndex(context.Background(), failingList{}, "/x"); err == nil {
		t.Fatal("a failed br list became an empty index (held worktrees would look free)")
	}
}

func TestWorktreeGCHeldFollowsRealParentEdgeNotDottedId(t *testing.T) {
	ctx := context.Background()
	mem := &beads.MemClient{EdgeParentsOnly: true}
	mem.Seed(beads.Bead{ID: "p-9", Labels: []string{beads.HoldLabel}})
	mem.Seed(beads.Bead{ID: "p-1", Status: "closed"})
	mem.Seed(beads.Bead{ID: "p-2.1", Status: "closed"})
	mem.SetParent(ctx, "/x", "p-2.1", "p-9") // moved under the held p-9
	mem.Seed(beads.Bead{ID: "p-1.1", Status: "closed"})
	mem.SetParent(ctx, "/x", "p-1.1", "p-1")
	byID, err := gcBeadIndex(ctx, mem, "/x")
	if err != nil {
		t.Fatal(err)
	}
	runs := []store.AgentRun{{TaskBeadID: "p-2.1", Status: "closed"}, {TaskBeadID: "p-1.1", Status: "closed"}}
	look := gcLookup(ctx, mem, "/x", runs, byID)
	if f := look("/x/.worktrees/op-a-p-2.1", "op/a-p-2.1"); !f.Held || !f.Matched {
		t.Errorf("moved step under held workstream: %+v", f)
	}
	if f := look("/x/.worktrees/op-b-p-1.1", "op/b-p-1.1"); f.Held {
		t.Errorf("unheld step: %+v", f)
	}
}

func TestWorktreeGCLookupLongestTaskIdWinsAndNeedsBoundary(t *testing.T) {
	runs := []store.AgentRun{{TaskBeadID: "p-1.2", Status: "closed"}, {TaskBeadID: "p-1.22", Status: "running"}}
	look := gcLookup(context.Background(), nil, "", runs, map[string]beads.Bead{})
	if f := look("/x/.worktrees/op-a-p-1.22", "op/a-p-1.22"); f.TaskID != "p-1.22" || !f.Running {
		t.Errorf("facts = %+v", f)
	}
	if f := look("/x/.worktrees/op-xp-1.2", "op/xp-1.2"); f.Matched {
		t.Errorf("id without a boundary matched: %+v", f)
	}
}
