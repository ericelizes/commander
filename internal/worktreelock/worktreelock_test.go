package worktreelock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestLockThenUnlockWorktree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(repo, ".worktrees", "op-x")
	git(t, repo, "worktree", "add", "-q", "-B", "op/x", wt)
	sub := filepath.Join(wt, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "a.go")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Lock(wt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("b"), 0o644); err == nil {
		t.Error("a locked worktree must refuse writes to files")
	}
	if err := os.WriteFile(filepath.Join(sub, "new.go"), nil, 0o644); err == nil {
		t.Error("a locked worktree must refuse new files")
	}
	if out := git(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked") {
		t.Errorf("git does not show the worktree locked:\n%s", out)
	}
	if err := Lock(wt); err != nil { // idempotent
		t.Fatal(err)
	}

	if err := Unlock(wt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("c"), 0o644); err != nil {
		t.Errorf("unlocked worktree must be writable: %v", err)
	}
	if out := git(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, "locked") {
		t.Errorf("git still shows it locked:\n%s", out)
	}
	if err := Lock(filepath.Join(repo, "missing")); err != nil {
		t.Errorf("missing dir: %v", err)
	}
}
