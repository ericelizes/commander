package spawn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// takeoverSetup is a project whose branch feat/auth a finished Operator left
// checked out under another name, so a spawn on it is a --branch takeover.
func takeoverSetup(t *testing.T) (dir, prev string) {
	t.Helper()
	dir = makeGitRepo(t)
	prev = filepath.Join(dir, ".worktrees", "op-earlier")
	mustRun(t, dir, "git", "worktree", "add", "-q", "-b", "feat/auth", prev)
	return dir, prev
}

func withBeforeTakeoverClaim(t *testing.T, f func(prev string)) {
	t.Helper()
	old := beforeTakeoverClaim
	beforeTakeoverClaim = f
	t.Cleanup(func() { beforeTakeoverClaim = old })
}

func assertRefusedTakeover(t *testing.T, err error, fs *fakeStore, dir string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("spawn: %v, want a clean refusal naming the removal", err)
	}
	if len(fs.runs) != 0 {
		t.Fatalf("a refused takeover must record no run: %v", fs.runs)
	}
	if _, err := os.Stat(filepath.Join(dir, ".worktrees", "feat-auth")); !os.IsNotExist(err) {
		t.Fatalf("a refused takeover must add no worktree of its own: %v", err)
	}
	if p := branchWorktree(context.Background(), dir, "feat/auth"); p != "" {
		t.Fatalf("the branch must stay unchecked-out, got %q", p)
	}
}

// The sweep removes the picked worktree after the spawn resolved it and before
// the spawn's claim: under the claim the spawn sees it gone and refuses.
func TestTakeoverRefusesWhenTheSweepRemovedTheWorktreeBeforeTheClaim(t *testing.T) {
	dir, prev := takeoverSetup(t)
	sp, fs, _, _, _, tx := newHostSpawner(t, dir)
	withBeforeTakeoverClaim(t, func(p string) {
		if resolvedPath(p) != resolvedPath(prev) {
			t.Errorf("resolved %q, want %q", p, prev)
		}
		release, held, err := TryClaimWorktree(p)
		if err != nil || !held {
			t.Errorf("sweep claim: held %v err %v", held, err)
			return
		}
		mustRun(t, dir, "git", "worktree", "remove", "--force", p)
		release()
	})
	_, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	assertRefusedTakeover(t, err, fs, dir)
	if len(tx.Calls) != 0 {
		t.Fatalf("nothing may start: %v", tx.Calls)
	}
}

// The other order: a real spawn holds the claim before starting its pane,
// so a sweep cannot take it. The successful run records the original worktree.
func TestTakeoverHoldingTheClaimKeepsTheSweepOff(t *testing.T) {
	dir, prev := takeoverSetup(t)
	sp, fs, _, _, _, tx := newHostSpawner(t, dir)
	checked := false
	tx.PipePaneFunc = func(_ context.Context, _, _ string) error {
		checked = true
		release, held, err := TryClaimWorktree(prev)
		if held {
			release()
		}
		if err != nil || held {
			t.Fatalf("sweep acquired the claim during spawn: held %v err %v", held, err)
		}
		return nil
	}
	res, err := sp.Spawn(context.Background(), goodRequest(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("spawn never reached the pane under its claim")
	}
	if resolvedPath(res.WorktreePath) != resolvedPath(prev) || len(fs.runs) != 1 {
		t.Fatalf("takeover result %+v, runs %+v", res, fs.runs)
	}
	for _, run := range fs.runs {
		if !run.WorktreePath.Valid || resolvedPath(run.WorktreePath.String) != resolvedPath(prev) {
			t.Fatalf("run does not own takeover worktree: %+v", run)
		}
	}
	release, held, err := TryClaimWorktree(prev)
	if err != nil || !held {
		t.Fatalf("spawn did not release claim: held %v err %v", held, err)
	}
	release()
}

// The scenario lives with the eval catalogue; its interleavings execute here
// so they use the real host spawn and claim without launching an agent.
func TestTakeoverSweepScenario(t *testing.T) {
	var sc struct {
		Cases []struct{ Name, Order string }
	}
	raw, err := os.ReadFile("../../tests/evals/scenarios/takeover-sweep.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	if len(sc.Cases) != 2 {
		t.Fatalf("want both interleavings: %+v", sc)
	}
	for _, c := range sc.Cases {
		switch c.Order {
		case "sweep-first":
			t.Run(c.Name, TestTakeoverRefusesWhenTheSweepRemovedTheWorktreeBeforeTheClaim)
		case "spawn-first":
			t.Run(c.Name, TestTakeoverHoldingTheClaimKeepsTheSweepOff)
		default:
			t.Fatalf("unknown interleaving %q", c.Order)
		}
	}
}

func TestRecheckBranchWorktree(t *testing.T) {
	ctx := context.Background()
	dir, prev := takeoverSetup(t)
	if err := recheckBranchWorktree(ctx, dir, "feat/auth", prev); err != nil {
		t.Fatalf("still there: %v", err)
	}
	if err := recheckBranchWorktree(ctx, dir, "op/none", ""); err != nil {
		t.Fatalf("no checkout before or after: %v", err)
	}
	if err := recheckBranchWorktree(ctx, dir, "feat/auth", ""); err == nil {
		t.Fatal("a checkout that appeared meanwhile must refuse")
	}
	if err := os.RemoveAll(prev); err != nil {
		t.Fatal(err)
	}
	if err := recheckBranchWorktree(ctx, dir, "feat/auth", prev); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("a dir git still lists but that is gone must refuse: %v", err)
	}
	mustRun(t, dir, "git", "worktree", "prune")
	if err := recheckBranchWorktree(ctx, dir, "feat/auth", prev); err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("a removed worktree must refuse: %v", err)
	}
}
