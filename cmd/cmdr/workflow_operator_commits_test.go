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

// Branches made as spawn makes them (-B from main) and then moved onto the
// user's commits: a reviewer resetting to the PR head, a fast-forward merge, a
// rebase, a non-fast-forward merge. Only the commits the Operator made count.
func TestOperatorAuthorshipIgnoresMovesOntoTheUsersCommits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	git := func(wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(wd, msg string) string {
		git(wd, "commit", "--allow-empty", "-q", "-m", msg)
		return git(wd, "rev-parse", "HEAD")
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "user@example.com")
	git(repo, "config", "user.name", "User")
	commit(repo, "base")
	git(repo, "checkout", "-q", "-b", "user-work")
	u1 := commit(repo, "user 1")
	u2 := commit(repo, "user 2")
	git(repo, "checkout", "-q", "main")
	wts := filepath.Join(repo, ".worktrees")
	add := func(name string) string {
		wt := filepath.Join(wts, name)
		git(repo, "worktree", "add", "-q", "-B", "op/"+strings.TrimPrefix(name, "op-"), wt, "main")
		return wt
	}
	// The review protocol: reset to the reviewed head, write nothing.
	rev := add("op-rev-t1")
	git(rev, "reset", "-q", "--hard", u2)
	// Fast-forward onto the user's branch, then a fix.
	ff := add("op-ff-t2")
	git(ff, "merge", "-q", "--ff-only", "user-work")
	f1 := commit(ff, "fix after ff")
	// A commit of its own, rebased onto the user's branch.
	rb := add("op-rb-t3")
	commit(rb, "own")
	git(rb, "rebase", "-q", "user-work")
	r1 := git(rb, "rev-parse", "HEAD")
	// A non-fast-forward merge of the user's branch: only the merge commit.
	mg := add("op-mg-t4")
	commit(mg, "own before merge")
	git(mg, "merge", "-q", "--no-ff", "--no-edit", "user-work")
	m1 := git(mg, "rev-parse", "HEAD")

	var runs []store.AgentRun
	for _, n := range []string{"op-rev-t1", "op-ff-t2", "op-rb-t3", "op-mg-t4"} {
		runs = append(runs, store.AgentRun{AgentName: "operator-" + n, TaskBeadID: n[len(n)-2:],
			WorktreePath: sql.NullString{String: filepath.Join(wts, n), Valid: true}})
	}
	all := []string{u1, u2, f1, r1, m1}
	got, err := operatorAuthorship(ctx, repo, repo, runs, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{u1, u2} {
		if why, ok := got[sha]; ok {
			t.Errorf("user commit %s attributed: %s", sha, why)
		}
	}
	for sha, who := range map[string]string{f1: "op-ff-t2", r1: "op-rb-t3", m1: "op-mg-t4"} {
		if !strings.Contains(got[sha], who) {
			t.Errorf("%s: got %q, want written by %s", sha, got[sha], who)
		}
	}
	// The reviewer alone wrote nothing, so it is independent of the user's commits.
	if got, err := operatorAuthorship(ctx, repo, repo, runs[:1], all); err != nil || len(got) != 0 {
		t.Fatalf("reviewer that reset to the PR head counted as a writer: %v %v", got, err)
	}
	// Removing the worktree leaves only the branch reflog: same answer.
	git(repo, "worktree", "remove", "--force", rb)
	if got, err := operatorAuthorship(ctx, repo, repo, runs[2:3], all); err != nil || len(got) != 1 || got[r1] == "" {
		t.Fatalf("rebased commit after worktree removal: %v %v", got, err)
	}
}

func TestReflogWrote(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for msg, want := range map[string]int{
		"commit: fix":                  wroteNew,
		"commit (amend): fix":          wroteNew,
		"commit (merge): Merge branch": wroteNew,
		"merge user-work: Merge made by the 'ort' strategy.":  wroteNew,
		"merge user-work: Fast-forward":                       wroteNothing,
		"pull: Fast-forward":                                  wroteNothing,
		"pull: Merge made by the 'ort' strategy.":             wroteNew,
		"cherry-pick: fast-forward":                           wroteNothing,
		"cherry-pick: fix":                                    wroteNew,
		"revert: Revert \"x\"":                                wroteNew,
		"am: fix":                                             wroteNew,
		"reset: moving to " + sha:                             wroteNothing,
		"branch: Created from main":                           wroteNothing,
		"branch: Reset to main":                               wroteNothing,
		"checkout: moving from a to b":                        wroteNothing,
		"rebase (start): checkout user-work":                  wroteNothing,
		"rebase (pick): own":                                  wroteNew,
		"commit: fast-forward":                                wroteNew,
		"commit (amend): Fast-forward the reset: moving to x": wroteNew,
		"commit: merge Fast-forward":                          wroteNew,
		"revert: Revert \"fast-forward\"":                     wroteNew,
		"rebase (pick): fast-forward":                         wroteNew,
		"rebase -i (pick): fast-forward":                      wroteNew,
		"rebase (continue): fix":                              wroteNew,
		"cherry-pick: Fast-forward reset: moving to":          wroteNew,
		"merge x: Merge made by the 'ort' strategy.":          wroteNew,
		"filter-branch: rewrite":                              wroteNothing,
		"commitx: y":                                          wroteNothing,
		"pull --rebase (pick): own":                           wroteNew,
		"rebase (finish): returning to refs/heads/x":          wroteNothing,
		"rebase (finish): refs/heads/x onto " + sha:           wroteRange,
		"pull --rebase (finish): refs/heads/x onto " + sha:    wroteRange,
	} {
		if got, _ := reflogWrote(msg); got != want {
			t.Errorf("%q: got %d want %d", msg, got, want)
		}
	}
}

// Operator commits whose subjects imitate other reflog actions are still
// attributed: only the action prefix of a reflog entry counts.
func TestOperatorAuthorshipIgnoresSubjectsThatImitateReflogActions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	git := func(wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(wd, msg string) string {
		git(wd, "commit", "--allow-empty", "-q", "-m", msg)
		return git(wd, "rev-parse", "HEAD")
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "user@example.com")
	git(repo, "config", "user.name", "User")
	commit(repo, "base")
	wts := filepath.Join(repo, ".worktrees")
	wt := filepath.Join(wts, "op-s-t1")
	git(repo, "worktree", "add", "-q", "-B", "op/s-t1", wt, "main")
	var shas []string
	for _, subj := range []string{"fast-forward", "Fast-forward", "reset: moving to main", "merge main", "checkout: moving from a to b"} {
		shas = append(shas, commit(wt, subj))
	}
	git(wt, "commit", "--allow-empty", "-q", "--amend", "-m", "amend fast-forward")
	shas = append(shas, git(wt, "rev-parse", "HEAD"))
	user := commit(repo, "user fast-forward")
	runs := []store.AgentRun{{AgentName: "operator-op-s-t1", TaskBeadID: "t1",
		WorktreePath: sql.NullString{String: wt, Valid: true}}}
	got, err := operatorAuthorship(ctx, repo, repo, runs, append(shas, user))
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range shas[:len(shas)-2] {
		if got[sha] == "" {
			t.Errorf("operator commit %s not attributed", sha)
		}
	}
	if got[shas[len(shas)-1]] == "" {
		t.Error("amended operator commit not attributed")
	}
	if why, ok := got[user]; ok {
		t.Errorf("user commit attributed: %s", why)
	}
}
