package spawn

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// A branch a finished Operator left checked out is found at its worktree, so a
// follow-up Operator takes that worktree over instead of failing git's
// one-checkout-per-branch rule.
func TestBranchWorktreeFindsWhereABranchIsCheckedOut(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(repo, ".worktrees", "op-first")
	run("worktree", "add", "-q", "-b", "op/first", wt)

	got, _ := filepath.EvalSymlinks(branchWorktree(context.Background(), repo, "op/first"))
	want, _ := filepath.EvalSymlinks(wt)
	if got != want {
		t.Fatalf("branchWorktree = %q, want %q", got, want)
	}
	if p := branchWorktree(context.Background(), repo, "op/other"); p != "" {
		t.Fatalf("an unused branch has no worktree, got %q", p)
	}
}
