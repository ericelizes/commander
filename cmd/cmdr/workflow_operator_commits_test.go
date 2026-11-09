package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example-user/commander/internal/spawn"
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

// opRepo is a real repo with the user's commits on user-work and git as an
// Operator runs it: the launcher's committer identity in the environment.
type opRepo struct {
	t    *testing.T
	repo string
	wts  string
}

func newOpRepo(t *testing.T) (*opRepo, string, string) {
	r := &opRepo{t: t, repo: filepath.Join(t.TempDir(), "repo")}
	r.wts = filepath.Join(r.repo, ".worktrees")
	if err := os.MkdirAll(r.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "", "init", "-q", "-b", "main")
	r.git(r.repo, "", "config", "user.email", "user@example.com")
	r.git(r.repo, "", "config", "user.name", "User")
	r.commit(r.repo, "", "base")
	r.git(r.repo, "", "checkout", "-q", "-b", "user-work")
	u1 := r.commit(r.repo, "", "user 1")
	u2 := r.commit(r.repo, "", "fast-forward") // a user subject that once fooled the reflog reader
	r.git(r.repo, "", "checkout", "-q", "main")
	return r, u1, u2
}

// gitEnvWithoutIdentity drops an identity the test itself inherits (an
// Operator running this suite has its own committer set).
func gitEnvWithoutIdentity() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_COMMITTER_") && !strings.HasPrefix(kv, "GIT_AUTHOR_") {
			env = append(env, kv)
		}
	}
	return env
}

