package worktreelock

import (
	"os"
	"os/exec"
	"strings"
)

// Prune removes a frozen Operator worktree, and its branch, once nothing in it
// is only there: no uncommitted change (the borrowed app/node_modules aside)
// and HEAD already on the project's main or kept by an archive ref
// (refs/archive/..., what worktrees gc --archive writes; main can be rebuilt,
// so "on main" alone misses work that is safe). Otherwise it changes nothing
// and returns false. The caller decides the bead is done (closed), so no
// respawn will want the worktree back.
func Prune(projectDir, dir string) (bool, error) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, nil
	}
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(status)), "\n") {
		if line != "" && line != "?? app/node_modules" && !strings.Contains(line, ".beads/") {
			return false, nil
		}
	}
	main := mainBranch(projectDir)
	if main == "" {
		return false, nil
	}
	head := headOf(dir)
	if head == "" {
		return false, nil
	}
	onMain := exec.Command("git", "-C", projectDir, "merge-base", "--is-ancestor", head, main).Run() == nil
	if !onMain && !archived(projectDir, head) {
		return false, nil
	}
	branch := headBranch(dir)
	// Writable first: --force removes the files, which Lock made read-only.
	if err := Unlock(dir); err != nil {
		return false, err
	}
	if out, err := exec.Command("git", "-C", projectDir, "worktree", "remove", "--force", dir).CombinedOutput(); err != nil {
		return false, &gitError{"worktree remove", string(out), err}
	}
	if branch != "" {
		if onMain {
			// -d, not -D: git refuses if the branch somehow is not on main.
			_ = exec.Command("git", "-C", projectDir, "branch", "-d", branch).Run()
		} else {
			// Archived: delete only while the branch still points at the tip
			// the archive ref keeps.
			_ = exec.Command("git", "-C", projectDir, "update-ref", "-d", "refs/heads/"+branch, head).Run()
		}
	}
	return true, nil
}

// archived reports whether sha is reachable from any refs/archive/ ref.
func archived(projectDir, sha string) bool {
	out, err := exec.Command("git", "-C", projectDir, "for-each-ref", "--contains", sha, "--count=1", "--format=%(refname)", "refs/archive/").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// IsProjectWorktree reports whether path is inside <project>/.worktrees or
// <project>/.worktrees.noindex (real paths compared).
func IsProjectWorktree(projectDir, path string) bool { return underProjectWorktrees(projectDir, path) }

func mainBranch(projectDir string) string {
	for _, b := range []string{"main", "master"} {
		if exec.Command("git", "-C", projectDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+b).Run() == nil {
			return b
		}
	}
	return ""
}

func headOf(dir string) string {
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

func headBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type gitError struct {
	op, out string
	err     error
}

func (e *gitError) Error() string {
	return "git " + e.op + ": " + e.err.Error() + ": " + strings.TrimSpace(e.out)
}
