package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/example-user/commander/internal/spawn"
	"github.com/example-user/commander/internal/store"
	"github.com/example-user/commander/internal/workflowtemplate"
)

// workflowOperatorCommits says which of shas an Operator run of project wrote
// (sha -> why), task "" meaning every run of the project. An external writer is
// the user's own sessions, so a commit an Operator wrote is never bound as one,
// and a reviewer task that wrote a bound external commit is not independent.
var workflowOperatorCommits = func(ctx context.Context, project, task string, shas []string) (map[string]string, error) {
	st, done, err := needsYouStoreFn()
	if err != nil {
		return nil, err
	}
	defer done()
	p, err := st.GetProjectByName(ctx, project)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("unknown project %s", project)
	}
	var runs []store.AgentRun
	if task == "" {
		runs, err = st.ListAgentRunsByProject(ctx, p.ID, "")
	} else {
		runs, err = st.ListAgentRunsByTaskBeadID(ctx, task, "")
	}
	if err != nil {
		return nil, err
	}
	runs = slices.DeleteFunc(runs, func(r store.AgentRun) bool { return r.ProjectID != p.ID })
	repo := p.RepoPath
	if repo == "" {
		repo = p.Path
	}
	return operatorAuthorship(ctx, repo, runs, task == "", shas)
}

// operatorAuthorship attributes shas by the identity in each commit object,
// read only from the local repo. The launcher gives every Operator run its own
// committer identity (spawn.OperatorGitEmail), and git writes the committer of
// every commit object a process makes (commit, amend, cherry-pick, revert, am,
// merge, rebase), while a ref move (reset, fast-forward, checkout, branch -f)
// makes no object. So a commit whose author or committer is an Operator
// identity was written by that Operator, and a reviewer that reset onto the
// user's commits wrote none of them. No reflog text and no commit message is
// read: commit text cannot change the answer.
//
// A copy of an Operator's commit made outside an Operator (the user's rebase,
// amend or cherry-pick, GitHub's "Update branch" fetched back) carries the
// user's or GitHub's identities, so the rest are matched by change: a commit
// whose git patch-id equals that of an earlier Operator commit in the local
// repo (any ref or reflog, every worktree) is that Operator's. "Earlier" is by
// committer time: a rebase or cherry-pick stamps its copy with the time it ran,
// so the user's own commit an Operator later copied stays the user's.
//
// anyOperator (the project-wide read at bind) counts every identity under
// spawn.OperatorGitDomain, a run of this project or not; otherwise only the
// identities of runs count. A commit missing from the local repo passes. Any
// git failure is an error: the caller refuses. Replace refs are ignored.
//
// Limits: an Operator started before its launcher set the identity, or one
// that unsets it, commits as the configured user and is not attributed; a copy
// whose change differs (a squash, a conflict resolved, moved context lines, an
// edit while amending) or whose Operator original is gone from the local repo
// is not matched.
func operatorAuthorship(ctx context.Context, repo string, runs []store.AgentRun, anyOperator bool, shas []string) (map[string]string, error) {
	found := map[string]string{}
	if len(shas) == 0 || (len(runs) == 0 && !anyOperator) {
		return found, nil
	}
	for _, sha := range shas {
		if !workflowtemplate.FullSHA(sha) {
			return nil, fmt.Errorf("commit %q is not a full SHA", sha)
		}
	}
	// One read for all: "<sha> commit <size>" or "<sha> missing".
	out, err := gitRead(ctx, repo, strings.Join(shas, "\n")+"\n", "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("cannot read commits in %s: %w", repo, err)
	}
	var present []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "commit" && slices.Contains(shas, f[0]) && !slices.Contains(present, f[0]) {
			present = append(present, f[0])
		}
	}
	if len(present) == 0 {
		return found, nil
	}
	who := map[string]string{} // identity -> run
	for _, r := range runs {
		id := spawn.OperatorGitEmail(r.AgentName)
		if _, ok := who[id]; !ok {
			who[id] = fmt.Sprintf("Operator run %s (task %s)", r.AgentName, r.TaskBeadID)
		}
	}
	// The run (or, with anyOperator, the Operator identity) a commit's author
	// or committer names; "" = none.
	operator := func(author, committer string) string {
		for _, id := range []string{strings.ToLower(committer), strings.ToLower(author)} {
			if run, ok := who[id]; ok {
				return "committed as " + id + " by " + run
			}
			if anyOperator && strings.HasSuffix(id, "@"+spawn.OperatorGitDomain) {
				return "committed as " + id + ", an Operator identity"
			}
		}
		return ""
	}
	// %ae/%ce are the raw headers (no mailmap); %x09 cannot occur in either.
	ids, err := gitRead(ctx, repo, "", append([]string{"show", "-s", "--no-show-signature", "--format=%H%x09%ct%x09%ae%x09%ce"}, present...)...)
	if err != nil {
		return nil, fmt.Errorf("cannot read the identities of the commits in %s: %w", repo, err)
	}
	read := 0
	var rest []string
	when := map[string]int64{}
	for _, line := range strings.Split(ids, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || !slices.Contains(present, f[0]) {
			continue
		}
		read++
		if why := operator(f[2], f[3]); why != "" {
			found[f[0]] = why
			continue
		}
		t, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot read the commit time of %s in %s: %w", f[0], repo, err)
		}
		when[f[0]] = t
		rest = append(rest, f[0])
	}
	if read != len(present) {
		return nil, fmt.Errorf("read the identities of %d of %d commits in %s", read, len(present), repo)
	}
	if len(rest) == 0 {
		return found, nil
	}
	if err := operatorCopies(ctx, repo, rest, when, operator, found); err != nil {
		return nil, fmt.Errorf("cannot match the commits in %s by change: %w", repo, err)
	}
	return found, nil
}