// git runs as agent ("" = the user) in wd.
func (r *opRepo) git(wd, agent string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
	cmd.Env = append(gitEnvWithoutIdentity(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if agent != "" {
		cmd.Env = append(cmd.Env, spawn.OperatorGitEnv(agent)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *opRepo) commit(wd, agent, msg string, extra ...string) string {
	r.git(wd, agent, append([]string{"commit", "--allow-empty", "--allow-empty-message", "-q", "-m", msg}, extra...)...)
	return r.git(wd, agent, "rev-parse", "HEAD")
}

// worktree adds the run's worktree as spawn does (-B from main).
func (r *opRepo) worktree(name string) (string, store.AgentRun) {
	wt := filepath.Join(r.wts, name)
	r.git(r.repo, "", "worktree", "add", "-q", "-B", "op/"+strings.TrimPrefix(name, "op-"), wt, "main")
	return wt, store.AgentRun{AgentName: "operator-" + name, TaskBeadID: name[len(name)-2:],
		WorktreePath: sql.NullString{String: wt, Valid: true}}
}

// Every way an Operator makes a commit object is attributed, whatever its
// message; every way it only moves a ref onto the user's commits is not.
func TestOperatorAuthorshipReadsCommitIdentities(t *testing.T) {
	ctx := context.Background()
	r, u1, u2 := newOpRepo(t)

	rev, revRun := r.worktree("op-rev-t1") // the review protocol: reset to the PR head, write nothing
	r.git(rev, revRun.AgentName, "reset", "-q", "--hard", u2)
	r.git(rev, revRun.AgentName, "merge", "-q", "--ff-only", "user-work")

	w, run := r.worktree("op-w-t2")
	a := run.AgentName
	empty := r.commit(w, a, "\nbody after an empty first line") // reflog "commit:" with no subject
	blank := r.commit(w, a, "")
	ff := r.commit(w, a, "fast-forward")
	r.commit(w, a, "to amend")
	amended := r.commit(w, a, "amended", "--amend")
	r.git(w, a, "cherry-pick", "--allow-empty", "--keep-redundant-commits", u2) // a commit titled "fast-forward"
	picked := r.git(w, a, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(w, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(w, a, "add", "f")
	file := r.commit(w, a, "file")
	r.git(w, a, "revert", "--no-edit", "HEAD")
	reverted := r.git(w, a, "rev-parse", "HEAD")
	r.git(w, a, "merge", "-q", "--no-ff", "--no-edit", "user-work")
	merged := r.git(w, a, "rev-parse", "HEAD")

	rb, rbRun := r.worktree("op-rb-t3")
	r.commit(rb, rbRun.AgentName, "own")
	r.git(rb, rbRun.AgentName, "rebase", "-q", "user-work")
	rebased := r.git(rb, rbRun.AgentName, "rev-parse", "HEAD")
	r.git(r.repo, "", "worktree", "remove", "--force", rb) // no worktree or branch needed
	r.git(r.repo, "", "branch", "-q", "-D", "op/rb-t3")

	// An Operator of a run this project never had (another project's).
	r.git(r.repo, "", "checkout", "-q", "main")
	stranger := r.commit(r.repo, "operator-op-elsewhere-t9", "stranger")
	missing := strings.Repeat("e", 40)

	runs := []store.AgentRun{revRun, run, rbRun}
	written := map[string]string{empty: a, blank: a, ff: a, amended: a, picked: a, file: a, reverted: a, merged: a, rebased: rbRun.AgentName}
	all := []string{u1, u2, stranger, missing}
	for sha := range written {
		all = append(all, sha)
	}
	got, err := operatorAuthorship(ctx, r.repo, runs, true, all)
	if err != nil {
		t.Fatal(err)
	}
	for sha, who := range written {
		if !strings.Contains(got[sha], who) {
			t.Errorf("%s: got %q, want written by %s", sha, got[sha], who)
		}
	}
	if !strings.Contains(got[stranger], "an Operator identity") {
		t.Errorf("another project's Operator commit: got %q", got[stranger])
	}
	for _, sha := range []string{u1, u2, missing} {
		if why, ok := got[sha]; ok {
			t.Errorf("user commit %s attributed: %s", sha, why)
		}
	}
	// Reviewer side: only that task's runs count. The reviewer that reset and
	// fast-forwarded onto the user's commits wrote none of them.
	if got, err := operatorAuthorship(ctx, r.repo, runs[:1], false, all); err != nil || len(got) != 0 {
		t.Fatalf("reviewer that reset to the PR head counted as a writer: %v %v", got, err)
	}
	if got, err := operatorAuthorship(ctx, r.repo, runs[2:], false, all); err != nil || len(got) != 1 || got[rebased] == "" {
		t.Fatalf("rebasing task: %v %v", got, err)
	}
	if got, err := operatorAuthorship(ctx, r.repo, nil, false, all); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	// No repo to read = error, never a pass.
	if _, err := operatorAuthorship(ctx, filepath.Join(r.repo, "nowhere"), runs, true, all); err == nil {
		t.Fatal("unreadable repo passed")
	}
	if _, err := operatorAuthorship(ctx, r.repo, runs, true, []string{"abc"}); err == nil {
		t.Fatal("short sha passed")
	}
}

// The user cherry-picking or rebasing an Operator's commit makes a new commit
// as the user, but the author header still names the Operator only when the
// Operator authored it; the committer identity is what the launcher sets, so
// the user's own rewrite of it is the user's.
func TestOperatorAuthorshipReadsTheAuthorToo(t *testing.T) {
	r, _, _ := newOpRepo(t)
	agent := "operator-op-x-t1"
	env := append(spawn.OperatorGitEnv(agent), "GIT_AUTHOR_EMAIL="+spawn.OperatorGitEmail(agent))
	cmd := exec.Command("git", "-C", r.repo, "commit", "--allow-empty", "-q", "-m", "authored")
	cmd.Env = append(gitEnvWithoutIdentity(), append([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	op := r.git(r.repo, "", "rev-parse", "HEAD")
	r.git(r.repo, "", "checkout", "-q", "user-work")
	r.git(r.repo, "", "cherry-pick", "--allow-empty", op)
	userCopy := r.git(r.repo, "", "rev-parse", "HEAD")
	got, err := operatorAuthorship(context.Background(), r.repo, []store.AgentRun{{AgentName: agent, TaskBeadID: "t1"}}, false, []string{userCopy})
	if err != nil || !strings.Contains(got[userCopy], agent) {
		t.Fatalf("author identity not read: %v %v", got, err)
	}
}
