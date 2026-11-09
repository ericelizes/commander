package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/beads"
	"github.com/example-user/commander/internal/livedata"
)

func stubBoard(t *testing.T, s *livedata.Snapshot) {
	t.Helper()
	orig := closeSnapshotFn
	closeSnapshotFn = func() (*livedata.Snapshot, error) { return s, nil }
	t.Cleanup(func() { closeSnapshotFn = orig })
}

func ruleBoard() *livedata.Snapshot {
	step := func(id, title, state, status string, mut ...func(*livedata.SnapStep)) livedata.SnapStep {
		s := livedata.SnapStep{BeadID: id, Title: title, State: state, Status: status}
		for _, m := range mut {
			m(&s)
		}
		return s
	}
	after := func(ids ...string) func(*livedata.SnapStep) { return func(s *livedata.SnapStep) { s.BlockedBy = ids } }
	return &livedata.Snapshot{Workstreams: []livedata.SnapWorkstream{{Project: "lab", BeadID: "lab-x", Steps: []livedata.SnapStep{
		step("lab-x.1", "Review gate", "blocked", "in_progress", func(s *livedata.SnapStep) { s.Gate = true; s.Review = "70"; s.Waiting = "review (reviewer-a)" }),
		step("lab-x.2", "Deploy", "not-started", "open", after("lab-x.1")),
		step("lab-x.3", "Stage", "not-started", "open", after("lab-x.1"), func(s *livedata.SnapStep) {
			s.Children = []livedata.SnapStep{step("lab-x.3.1", "Inner work", "not-started", "open")}
		}),
		step("lab-x.4", "Workflow approved", "needs-user", "open", func(s *livedata.SnapStep) { s.Gate = true }),
		step("lab-x.5", "Free step", "not-started", "open"),
	}}}}
}

func TestDoor_NothingBeforeItsPrerequisites(t *testing.T) {
	stubBoard(t, ruleBoard())
	bc := reachedBeads()
	for _, id := range []string{"lab-x.2", "lab-x.3.1"} { // own blocker, and a blocker above it
		if err := checkReached(context.Background(), bc, "/lab", "lab", id, "--state in-progress"); err == nil || !strings.Contains(err.Error(), "lab-x.1") || !strings.Contains(err.Error(), "Nothing was written") {
			t.Errorf("%s: err = %v", id, err)
		}
	}
	if err := checkReached(context.Background(), bc, "/lab", "lab", "lab-x.5", "--state in-progress"); err != nil {
		t.Errorf("a step with nothing open before it: %v", err)
	}
}

// reachedBeads is ruleBoard as the db has it: the board's edges, all still true.
func reachedBeads() *beads.MemClient {
	m := &beads.MemClient{Edges: []beads.Dep{
		{IssueID: "lab-x.2", DependsOnID: "lab-x.1", Type: "blocks"},
		{IssueID: "lab-x.3", DependsOnID: "lab-x.1", Type: "blocks"},
	}}
	for _, id := range []string{"lab-x", "lab-x.1", "lab-x.2", "lab-x.3", "lab-x.3.1", "lab-x.5"} {
		m.Seed(beads.Bead{ID: id, Title: id})
	}
	return m
}

// failingDetails is a beads backend whose show fails.
type failingDetails struct{ *beads.MemClient }

func (failingDetails) Details(context.Context, string, []string) (map[string]beads.Detail, error) {
	return nil, errors.New("br show failed")
}

// The board lags a write; the refusal only stands on what a fresh read confirms.
func TestCheckReachedConfirmsTheBlockerWithAFreshRead(t *testing.T) {
	ctx := context.Background()
	reached := func(bc beads.Client, id string) error {
		return checkReached(ctx, bc, "/lab", "lab", id, "spawning an Operator")
	}
	t.Run("edge removed after the board was built", func(t *testing.T) {
		stubBoard(t, ruleBoard())
		bc := reachedBeads()
		bc.Edges = bc.Edges[1:]
		if err := reached(bc, "lab-x.2"); err != nil {
			t.Fatalf("a removed edge still refused: %v", err)
		}
	})
	t.Run("edge above removed after the board was built", func(t *testing.T) {
		stubBoard(t, ruleBoard())
		bc := reachedBeads()
		bc.Edges = bc.Edges[:1]
		if err := reached(bc, "lab-x.3.1"); err != nil {
			t.Fatalf("a removed edge above still refused: %v", err)
		}
	})
	t.Run("blocker closed, parked or superseded after the board was built", func(t *testing.T) {
		for _, edit := range []func(*beads.Bead){
			func(b *beads.Bead) { b.Status = "closed" },
			func(b *beads.Bead) { b.Status = "deferred" },
			func(b *beads.Bead) { b.Labels = []string{"superseded"} },
		} {
			stubBoard(t, ruleBoard())
			bc := reachedBeads()
			for _, b := range bc.Beads {
				if b.ID == "lab-x.1" {
					edit(b)
				}
			}
			if err := reached(bc, "lab-x.2"); err != nil {
				t.Fatalf("a settled blocker still refused: %v", err)
			}
		}
	})
	t.Run("a second blocker the board also names still refuses", func(t *testing.T) {
		board := ruleBoard()
		board.Workstreams[0].Steps[1].BlockedBy = []string{"lab-x.1", "lab-x.5"}
		stubBoard(t, board)
		bc := reachedBeads()
		bc.Edges = []beads.Dep{{IssueID: "lab-x.2", DependsOnID: "lab-x.5", Type: "blocks"}}
		if err := reached(bc, "lab-x.2"); err == nil || !strings.Contains(err.Error(), "waits on lab-x.5") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed fresh read refuses as before", func(t *testing.T) {
		stubBoard(t, ruleBoard())
		if err := reached(failingDetails{reachedBeads()}, "lab-x.2"); err == nil || !strings.Contains(err.Error(), "waits on lab-x.1") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a fresh read that leaves the bead out refuses as before", func(t *testing.T) {
		stubBoard(t, ruleBoard())
		if err := reached(&beads.MemClient{}, "lab-x.2"); err == nil || !strings.Contains(err.Error(), "waits on lab-x.1") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDoor_OneWaitPerReview(t *testing.T) {
	stubBoard(t, ruleBoard())
	review := []beads.Wait{{Kind: beads.WaitReview, Ref: "reviewer-a"}}
	if err := checkOneWaitPerReview("lab", "lab-x.5", "70", review); err == nil || !strings.Contains(err.Error(), "lab-x.1") {
		t.Errorf("err = %v", err)
	}
	if err := checkOneWaitPerReview("lab", "lab-x.5", "71", review); err != nil {
		t.Errorf("another PR: %v", err)
	}
}

func TestDoor_GateNotDuplicate(t *testing.T) {
	stubBoard(t, ruleBoard())
	if err := checkGateNotDuplicate("lab", "lab-x", "workflow approved"); err == nil || !strings.Contains(err.Error(), "lab-x.4") {
		t.Errorf("err = %v", err)
	}
	if err := checkGateNotDuplicate("lab", "lab-x", "Release approved"); err != nil {
		t.Errorf("a new title: %v", err)
	}
}
