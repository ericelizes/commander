package worktreelock

import (
	"os"
	"os/exec"
	"strings"
)

// Prune removes a frozen Operator worktree, and its branch, once nothing in it
// is only there: no uncommitted change (the borrowed app/node_modules aside)
// and HEAD already on the project's main. Otherwise it changes nothing and
// returns false. The caller decides the bead is done (closed), so no respawn
// will want the worktree back.
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
	if exec.Command("git", "-C", projectDir, "merge-base", "--is-ancestor", headOf(dir), main).Run() != nil {
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
		// -d, not -D: git refuses if the branch somehow is not on main.
		_ = exec.Command("git", "-C", projectDir, "branch", "-d", branch).Run()
	}
	return true, nil
}

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
