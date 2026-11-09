package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/store"
)

// pinOperatorCommits fakes which commits Operator runs wrote: sha -> task.
// hiddenAtBind models a binding recorded before the run's branch showed the
// commit: the project-wide read at bind time sees nothing.
func pinOperatorCommits(t *testing.T, wrote map[string]string, hiddenAtBind bool) {
	t.Helper()
	old := workflowOperatorCommits
	workflowOperatorCommits = func(_ context.Context, project, task string, shas []string) (map[string]string, error) {
		if project != "p" {
			t.Errorf("Operator commits read for project %q", project)
		}
		found := map[string]string{}
		for _, sha := range shas {
			owner, ok := wrote[sha]
			if !ok || (task == "" && hiddenAtBind) || (task != "" && task != owner) {
				continue
			}
			found[sha] = "on branch op/x of Operator run operator-op-x (task " + owner + ")"
		}
		return found, nil
	}
	t.Cleanup(func() { workflowOperatorCommits = old })
}

// A real repo: the user's commits, an Operator branched off main, a fix
// Operator branched off the user's commits, a commit made on a detached
// worktree, an archived branch, and a commit under an Operator-only identity.
func TestOperatorAuthorshipReadsTheRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	git := func(wd string, env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(wd, msg string, env ...string) string {
		git(wd, env, "commit", "--allow-empty", "-q", "-m", msg)
		return git(wd, nil, "rev-parse", "HEAD")
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, nil, "init", "-q", "-b", "main")
	git(repo, nil, "config", "user.email", "user@example.com")
	git(repo, nil, "config", "user.name", "User")
	commit(repo, "base")
	git(repo, nil, "checkout", "-q", "-b", "user-work")
	u1 := commit(repo, "user 1")
	u2 := commit(repo, "user 2")
	git(repo, nil, "checkout", "-q", "main")

	wts := filepath.Join(repo, ".worktrees")
	git(repo, nil, "worktree", "add", "-q", "-b", "op/a-t1", filepath.Join(wts, "op-a-t1"), "main")
	a1 := commit(filepath.Join(wts, "op-a-t1"), "operator a")
	git(repo, nil, "worktree", "remove", filepath.Join(wts, "op-a-t1")) // only its branch is left
	git(repo, nil, "worktree", "add", "-q", "-b", "op/fix-t2", filepath.Join(wts, "op-fix-t2"), u2)
	f1 := commit(filepath.Join(wts, "op-fix-t2"), "fix")
	det := filepath.Join(wts, "op-det-t3")
	git(repo, nil, "worktree", "add", "-q", "--detach", det, "main")
	bot := []string{"GIT_COMMITTER_EMAIL=bot@example.com", "GIT_AUTHOR_EMAIL=bot@example.com"}
	d1 := commit(det, "detached", bot...)
	git(det, nil, "checkout", "-q", "--detach", "main") // d1 is now on no branch
	git(repo, nil, "branch", "-q", "old-tip", "main")
	o1 := git(repo, nil, "commit-tree", "-p", "main", "-m", "archived", "main^{tree}")
	git(repo, nil, "update-ref", "refs/archive/op/old-t4-"+o1[:7], o1)
	// Made outside every worktree, authored as the Operator-only identity.
	i1 := git(repo, []string{"GIT_AUTHOR_EMAIL=bot@example.com"}, "commit-tree", "-p", "main", "-m", "identity", "main^{tree}")
	git(repo, nil, "update-ref", "refs/heads/side", i1)
	missing := strings.Repeat("e", 40)

	run := func(name, task string, wt bool) store.AgentRun {
		r := store.AgentRun{AgentName: name, TaskBeadID: task}
		if wt {
			r.WorktreePath = sql.NullString{String: filepath.Join(wts, strings.TrimPrefix(name, "operator-")), Valid: true}
		}
		return r
	}
	runs := []store.AgentRun{
		run("operator-op-a-t1", "t1", true),
		run("operator-op-fix-t2", "t2", true),
		run("operator-op-det-t3", "t3", true),
		run("operator-op-old-t4", "t4", false),
	}
	all := []string{u1, u2, a1, f1, d1, o1, i1, missing}
	got, err := operatorAuthorship(ctx, repo, repo, runs, all)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{a1: "on branch op/a-t1 of Operator run operator-op-a-t1", f1: "operator-op-fix-t2 (task t2)", d1: "worktree of Operator run operator-op-det-t3", o1: "op/old-t4 (archived)", i1: "bot@example.com"}
	for sha, why := range want {
		if !strings.Contains(got[sha], why) {
			t.Errorf("%s: got %q want %q", sha, got[sha], why)
		}
	}
	for _, sha := range []string{u1, u2, missing} {
		if why, ok := got[sha]; ok {
			t.Errorf("user commit %s attributed: %s", sha, why)
		}
	}
	// Reviewer side: only that task's runs count.
	if got, err := operatorAuthorship(ctx, repo, repo, runs[1:2], all); err != nil || len(got) != 1 || got[f1] == "" {
		t.Fatal(got, err)
	}
	if got, err := operatorAuthorship(ctx, repo, repo, nil, all); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	// No repo to read = error, never a pass.
	if _, err := operatorAuthorship(ctx, filepath.Join(dir, "nowhere"), repo, runs, all); err == nil {
		t.Fatal("unreadable repo passed")
	}
}