// maxCopyPaths bounds the pathspec of the Operator commit scan (an argument
// list); past it every Operator commit is read.
const maxCopyPaths = 500

// operatorCopies adds to found every commit of rest whose patch-id equals
// that of an Operator commit (operator != "") in the local repo committed no
// later than it (when: committer time of each commit of rest).
func operatorCopies(ctx context.Context, repo string, rest []string, when map[string]int64, operator func(author, committer string) string, found map[string]string) error {
	ids, err := patchIDs(ctx, repo, rest)
	if err != nil {
		return err
	}
	if len(ids) == 0 { // only empty commits or merges: no change to match
		return nil
	}
	// Only a commit touching the same paths can have the same patch-id.
	names, err := gitRead(ctx, repo, strings.Join(rest, "\n")+"\n",
		"diff-tree", "--stdin", "-r", "--no-commit-id", "--name-only", "--no-renames", "-z")
	if err != nil {
		return err
	}
	var paths []string
	for _, p := range strings.Split(names, "\x00") {
		if p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	// Every ref and reflog of every worktree: a rebased-away or deleted
	// Operator branch is still in its reflog.
	logArgs := []string{"log", "--all", "--reflog", "--full-history", "--no-merges", "--no-show-signature",
		"--format=%H%x09%ct%x09%ae%x09%ce"}
	if len(paths) <= maxCopyPaths {
		logArgs = append(append(logArgs, "--"), paths...)
	}
	list, err := gitRead(ctx, repo, "", logArgs...)
	if err != nil {
		return err
	}
	ops := map[string]string{} // Operator commit -> why
	opTime := map[string]int64{}
	var opShas []string
	for _, line := range strings.Split(list, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || ops[f[0]] != "" {
			continue
		}
		why := operator(f[2], f[3])
		if why == "" {
			continue
		}
		t, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return fmt.Errorf("commit time of %s: %w", f[0], err)
		}
		ops[f[0]], opTime[f[0]] = why, t
		opShas = append(opShas, f[0])
	}
	if len(opShas) == 0 {
		return nil
	}
	opIDs, err := patchIDs(ctx, repo, opShas)
	if err != nil {
		return err
	}
	byID := map[string][]string{}
	for _, op := range opShas {
		if id := opIDs[op]; id != "" {
			byID[id] = append(byID[id], op)
		}
	}
	for _, sha := range rest {
		id := ids[sha]
		if id == "" {
			continue
		}
		for _, op := range byID[id] {
			if op != sha && opTime[op] <= when[sha] {
				found[sha] = "same change (patch-id " + id[:12] + ") as " + op[:12] + ", " + ops[op]
				break
			}
		}
	}
	return nil
}

// patchIDs is git patch-id --stable of each non-merge commit with a change.
func patchIDs(ctx context.Context, repo string, shas []string) (map[string]string, error) {
	diff, err := gitRead(ctx, repo, strings.Join(shas, "\n")+"\n",
		"log", "--stdin", "--no-walk=unsorted", "--no-merges", "-p", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--no-color", "--no-show-signature", "--src-prefix=a/", "--dst-prefix=b/", "--format=commit %H")
	if err != nil {
		return nil, err
	}
	out, err := gitRead(ctx, repo, diff+"\n", "patch-id", "--stable")
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && slices.Contains(shas, f[1]) {
			ids[f[1]] = f[0]
		}
	}
	return ids, nil
}

// gitRead runs a read-only git in repo with stdin (none when ""), ignoring
// replace refs (one could dress an Operator commit in the user's identity),
// and pathspec magic.
func gitRead(ctx context.Context, repo, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--literal-pathspecs", "-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1", "GIT_LITERAL_PATHSPECS=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
