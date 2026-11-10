package worktreelock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo returns a project on main with one Operator worktree on branch op/x,
// frozen the way a closed run leaves it.
func repo(t *testing.T) (project, wt string) {
	t.Helper()
	project = t.TempDir()
	git(t, project, "init", "-q", "-b", "main")
	git(t, project, "config", "user.name", "t")
	git(t, project, "config", "user.email", "t@t")
	os.WriteFile(filepath.Join(project, "a.txt"), []byte("a"), 0o644)
	git(t, project, "add", ".")
	git(t, project, "commit", "-qm", "a")
	wt = filepath.Join(project, ".worktrees", "op-x")
	git(t, project, "worktree", "add", "-q", "-b", "op/x", wt)
	return project, wt
}

func TestPruneRemovesAFrozenWorktreeWhoseWorkIsOnMain(t *testing.T) {
	project, wt := repo(t)
	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b"), 0o644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "b")
	git(t, project, "merge", "-q", "--ff-only", "op/x")
	if err := Lock(wt); err != nil {
		t.Fatal(err)
	}
	ok, err := Prune(project, wt)
	if err != nil || !ok {
		t.Fatalf("Prune = %v, %v; want removed", ok, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}
	if exec.Command("git", "-C", project, "rev-parse", "--verify", "--quiet", "refs/heads/op/x").Run() == nil {
		t.Error("branch op/x kept")
	}
}

func TestPruneKeepsWorkThatIsNotOnMain(t *testing.T) {
	t.Run("unmerged commit", func(t *testing.T) {
		project, wt := repo(t)
		os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b"), 0o644)
		git(t, wt, "add", ".")
		git(t, wt, "commit", "-qm", "b")
		Lock(wt)
		t.Cleanup(func() { Unlock(wt) }) // TempDir cannot delete a frozen tree
		if ok, _ := Prune(project, wt); ok {
			t.Fatal("pruned a branch with a commit main lacks")
		}
		if _, err := os.Stat(filepath.Join(wt, "b.txt")); err != nil {
			t.Fatalf("work lost: %v", err)
		}
	})
	t.Run("uncommitted change", func(t *testing.T) {
		project, wt := repo(t)
		os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip"), 0o644)
		if ok, _ := Prune(project, wt); ok {
			t.Fatal("pruned a worktree with uncommitted work")
		}
		if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
			t.Fatalf("work lost: %v", err)
		}
	})
}

func TestPruneRemovesWorkKeptByAnArchiveRef(t *testing.T) {
	project, wt := repo(t)
	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b"), 0o644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "b")
	tip := strings.TrimSpace(git(t, wt, "rev-parse", "HEAD"))
	// main was rebuilt: the branch's commit is not on it, only in the archive.
	git(t, project, "update-ref", "refs/archive/op/x-"+tip[:12], tip)
	if err := Lock(wt); err != nil {
		t.Fatal(err)
	}
	ok, err := Prune(project, wt)
	if err != nil || !ok {
		t.Fatalf("Prune = %v, %v; want removed", ok, err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still on disk: %v", err)
	}
	if exec.Command("git", "-C", project, "rev-parse", "--verify", "--quiet", "refs/heads/op/x").Run() == nil {
		t.Error("branch op/x kept")
	}
	if exec.Command("git", "-C", project, "cat-file", "-e", tip).Run() != nil {
		t.Error("archived commit lost")
	}
}
