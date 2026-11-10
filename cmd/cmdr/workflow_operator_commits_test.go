package main

import (
	"context"
	"database/sql"
	"fmt"
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

// An author header under the Operator domain counts as well as the committer:
// the user's copy of a commit an Operator authored names it by identity.
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

// gitAt runs git as agent ("" = the user) with every commit it makes stamped
// at unix time at.
func (r *opRepo) gitAt(at int64, wd, agent string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
	stamp := fmt.Sprintf("@%d +0000", at)
	cmd.Env = append(gitEnvWithoutIdentity(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_COMMITTER_DATE="+stamp, "GIT_AUTHOR_DATE="+stamp)
	if agent != "" {
		cmd.Env = append(cmd.Env, spawn.OperatorGitEnv(agent)...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// change writes a file in wd and commits it at time at.
func (r *opRepo) change(at int64, wd, agent, file, body string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(wd, file), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.gitAt(at, wd, agent, "add", file)
	r.gitAt(at, wd, agent, "commit", "-q", "-m", "change "+file)
	return r.gitAt(at, wd, agent, "rev-parse", "HEAD")
}

// A rewrite made outside an Operator (the user's rebase, amend or
// cherry-pick, GitHub's update-branch fetched back) leaves the user's or
// GitHub's identities on a copy of the Operator's change: the copy is still
// the Operator's, matched by patch-id against the original in the local repo.
// The user's own commit an Operator copied later stays the user's.
func TestOperatorAuthorshipFollowsRewritesOutsideAnOperator(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newOpRepo(t)
	const t0 = int64(1_800_000_000)
	w, run := r.worktree("op-w-t2")
	a := run.AgentName
	op1 := r.change(t0+10, w, a, "op1.txt", "operator one\n")
	op2 := r.change(t0+20, w, a, "op2.txt", "operator two\n")

	// main moves on, then the user rebases the Operator branch in place: both
	// commits now read as the user's and the branch no longer holds the
	// originals (only its reflog does).
	r.gitAt(t0+30, r.repo, "", "checkout", "-q", "main")
	r.change(t0+30, r.repo, "", "main.txt", "main moved\n")
	r.gitAt(t0+40, w, "", "rebase", "-q", "main")
	rebased2 := r.gitAt(t0+40, w, "", "rev-parse", "HEAD")
	rebased1 := r.gitAt(t0+40, w, "", "rev-parse", "HEAD~1")
	if who := r.git(w, "", "show", "-s", "--format=%ae %ce", rebased2); who != "user@example.com user@example.com" {
		t.Fatalf("rebase outside an Operator kept an Operator identity: %s", who)
	}

	// The user amends a cherry-picked copy without changing it (message only).
	r.gitAt(t0+50, r.repo, "", "checkout", "-q", "-b", "user-pick", "main")
	r.gitAt(t0+50, r.repo, "", "cherry-pick", op1)
	r.gitAt(t0+51, r.repo, "", "commit", "-q", "--amend", "-m", "reworded by the user")
	amended := r.gitAt(t0+51, r.repo, "", "rev-parse", "HEAD")

	// GitHub's update-branch: author kept, committer GitHub, fetched back.
	r.gitAt(t0+60, r.repo, "", "checkout", "-q", "-b", "gh", "main")
	cmd := exec.Command("git", "-C", r.repo, "cherry-pick", op2)
	cmd.Env = append(gitEnvWithoutIdentity(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_COMMITTER_NAME=GitHub", "GIT_COMMITTER_EMAIL=noreply@github.com", "GIT_COMMITTER_DATE=@1800000060 +0000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	github := r.git(r.repo, "", "rev-parse", "HEAD")

	// The user's own change, which an Operator copies afterwards: still the
	// user's (the Operator's copy is later).
	r.gitAt(t0+70, r.repo, "", "checkout", "-q", "-b", "mine", "main")
	mine := r.change(t0+70, r.repo, "", "mine.txt", "the user's own\n")
	cp, cpRun := r.worktree("op-cp-t4")
	r.gitAt(t0+80, cp, cpRun.AgentName, "cherry-pick", mine)
	r.gitAt(t0+80, r.repo, "", "checkout", "-q", "main")

	runs := []store.AgentRun{run, cpRun}
	all := []string{rebased1, rebased2, amended, github, mine}
	got, err := operatorAuthorship(ctx, r.repo, runs, true, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{rebased1, rebased2, amended, github} {
		if !strings.Contains(got[sha], "same change") || !strings.Contains(got[sha], a) {
			t.Errorf("copy %s outside an Operator: got %q, want the change of %s", sha, got[sha], a)
		}
	}
	if why, ok := got[mine]; ok {
		t.Errorf("the user's own commit, copied later by an Operator, attributed: %s", why)
	}
	// Reviewer side: only the task's own runs' originals count.
	if got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{cpRun}, false, all); err != nil || len(got) != 0 {
		t.Fatalf("another task's copies attributed to the reviewer: %v %v", got, err)
	}
	if got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{run}, false, all); err != nil || len(got) != 4 {
		t.Fatalf("writer task: %v %v", got, err)
	}

	// Limit: with the Operator's worktree and branch (and so their reflogs)
	// gone, no original is left to match and the copies pass.
	r.git(r.repo, "", "worktree", "remove", "--force", w)
	r.git(r.repo, "", "branch", "-q", "-D", "op/w-t2")
	if got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{run}, false, all); err != nil || len(got) != 0 {
		t.Fatalf("originals gone, yet matched: %v %v", got, err)
	}
}

// A replace ref cannot dress an Operator commit in the user's identity.
func TestOperatorAuthorshipIgnoresReplaceRefs(t *testing.T) {
	r, _, _ := newOpRepo(t)
	agent := "operator-op-x-t1"
	op := r.change(1_800_000_000, r.repo, agent, "x.txt", "x\n")
	r.git(r.repo, "", "checkout", "-q", "-b", "disguise", "main~1")
	disguise := r.change(1_800_000_001, r.repo, "", "y.txt", "y\n")
	r.git(r.repo, "", "replace", op, disguise)
	if who := r.git(r.repo, "", "show", "-s", "--format=%ce", op); who != "user@example.com" {
		t.Fatalf("replace ref not in effect: %s", who)
	}
	got, err := operatorAuthorship(context.Background(), r.repo, []store.AgentRun{{AgentName: agent, TaskBeadID: "t1"}}, false, []string{op})
	if err != nil || !strings.Contains(got[op], agent) {
		t.Fatalf("replace ref hid the Operator: %v %v", got, err)
	}
}

// The change's first appearance decides: the user's own change stays the
// user's after an Operator copies it (a rebase of the PR branch) and the user
// copies that copy again (another rebase, GitHub's update-branch).
func TestOperatorAuthorshipFirstAppearanceWins(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newOpRepo(t)
	const t0 = int64(1_800_000_000)
	r.gitAt(t0, r.repo, "", "checkout", "-q", "-b", "pr", "main")
	u := r.change(t0+10, r.repo, "", "u.txt", "the user's change\n")
	r.gitAt(t0+10, r.repo, "", "checkout", "-q", "main")
	r.change(t0+20, r.repo, "", "main.txt", "main moved\n")

	w, run := r.worktree("op-fix-t2")
	a := run.AgentName
	r.gitAt(t0+30, w, a, "checkout", "-q", "-B", "op/fix-t2", "pr")
	r.gitAt(t0+30, w, a, "rebase", "-q", "main") // the Operator's copy O of u
	o := r.gitAt(t0+30, w, a, "rev-parse", "HEAD")
	if o == u {
		t.Fatal("rebase made no copy")
	}
	r.gitAt(t0+40, r.repo, "", "checkout", "-q", "main")
	r.change(t0+40, r.repo, "", "main2.txt", "main moved again\n")
	r.gitAt(t0+50, r.repo, "", "checkout", "-q", "-b", "fin", "main")
	r.gitAt(t0+50, r.repo, "", "cherry-pick", o) // the user's copy FIN of O
	fin := r.gitAt(t0+50, r.repo, "", "rev-parse", "HEAD")

	// A change an Operator wrote first, copied by the user, then by an
	// Operator, then by the user again, stays the Operator's.
	op := r.change(t0+60, w, a, "op.txt", "the Operator's change\n")
	r.gitAt(t0+70, r.repo, "", "checkout", "-q", "-b", "u2", "main")
	r.gitAt(t0+70, r.repo, "", "cherry-pick", op)
	u2 := r.gitAt(t0+70, r.repo, "", "rev-parse", "HEAD")
	r.gitAt(t0+80, r.repo, "", "checkout", "-q", "main")

	for _, any := range []bool{true, false} {
		got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{run}, any, []string{u, fin, u2})
		if err != nil {
			t.Fatal(err)
		}
		if why, ok := got[fin]; ok {
			t.Errorf("the user's re-copy of the user's own change attributed: %s", why)
		}
		if why, ok := got[u]; ok {
			t.Errorf("the user's original attributed: %s", why)
		}
		if !strings.Contains(got[u2], a) {
			t.Errorf("the user's copy of the Operator's change: got %q", got[u2])
		}
	}

	// Failing closed: the same change in the same second as the user's
	// original counts as the Operator's.
	r.gitAt(t0+90, r.repo, "", "checkout", "-q", "-b", "tie", "main")
	tu := r.change(t0+90, r.repo, "", "tie.txt", "same\n")
	r.gitAt(t0+90, w, a, "checkout", "-q", "-B", "op/fix-t2", "main")
	r.change(t0+90, w, a, "tie.txt", "same\n")
	r.gitAt(t0+95, r.repo, "", "commit", "-q", "--amend", "-m", "reworded")
	tfin := r.gitAt(t0+95, r.repo, "", "rev-parse", "HEAD")
	got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{run}, false, []string{tu, tfin})
	if err != nil {
		t.Fatal(err)
	}
	if got[tu] == "" || got[tfin] == "" {
		t.Errorf("same-second tie not counted as the Operator's: %v", got)
	}

	// Any git failure refuses.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := operatorAuthorship(cancelled, r.repo, []store.AgentRun{run}, true, []string{fin}); err == nil {
		t.Fatal("a failed git read passed")
	}
}

// Originals are found wherever the local repo keeps them: a root commit, a
// change over more paths than the pathspec bound, a commit only a worktree's
// HEAD reflog still holds.
func TestOperatorAuthorshipFindsEveryOriginal(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newOpRepo(t)
	const t0 = int64(1_800_000_000)
	w, run := r.worktree("op-w-t2")
	a := run.AgentName

	r.gitAt(t0+10, w, a, "checkout", "-q", "--orphan", "op-root")
	r.gitAt(t0+10, w, a, "rm", "-rq", "--cached", "--ignore-unmatch", ".")
	root := r.change(t0+10, w, a, "root.txt", "a root commit\n")

	r.gitAt(t0+20, w, a, "checkout", "-q", "-f", "--detach", "main")
	for i := range maxCopyPaths + 1 {
		if err := os.WriteFile(filepath.Join(w, fmt.Sprintf("m%03d.txt", i)), []byte("many\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.gitAt(t0+20, w, a, "add", ".")
	r.gitAt(t0+20, w, a, "commit", "-q", "-m", "many paths")
	many := r.gitAt(t0+20, w, a, "rev-parse", "HEAD")
	detached := r.change(t0+21, w, a, "detached.txt", "only in the HEAD reflog\n")
	r.gitAt(t0+22, w, a, "checkout", "-q", "op/w-t2") // now nothing but HEAD's reflog holds them

	r.gitAt(t0+30, r.repo, "", "checkout", "-q", "-b", "copies", "main")
	var copies []string
	for _, sha := range []string{root, many, detached} {
		r.gitAt(t0+30, r.repo, "", "cherry-pick", sha)
		copies = append(copies, r.gitAt(t0+30, r.repo, "", "rev-parse", "HEAD"))
	}
	got, err := operatorAuthorship(ctx, r.repo, []store.AgentRun{run}, false, copies)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"root commit", "many paths", "HEAD reflog only"} {
		if !strings.Contains(got[copies[i]], "same change") {
			t.Errorf("%s: copy %s not matched: %q", name, copies[i], got[copies[i]])
		}
	}
}
